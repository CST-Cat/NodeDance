package server

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/domains"
)

func domainError(w http.ResponseWriter, e error) {
	status := 400
	if errors.Is(e, sql.ErrNoRows) {
		status = 404
	} else if errors.Is(e, domains.ErrConflict) {
		status = 409
	}
	http.Error(w, e.Error(), status)
}
func (s *Server) domainEvent(r *http.Request) audit.Event {
	return audit.Event{OccurredAt: s.now(), ActorID: requestActor(r), RemoteAddr: s.effectiveRemoteAddr(r)}
}
func (s *Server) handleDomainAPI(w http.ResponseWriter, r *http.Request) bool {
	base := ""
	for _, candidate := range []string{"/api/v1/domains", "/api/v1/certificates"} {
		if r.URL.Path == candidate || strings.HasPrefix(r.URL.Path, candidate+"/") {
			base = candidate
			break
		}
	}
	if base == "" {
		return false
	}
	if s.domains == nil {
		http.Error(w, "domain management is unavailable", 503)
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	path := strings.TrimPrefix(r.URL.Path, base)
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	event := s.domainEvent(r)
	send := func(v any, e error, status int) {
		if e != nil {
			domainError(w, e)
		} else if status == 204 {
			w.WriteHeader(204)
		} else {
			writeJSON(w, status, v)
		}
	}
	if base == "/api/v1/domains" {
		if path == "/options" && r.Method == "GET" {
			writeJSON(w, 200, map[string]any{"directories": []string{"letsencrypt", "letsencrypt-staging"}, "challenges": []string{"http-01", "dns-01"}, "providerKinds": []string{"cloudflare"}, "keyTypes": []string{"ec256", "rsa2048"}, "httpChallengePath": "/.well-known/acme-challenge/", "renewBeforeDays": 30, "notifyBeforeDays": 14, "maxDomainsPerCertificate": 100, "maxBatchDomains": 500})
			return true
		}
		if path == "" {
			switch r.Method {
			case "GET":
				v, e := s.domains.Domains(r.Context())
				send(map[string]any{"domains": v}, e, 200)
			case "POST":
				var in domains.DomainInput
				if !decodeJSON(w, r, &in) {
					return true
				}
				v, e := s.domains.SaveDomain(r.Context(), "", in, event)
				send(v, e, 201)
			default:
				http.Error(w, "method not allowed", 405)
			}
			return true
		}
		if path == "/batch" && r.Method == "POST" {
			var in domains.BulkDomains
			if !decodeJSON(w, r, &in) {
				return true
			}
			v, e := s.domains.BulkDomains(r.Context(), in, event)
			send(map[string]any{"domains": v}, e, 200)
			return true
		}
		if !validUUID(parts[0]) {
			http.NotFound(w, r)
			return true
		}
		id := parts[0]
		if len(parts) == 2 && parts[1] == "dns" && r.Method == "GET" {
			v, e := s.domains.CheckDNS(r.Context(), id)
			send(v, e, 200)
			return true
		}
		if len(parts) == 1 {
			switch r.Method {
			case "GET":
				v, e := s.domains.Domain(r.Context(), id)
				send(v, e, 200)
			case "PUT":
				var in domains.DomainInput
				if !decodeJSON(w, r, &in) {
					return true
				}
				v, e := s.domains.SaveDomain(r.Context(), id, in, event)
				send(v, e, 200)
			case "DELETE":
				var in domains.ConfirmInput
				if !decodeJSON(w, r, &in) {
					return true
				}
				send(nil, s.domains.Delete(r.Context(), "domains", id, in, event), 204)
			default:
				http.Error(w, "method not allowed", 405)
			}
			return true
		}
	} else {
		if path == "" && r.Method == "GET" {
			v, e := s.domains.Certificates(r.Context())
			send(map[string]any{"certificates": v}, e, 200)
			return true
		}
		if path == "/import" && r.Method == "POST" {
			var in domains.ImportInput
			if !decodeJSON(w, r, &in) {
				return true
			}
			v, e := s.domains.Import(r.Context(), in, event)
			send(v, e, 201)
			return true
		}
		if path == "/issue" && r.Method == "POST" {
			var in domains.IssueInput
			if !decodeJSON(w, r, &in) {
				return true
			}
			v, e := s.domains.QueueIssue(r.Context(), in, r.Header.Get("Idempotency-Key"), event)
			send(v, e, 202)
			return true
		}
		if parts[0] == "accounts" {
			if len(parts) == 1 {
				switch r.Method {
				case "GET":
					v, e := s.domains.Accounts(r.Context())
					send(map[string]any{"accounts": v}, e, 200)
				case "POST":
					var in domains.AccountInput
					if !decodeJSON(w, r, &in) {
						return true
					}
					v, e := s.domains.SaveAccount(r.Context(), in, event)
					send(v, e, 201)
				default:
					http.Error(w, "method not allowed", 405)
				}
				return true
			}
			if len(parts) == 2 && validUUID(parts[1]) && r.Method == "DELETE" {
				var in domains.ConfirmInput
				if !decodeJSON(w, r, &in) {
					return true
				}
				send(nil, s.domains.Delete(r.Context(), "accounts", parts[1], in, event), 204)
				return true
			}
		}
		if parts[0] == "providers" {
			if len(parts) == 1 && r.Method == "GET" {
				v, e := s.domains.Providers(r.Context())
				send(map[string]any{"providers": v}, e, 200)
				return true
			}
			if (len(parts) == 1 && r.Method == "POST") || (len(parts) == 2 && validUUID(parts[1]) && r.Method == "PUT") {
				var in domains.ProviderInput
				if !decodeJSON(w, r, &in) {
					return true
				}
				id := ""
				status := 201
				if len(parts) == 2 {
					id = parts[1]
					status = 200
				}
				v, e := s.domains.SaveProvider(r.Context(), id, in, event)
				send(v, e, status)
				return true
			}
			if len(parts) == 2 && validUUID(parts[1]) && r.Method == "DELETE" {
				var in domains.ConfirmInput
				if !decodeJSON(w, r, &in) {
					return true
				}
				send(nil, s.domains.Delete(r.Context(), "providers", parts[1], in, event), 204)
				return true
			}
		}
		if parts[0] == "jobs" {
			if len(parts) == 1 && r.Method == "GET" {
				v, e := s.domains.Jobs(r.Context())
				send(map[string]any{"jobs": v}, e, 200)
				return true
			}
			if len(parts) >= 2 && validUUID(parts[1]) {
				id := parts[1]
				if len(parts) == 2 && r.Method == "GET" {
					v, e := s.domains.Job(r.Context(), id)
					send(v, e, 200)
					return true
				}
				if len(parts) == 3 && r.Method == "POST" {
					var in struct {
						Confirm bool `json:"confirm"`
					}
					if !decodeJSON(w, r, &in) {
						return true
					}
					if !in.Confirm {
						http.Error(w, "explicit confirmation is required", 400)
						return true
					}
					switch parts[2] {
					case "cancel":
						v, e := s.domains.Cancel(r.Context(), id, event)
						send(v, e, 200)
						return true
					case "retry":
						v, e := s.domains.Retry(r.Context(), id, r.Header.Get("Idempotency-Key"), event)
						send(v, e, 202)
						return true
					}
				}
			}
		}
		if validUUID(parts[0]) {
			id := parts[0]
			if len(parts) == 1 {
				switch r.Method {
				case "GET":
					v, e := s.domains.Certificate(r.Context(), id)
					send(v, e, 200)
				case "DELETE":
					var in domains.ConfirmInput
					if !decodeJSON(w, r, &in) {
						return true
					}
					send(nil, s.domains.Delete(r.Context(), "certificates", id, in, event), 204)
				default:
					http.Error(w, "method not allowed", 405)
				}
				return true
			}
			if len(parts) == 2 {
				switch parts[1] {
				case "policy":
					if r.Method == "PUT" {
						var in domains.PolicyInput
						if !decodeJSON(w, r, &in) {
							return true
						}
						v, e := s.domains.UpdatePolicy(r.Context(), id, in, event)
						send(v, e, 200)
						return true
					}
				case "renew":
					if r.Method == "POST" {
						var in domains.ConfirmInput
						if !decodeJSON(w, r, &in) {
							return true
						}
						v, e := s.domains.QueueRenew(r.Context(), id, in, r.Header.Get("Idempotency-Key"), event)
						send(v, e, 202)
						return true
					}
				case "revoke":
					if r.Method == "POST" {
						var in domains.RevokeInput
						if !decodeJSON(w, r, &in) {
							return true
						}
						v, e := s.domains.QueueRevoke(r.Context(), id, in, r.Header.Get("Idempotency-Key"), event)
						send(v, e, 202)
						return true
					}
				case "pem":
					if r.Method == "GET" {
						cert, _, e := s.domains.Material(r.Context(), id)
						if e != nil {
							domainError(w, e)
							return true
						}
						w.Header().Set("Content-Type", "application/x-pem-file")
						w.Header().Set("Content-Disposition", `attachment; filename="certificate.pem"`)
						w.Write(cert)
						return true
					}
				case "export":
					if r.Method == "POST" {
						var in domains.ConfirmInput
						if !decodeJSON(w, r, &in) {
							return true
						}
						cert, key, e := s.domains.Export(r.Context(), id, in, event)
						if e != nil {
							domainError(w, e)
							return true
						}
						var buffer bytes.Buffer
						archive := zip.NewWriter(&buffer)
						for _, file := range []struct {
							name string
							body []byte
						}{{"certificate.pem", cert}, {"private-key.pem", key}} {
							header := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
							header.SetMode(0600)
							writer, e := archive.CreateHeader(header)
							if e != nil {
								http.Error(w, "export failed", 500)
								return true
							}
							if _, e = writer.Write(file.body); e != nil {
								http.Error(w, "export failed", 500)
								return true
							}
						}
						if e = archive.Close(); e != nil {
							http.Error(w, "export failed", 500)
							return true
						}
						w.Header().Set("Content-Type", "application/zip")
						w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="certificate-%s.zip"`, id))
						w.Write(buffer.Bytes())
						return true
					}
				}
			}
		}
	}
	http.Error(w, "method or route not available", 405)
	return true
}
