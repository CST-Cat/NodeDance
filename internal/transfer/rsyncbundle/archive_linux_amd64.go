//go:build linux && amd64

package rsyncbundle

import _ "embed"

//go:embed assets/rsync-linux-amd64.tar.gz
var archive []byte

const archiveSHA256 = "5a71f15ebf3cfd4b03d950051a0b88477ef82e90bbf9e5ff60eac6381415a94a"
const binarySHA256 = "67f4ba0bea900a38f9f42cde46ce51982596469ad324c6645935b0c85e1f7fff"
const archiveMachine = 62
