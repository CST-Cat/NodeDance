//go:build linux && amd64

package rsyncbundle

import _ "embed"

//go:embed assets/rsync-linux-amd64.tar.gz
var archive []byte

const archiveSHA256 = "f38462de0e2b7f08c66387d2ef9127a05afad1acd967c011f14d2c5e3a210504"
