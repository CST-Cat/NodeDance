package control

import (
	"github.com/CST-Cat/NodeDance/internal/core/domains"
)

func domainOperations() []Operation {
	list := []Operation{
		op("domains.options", "GET", "/api/v1/domains/options", "Domain, CA, DNS challenge and certificate policy options", nil),
		op("domains.list", "GET", "/api/v1/domains", "List managed domains", nil),
		op("domains.create", "POST", "/api/v1/domains", "Create a domain; Unicode names normalize to ASCII", schema(domains.DomainInput{})),
		op("domains.batch", "POST", "/api/v1/domains/batch", "Atomically create up to 500 domains; exact matching retries return existing records", schema(domains.BulkDomains{})),
		op("domains.get", "GET", "/api/v1/domains/{id}", "Read a managed domain", nil),
		op("domains.update", "PUT", "/api/v1/domains/{id}", "Update domain label and notes using current revision; name is immutable", schema(domains.DomainInput{})),
		op("domains.delete", "DELETE", "/api/v1/domains/{id}", "Delete an unused domain using current revision; confirm required", schema(domains.ConfirmInput{})),
		op("domains.dns", "GET", "/api/v1/domains/{id}/dns", "Resolve domain addresses and canonical name", nil),
		op("certificates.list", "GET", "/api/v1/certificates", "List certificate metadata and expiration state; never returns private keys", nil),
		op("certificates.get", "GET", "/api/v1/certificates/{id}", "Read certificate metadata, renewal policy and expiration state", nil),
		op("certificates.import", "POST", "/api/v1/certificates/import", "Import a matching PEM leaf/chain and private key; keep body in a private file or stdin", schema(domains.ImportInput{})),
		op("certificates.issue", "POST", "/api/v1/certificates/issue", "Queue ACME issuance for selected domains; confirm and stable request identity required", schema(domains.IssueInput{})),
		op("certificates.renew", "POST", "/api/v1/certificates/{id}/renew", "Queue renewal; retain the previous certificate until success", schema(domains.ConfirmInput{})),
		op("certificates.revoke", "POST", "/api/v1/certificates/{id}/revoke", "Queue CA revocation; explicit confirmation and current revision required", schema(domains.RevokeInput{})),
		op("certificates.policy", "PUT", "/api/v1/certificates/{id}/policy", "Update automatic renewal and expiry notification policy", schema(domains.PolicyInput{})),
		op("certificates.delete", "DELETE", "/api/v1/certificates/{id}", "Delete stored certificate and private key; does not revoke at CA; confirm required", schema(domains.ConfirmInput{})),
		op("certificates.pem", "GET", "/api/v1/certificates/{id}/pem", "Download the public certificate chain to --output", nil),
		op("certificates.export", "POST", "/api/v1/certificates/{id}/export", "Download private ZIP with chain and key; use --output, current revision and explicit confirmation", schema(domains.ConfirmInput{})),
		op("certificates.accounts", "GET", "/api/v1/certificates/accounts", "List ACME accounts without signing keys", nil),
		op("certificates.account-create", "POST", "/api/v1/certificates/accounts", "Create a protected ACME account; accept CA terms explicitly", schema(domains.AccountInput{})),
		op("certificates.account-delete", "DELETE", "/api/v1/certificates/accounts/{id}", "Delete an unused ACME account using current revision; confirm required", schema(domains.ConfirmInput{})),
		op("certificates.providers", "GET", "/api/v1/certificates/providers", "List DNS provider metadata without API tokens", nil),
		op("certificates.provider-create", "POST", "/api/v1/certificates/providers", "Store Cloudflare zone DNS API credentials privately", schema(domains.ProviderInput{})),
		op("certificates.provider-update", "PUT", "/api/v1/certificates/providers/{id}", "Update DNS provider using current revision; blank API token preserves the existing token", schema(domains.ProviderInput{})),
		op("certificates.provider-delete", "DELETE", "/api/v1/certificates/providers/{id}", "Delete an unused DNS provider; active challenge cleanup must finish first", schema(domains.ConfirmInput{})),
		op("certificates.jobs", "GET", "/api/v1/certificates/jobs", "List the latest 500 certificate jobs", nil),
		op("certificates.job", "GET", "/api/v1/certificates/jobs/{id}", "Observe certificate job status without resubmitting", nil),
		op("certificates.job-cancel", "POST", "/api/v1/certificates/jobs/{id}/cancel", "Cancel queued or running work; existing certificates remain intact", fields("confirm:boolean")),
		op("certificates.job-retry", "POST", "/api/v1/certificates/jobs/{id}/retry", "Retry failed or canceled work as a new job with a new request identity", fields("confirm:boolean")),
	}
	for i := range list {
		o := &list[i]
		p := map[string]any{}
		if b, ok := o.BodySchema.(map[string]any); ok {
			p, _ = b["properties"].(map[string]any)
		}
		field := func(name string, values map[string]any) {
			if f, ok := p[name].(map[string]any); ok {
				for k, v := range values {
					f[k] = v
				}
			}
		}
		require := func(names ...string) { o.BodySchema.(map[string]any)["required"] = names }
		field("name", map[string]any{"minLength": 1, "maxBytes": 253})
		field("label", map[string]any{"maxBytes": 200})
		field("notes", map[string]any{"maxBytes": 4096})
		field("revision", map[string]any{"minimum": 1})
		field("renewBeforeDays", map[string]any{"minimum": 1, "maximum": 90, "default": 30})
		field("notifyBeforeDays", map[string]any{"minimum": 1, "maximum": 90, "default": 14})
		field("channelIds", map[string]any{"maxItems": 20})
		field("domainIds", map[string]any{"minItems": 1, "maxItems": 100})
		field("confirm", map[string]any{"enum": []any{true}})
		field("termsAgreed", map[string]any{"enum": []any{true}})
		field("apiToken", map[string]any{"maxBytes": 4096})
		field("certificatePem", map[string]any{"maxBytes": domains.MaxPEMBytes})
		field("privateKeyPem", map[string]any{"maxBytes": domains.MaxPEMBytes})
		field("directory", map[string]any{"enum": []string{"letsencrypt", "letsencrypt-staging"}})
		field("challenge", map[string]any{"enum": []string{"http-01", "dns-01"}})
		field("kind", map[string]any{"enum": []string{"cloudflare"}})
		field("keyType", map[string]any{"enum": []string{"ec256", "rsa2048"}})
		field("reason", map[string]any{"enum": []int{0, 1, 3, 4, 5, 9}})
		switch o.Name {
		case "domains.create":
			require("name")
		case "domains.update":
			require("name", "revision")
		case "domains.batch":
			require("domains")
			field("domains", map[string]any{"minItems": 1, "maxItems": 500})
			items := p["domains"].(map[string]any)["items"].(map[string]any)
			items["required"] = []string{"name"}
		case "certificates.import":
			field("domainIds", map[string]any{"minItems": 0})
			require("name", "certificatePem", "privateKeyPem")
			o.MaxBodyBytes = 3 << 20
		case "certificates.account-create":
			require("name", "email", "directory", "termsAgreed")
		case "certificates.provider-create":
			require("name", "kind", "zoneId", "apiToken")
		case "certificates.provider-update":
			require("name", "kind", "zoneId", "revision")
		case "certificates.policy":
			require("revision", "autoRenew")
		case "certificates.issue":
			require("name", "domainIds", "accountId", "challenge", "keyType", "confirm")
		case "certificates.renew", "certificates.revoke", "certificates.export", "domains.delete", "certificates.delete", "certificates.account-delete", "certificates.provider-delete":
			require("revision", "confirm")
		case "certificates.job-cancel", "certificates.job-retry":
			require("confirm")
		}
		if o.Name == "certificates.issue" || o.Name == "certificates.renew" || o.Name == "certificates.revoke" || o.Name == "certificates.job-retry" {
			o.Idempotent = true
			o.Completion = &CompletionContract{Operation: "certificates.job", Params: map[string]string{"id": "data.id"}, StatusPath: "status", TerminalStatuses: []string{"succeeded", "failed", "canceled"}, SuccessStatuses: []string{"succeeded"}}
		}
		if o.Name == "certificates.pem" || o.Name == "certificates.export" {
			o.Transport = "download"
			o.MaxResponseBytes = 3 << 20
		}
		if o.MaxBodyBytes == 0 && o.BodySchema != nil {
			o.MaxBodyBytes = 256 << 10
		}
	}
	return list
}
