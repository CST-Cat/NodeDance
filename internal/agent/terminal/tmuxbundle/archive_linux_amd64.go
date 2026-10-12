//go:build linux && amd64

package tmuxbundle

import _ "embed"

//go:embed assets/linux-amd64.tar.gz
var archive []byte

const archiveSHA256 = "cc56bd1cc873eb6089c615f0496b072385bda8a6d944069f38564a5d49c128aa"
