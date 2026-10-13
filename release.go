// Package nodedance provides the Agent release paired with this Core build.
package nodedance

import _ "embed"

// PairedAgentRelease is verified as a published stable Agent release by Core CI.
//
//go:embed agent-release.version
var PairedAgentRelease string
