package control

import "github.com/CST-Cat/NodeDance/internal/core/shares"

func shareOperations() []Operation {
	list := []Operation{
		op("share-requests.list", "GET", "/api/v1/share-requests", "Own file collection links", nil),
		op("share-requests.create", "POST", "/api/v1/share-requests", "Create invitation-only file collection link", schema(shares.RequestInput{})),
		op("share-requests.get", "GET", "/api/v1/share-requests/{id}", "Private collected files", nil),
		op("share-requests.update", "PUT", "/api/v1/share-requests/{id}", "Edit collection limits", schema(shares.RequestInput{})),
		op("share-requests.revoke", "DELETE", "/api/v1/share-requests/{id}", "Revoke collection link and remove collected entries", nil, "revision"),
		op("share-requests.download", "GET", "/api/v1/share-requests/{id}/download", "Owner download of received file", nil, "path"),
		op("share-requests.public", "GET", "/api/v1/public/share-requests/{id}", "Public collection invitation; never exposes collected files", nil),
		op("share-requests.upload", "PUT", "/api/v1/public/share-requests/{id}/upload", "Upload to an invitation; no remote filesystem access", nil, "name"),
		op("shares.upload", "PUT", "/api/v1/shares/upload", "Upload a file directly into sharing storage", nil, "name", "expiryDays", "maxDownloads", "requestId"),
		op("shares.list", "GET", "/api/v1/shares", "Own file shares", nil),
		op("shares.create", "POST", "/api/v1/shares", "Publish file or folder snapshots; requestId is a persistent random 64-character hex intent", schema(shares.Input{})),
		op("shares.get", "GET", "/api/v1/shares/{id}", "Share settings and download counts", nil),
		op("shares.update", "PUT", "/api/v1/shares/{id}", "Edit limits/password or replace content from another owned share", schema(shares.Input{})),
		op("shares.revoke", "DELETE", "/api/v1/shares/{id}", "Immediately revoke a share without deleting source files", nil, "revision"),
		op("shares.download", "GET", "/api/v1/shares/{id}/download", "Owner download without consuming public count", nil, "path"),
		op("shares.public", "GET", "/api/v1/public/shares/{id}", "Public share metadata; protected contents require password", nil),
		op("shares.unlock", "POST", "/api/v1/public/shares/{id}/unlock", "Unlock public share metadata", fields("password:string")),
		op("shares.receive", "POST", "/api/v1/public/shares/{id}/download", "Download a published file, consuming one download", fields("password:string"), "path"),
		op("shares.hotlink", "GET", "/api/v1/public/shares/{id}/hotlink", "Direct download of a share without a password; counts once", nil, "path"),
	}
	for i := range list {
		switch list[i].Name {
		case "shares.upload", "share-requests.upload":
			list[i].Transport = "upload"
			list[i].Headers = map[string]any{}
		case "share-requests.download", "shares.download", "shares.receive", "shares.hotlink":
			list[i].Transport = "download"
		}
	}
	return list
}
