//go:build linux && arm64

package tmuxbundle

import _ "embed"

//go:embed assets/linux-arm64.tar.gz
var archive []byte

const archiveSHA256 = "29dcb978da2a4b0cf6790f9e004865dac239a3ba03bbf776dddacce23d03831b"
