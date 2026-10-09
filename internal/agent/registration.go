package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type assignedIdentity struct {
	AgentID         string `json:"agentId"`
	NodeID          string `json:"nodeId"`
	DisplayName     string `json:"displayName"`
	Status          string `json:"status"`
	CredentialState string `json:"credentialState"`
}

func Enroll(ctx context.Context, server, caFile string, development bool, tokenReader io.Reader, configPath string) error {
	parsedServer, err := ParseServerURL(server, development)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(configPath); err == nil {
		return errors.New("Agent credential file already exists; use recover if enrollment may have completed")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Agent credential file: %w", err)
	}
	token, err := ReadEnrollmentToken(tokenReader)
	if err != nil {
		return err
	}
	credential, err := NewCredential()
	if err != nil {
		return fmt.Errorf("generate device credential: %w", err)
	}
	requestID, err := NewRequestID()
	if err != nil {
		return fmt.Errorf("generate enrollment request ID: %w", err)
	}
	configuredShell := strings.TrimSpace(os.Getenv("SHELL"))
	if configuredShell == "" || !filepath.IsAbs(configuredShell) || strings.ContainsAny(configuredShell, "\x00\r\n") {
		configuredShell = "/bin/sh"
	}
	config := Config{Schema: ConfigSchema, Server: parsedServer, Shell: configuredShell, CAFile: caFile, Development: development,
		Credential: credential, EnrollmentToken: token, RequestID: requestID, CreatedAt: time.Now().UTC()}
	if err := SaveConfig(configPath, config, true); err != nil {
		return err
	}
	client, err := newHTTPClient(caFile)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	result, err := consumeEnrollment(ctx, client, config)
	if err != nil {
		// The request may have committed even if the response was lost. Resolve
		// identity only with the already-persisted device credential; never
		// replay the one-use enrollment token or store it in Core.
		if recovered, recoverErr := lookupIdentity(ctx, client, config, credential); recoverErr == nil && recovered.CredentialState == "active" {
			result = recovered
			err = nil
		}
	}
	if err != nil {
		return err
	}
	if !isUUID(result.AgentID) || !isUUID(result.NodeID) || result.CredentialState != "active" {
		return errors.New("Core returned an invalid Agent identity")
	}
	config.AgentID, config.NodeID = result.AgentID, result.NodeID
	config.EnrollmentToken = ""
	if err := SaveConfig(configPath, config, false); err != nil {
		return fmt.Errorf("persist assigned Agent identity; recover with the saved credential: %w", err)
	}
	return nil
}

func Recover(ctx context.Context, configPath string) error {
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	client, err := newHTTPClient(config.CAFile)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	var result assignedIdentity
	usedPendingCredential := false
	pendingCredentialKnown := false
	if config.PendingCredential != "" {
		result, err = lookupIdentity(ctx, client, config, config.PendingCredential)
		if err == nil {
			pendingCredentialKnown = true
			usedPendingCredential = result.CredentialState == "active"
		} else {
			result, err = lookupIdentity(ctx, client, config, config.Credential)
		}
	} else {
		result, err = lookupIdentity(ctx, client, config, config.Credential)
	}
	if err != nil {
		return err
	}
	if !pendingCredentialKnown && result.CredentialState != "active" {
		return errors.New("current Agent credential is not active on Core")
	}
	if config.NodeID != "" && (config.NodeID != result.NodeID || config.AgentID != result.AgentID) {
		return errors.New("recovered Agent identity conflicts with the saved identity")
	}
	config.NodeID, config.AgentID = result.NodeID, result.AgentID
	config.EnrollmentToken = ""
	if usedPendingCredential {
		// Only Core's active state proves its transaction promoted the pending
		// verifier and retired the old one. Pending alone is not an ACK.
		config.Credential = config.PendingCredential
		config.PendingCredential = ""
		config.PendingRotationID = ""
	}
	return SaveConfig(configPath, config, false)
}

func consumeEnrollment(ctx context.Context, client *http.Client, config Config) (assignedIdentity, error) {
	body, err := json.Marshal(map[string]string{"requestId": config.RequestID, "credential": config.Credential})
	if err != nil {
		return assignedIdentity{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.Server+"/api/v1/agents/enroll", bytes.NewReader(body))
	if err != nil {
		return assignedIdentity{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Enrollment "+config.EnrollmentToken)
	response, err := client.Do(request)
	if err != nil {
		return assignedIdentity{}, fmt.Errorf("enrollment request failed; run recover after the server is reachable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return assignedIdentity{}, fmt.Errorf("enrollment request was rejected with HTTP %d", response.StatusCode)
	}
	var result assignedIdentity
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return assignedIdentity{}, fmt.Errorf("Core enrollment response is invalid")
	}
	if result.CredentialState != "active" {
		return assignedIdentity{}, errors.New("Core enrollment response is invalid")
	}
	return result, nil
}

func lookupIdentity(ctx context.Context, client *http.Client, config Config, credential string) (assignedIdentity, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, config.Server+"/api/v1/agents/identity", nil)
	if err != nil {
		return assignedIdentity{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+credential)
	response, err := client.Do(request)
	if err != nil {
		return assignedIdentity{}, fmt.Errorf("Agent identity recovery request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return assignedIdentity{}, fmt.Errorf("Agent identity recovery was rejected with HTTP %d", response.StatusCode)
	}
	var result assignedIdentity
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || !isUUID(result.AgentID) || !isUUID(result.NodeID) || result.CredentialState != "active" && result.CredentialState != "pending" {
		return assignedIdentity{}, errors.New("Core identity recovery response is invalid")
	}
	return result, nil
}

func newHTTPClient(caFile string) (*http.Client, error) {
	tlsConfig, err := newTLSConfig(caFile)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig, Proxy: http.ProxyFromEnvironment}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func newTLSConfig(caFile string) (*tls.Config, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		pem, err := os.ReadFile(filepath.Clean(caFile))
		if err != nil {
			return nil, fmt.Errorf("read Agent CA file: %w", err)
		}
		if ok := roots.AppendCertsFromPEM(pem); !ok {
			return nil, errors.New("Agent CA file contains no certificates")
		}
		tlsConfig.RootCAs = roots
	}
	return tlsConfig, nil
}
