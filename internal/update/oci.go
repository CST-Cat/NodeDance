package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
)

const (
	devBinaryRepository = "cst-cat/nodedance-agent-binaries"
	devImageRepository = "cst-cat/nodedance-agent"
	devArtifactType = "application/vnd.nodedance.agent.dev.v1"
	devBinaryMediaType = "application/vnd.nodedance.agent.binary.v1"
	ociManifestType = "application/vnd.oci.image.manifest.v1+json"
	ociIndexType = "application/vnd.oci.image.index.v1+json"
	dockerIndexType = "application/vnd.docker.distribution.manifest.list.v2+json"
)

type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest string `json:"digest"`
	Size int64 `json:"size"`
	Annotations map[string]string `json:"annotations"`
	Platform *struct {
		OS string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform"`
}

type ociManifest struct {
	SchemaVersion int `json:"schemaVersion"`
	MediaType string `json:"mediaType"`
	ArtifactType string `json:"artifactType"`
	Annotations map[string]string `json:"annotations"`
	Layers []ociDescriptor `json:"layers"`
	Manifests []ociDescriptor `json:"manifests"`
}

func ociDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func (g *GitHub) registryGet(ctx context.Context, repository, resource, accept string) (*http.Response, error) {
	values := url.Values{}
	values.Set("service", "ghcr.io")
	values.Set("scope", "repository:"+repository+":pull")
	raw, err := g.fetch(ctx, "https://ghcr.io/token?"+values.Encode(), 16<<10)
	if err != nil {
		return nil, fmt.Errorf("GHCR public pull token unavailable (check package visibility): %w", err)
	}
	var token struct {
		Token string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &token); err != nil {
		return nil, err
	}
	if token.Token == "" {
		token.Token = token.AccessToken
	}
	if token.Token == "" {
		return nil, errors.New("GHCR did not return a public read token")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ghcr.io/v2/"+repository+"/"+resource, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("Authorization", "Bearer "+token.Token)
	request.Header.Set("User-Agent", "NodeDance")
	response, err := g.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("GHCR %s: HTTP %d (public package access required)", resource, response.StatusCode)
	}
	return response, nil
}

func (g *GitHub) registryManifest(ctx context.Context, repository, reference, accept string) ([]byte, ociManifest, string, error) {
	var manifest ociManifest
	response, err := g.registryGet(ctx, repository, "manifests/"+reference, accept)
	if err != nil {
		return nil, manifest, "", err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
	if err != nil {
		return nil, manifest, "", err
	}
	if len(raw) > 128<<10 {
		return nil, manifest, "", errors.New("OCI manifest is too large")
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, manifest, "", err
	}
	if manifest.SchemaVersion != 2 {
		return nil, manifest, "", errors.New("unexpected OCI schema")
	}
	sum := sha256.Sum256(raw)
	digest := "sha256:"+hex.EncodeToString(sum[:])
	if serverDigest := response.Header.Get("Docker-Content-Digest"); serverDigest != "" && serverDigest != digest {
		return nil, manifest, "", errors.New("OCI manifest digest does not match registry response")
	}
	return raw, manifest, digest, nil
}

func (g *GitHub) resolveDev(ctx context.Context, version string) (Release, error) {
	reference := "dev"
	if ValidDevVersion(version) {
		reference = version
	}
	_, manifest, _, err := g.registryManifest(ctx, devBinaryRepository, reference, ociManifestType)
	if err != nil {
		return Release{}, err
	}
	if manifest.MediaType != ociManifestType || manifest.ArtifactType != devArtifactType || len(manifest.Layers) != 2 {
		return Release{}, errors.New("unexpected Agent Dev OCI artifact")
	}
	revision := manifest.Annotations["org.opencontainers.image.revision"]
	resolvedVersion := "agent-dev-"+revision
	if !ValidDevVersion(resolvedVersion) || ValidDevVersion(version) && version != resolvedVersion {
		return Release{}, errors.New("invalid or mismatched Agent Dev commit")
	}
	expected := map[string]bool{
		"nodedance-agent-linux-amd64": false,
		"nodedance-agent-linux-arm64": false,
	}
	var binaryDigest string
	var binarySize int64
	for _, layer := range manifest.Layers {
		name := layer.Annotations["org.opencontainers.image.title"]
		_, exists := expected[name]
		if !exists || expected[name] || layer.MediaType != devBinaryMediaType || !ociDigest(layer.Digest) || layer.Size < 1 || layer.Size > 256<<20 {
			return Release{}, errors.New("invalid Agent Dev OCI artifact layer")
		}
		expected[name] = true
		if name == "nodedance-agent-linux-"+runtime.GOARCH {
			binaryDigest = layer.Digest
			binarySize = layer.Size
		}
	}
	if !expected["nodedance-agent-linux-amd64"] || !expected["nodedance-agent-linux-arm64"] || binaryDigest == "" {
		return Release{}, errors.New("Agent Dev artifact lacks the selected architecture")
	}
	_, index, imageDigest, err := g.registryManifest(ctx, devImageRepository, "dev-"+revision, ociIndexType+", "+dockerIndexType)
	if err != nil {
		return Release{}, fmt.Errorf("paired Agent Dev Docker image unavailable: %w", err)
	}
	if index.MediaType != ociIndexType && index.MediaType != dockerIndexType {
		return Release{}, errors.New("Agent Dev Docker image is not a multi-platform index")
	}
	platforms := map[string]bool{}
	for _, item := range index.Manifests {
		if !ociDigest(item.Digest) || item.Size < 1 {
			return Release{}, errors.New("invalid Docker image descriptor")
		}
		if item.Platform != nil && item.Platform.OS == "linux" {
			platforms[item.Platform.Architecture] = true
		}
	}
	if !platforms["amd64"] || !platforms["arm64"] {
		return Release{}, errors.New("Agent Dev Docker image is missing amd64 or arm64")
	}
	return Release{
		Component: "agent",
		Version: resolvedVersion,
		URL: "https://github.com/"+Repository+"/commit/"+revision,
		BinaryDigest: binaryDigest,
		BinarySize: binarySize,
		ImageDigest: imageDigest,
	}, nil
}

func (g *GitHub) downloadDev(ctx context.Context, release Release, path string) error {
	if release.Component != "agent" || !ValidDevVersion(release.Version) || !ociDigest(release.BinaryDigest) || release.BinarySize < 1 || release.BinarySize > 256<<20 {
		return errors.New("invalid Agent Dev download descriptor")
	}
	response, err := g.registryGet(ctx, devBinaryRepository, "blobs/"+release.BinaryDigest, "application/octet-stream")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	defer file.Close()
	complete := false
	defer func() {
		if !complete {
			os.Remove(path)
		}
	}()
	hasher := sha256.New()
	writer := &releaseWriter{file: file}
	count, err := io.Copy(io.MultiWriter(writer, hasher), io.LimitReader(response.Body, release.BinarySize+1))
	if err != nil {
		return err
	}
	if count != release.BinarySize || "sha256:"+hex.EncodeToString(hasher.Sum(nil)) != release.BinaryDigest {
		return errors.New("Agent Dev OCI layer size or SHA256 mismatch")
	}
	if err := writer.flush(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}
