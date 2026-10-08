// Package protocol contains the versioned Core-Agent wire contract.
//
// Stage S00 intentionally defines only compatibility metadata. No Agent
// enrollment or control messages are accepted until their stages are built.
package protocol

const (
	// CurrentVersion is incremented when an incompatible wire change is made.
	CurrentVersion = 1
	// MinimumVersion is the oldest wire version Core will eventually accept.
	MinimumVersion = 1
)
