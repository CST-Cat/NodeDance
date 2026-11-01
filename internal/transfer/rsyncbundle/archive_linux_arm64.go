//go:build linux && arm64

package rsyncbundle

import _ "embed"

//go:embed assets/rsync-linux-arm64.tar.gz
var archive []byte

const archiveSHA256 = "a499caae423a2b9910316946447063896e3332c4b2d9f7950923935becf7ea07"
