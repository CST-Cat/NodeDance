// Package domains owns domain inventory, private certificate material and ACME jobs.
package domains

import "time"

const MaxPEMBytes = 1 << 20

type DomainInput struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Notes    string `json:"notes"`
	Revision int64  `json:"revision,omitempty"`
}
type Domain struct {
	ID string `json:"id"`
	DomainInput
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type AccountInput struct {
	Name        string `json:"name"`
	Email       string `json:"email"`
	Directory   string `json:"directory"`
	TermsAgreed bool   `json:"termsAgreed"`
	Revision    int64  `json:"revision,omitempty"`
}
type Account struct {
	ID string `json:"id"`
	AccountInput
	Registered bool `json:"registered"`
}
type ProviderInput struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	ZoneID   string `json:"zoneId"`
	APIToken string `json:"apiToken"`
	Revision int64  `json:"revision,omitempty"`
}
type Provider struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	ZoneID   string `json:"zoneId"`
	HasToken bool   `json:"hasToken"`
	Revision int64  `json:"revision,omitempty"`
}
type Policy struct {
	AutoRenew        bool     `json:"autoRenew"`
	RenewBeforeDays  int      `json:"renewBeforeDays,omitempty"`
	NotifyBeforeDays int      `json:"notifyBeforeDays,omitempty"`
	ChannelIDs       []string `json:"channelIds"`
}
type PolicyInput struct {
	Policy
	Revision int64 `json:"revision,omitempty"`
}
type ImportInput struct {
	Name             string   `json:"name"`
	CertificatePEM   string   `json:"certificatePem"`
	PrivateKeyPEM    string   `json:"privateKeyPem"`
	DomainIDs        []string `json:"domainIds"`
	NotifyBeforeDays int      `json:"notifyBeforeDays,omitempty"`
	ChannelIDs       []string `json:"channelIds"`
}
type IssueInput struct {
	Name       string   `json:"name"`
	DomainIDs  []string `json:"domainIds"`
	AccountID  string   `json:"accountId"`
	Challenge  string   `json:"challenge"`
	ProviderID string   `json:"providerId"`
	KeyType    string   `json:"keyType"`
	Policy
	Confirm bool `json:"confirm"`
}
type ConfirmInput struct {
	Confirm  bool  `json:"confirm"`
	Revision int64 `json:"revision,omitempty"`
}
type Certificate struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	DomainIDs     []string  `json:"domainIds"`
	DNSNames      []string  `json:"dnsNames"`
	Issuer        string    `json:"issuer"`
	Serial        string    `json:"serial"`
	Fingerprint   string    `json:"fingerprint"`
	KeyType       string    `json:"keyType"`
	NotBefore     time.Time `json:"notBefore"`
	NotAfter      time.Time `json:"notAfter"`
	DaysRemaining int       `json:"daysRemaining"`
	Status        string    `json:"status"`
	Source        string    `json:"source"`
	AccountID     string    `json:"accountId"`
	Challenge     string    `json:"challenge"`
	ProviderID    string    `json:"providerId"`
	Policy
	Revision          int64     `json:"revision,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	NotificationError string    `json:"notificationError"`
}
type Job struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	CertificateID string    `json:"certificateId"`
	Error         string    `json:"error"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}
type DNSResult struct {
	Name      string    `json:"name"`
	Addresses []string  `json:"addresses"`
	CNAME     string    `json:"cname"`
	CheckedAt time.Time `json:"checkedAt"`
	Error     string    `json:"error"`
}
type BulkDomains struct {
	Domains []DomainInput `json:"domains"`
}
