//go:build linux && arm64

package rsyncbundle

import _ "embed"

//go:embed assets/rsync-linux-arm64.tar.gz
var archive []byte

const archiveSHA256 = "43b371fdb10750b715b6292b6234ed8689ef44d83cbd04186caa6766d3bedfc7"
const binarySHA256 = "e127392541c7adb0ced95947417758c6164855d502bd56ff483d4c0699930dfc"
const archiveMachine = 183
