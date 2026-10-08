#!/usr/bin/env bash
# Configure Docker CLI to use only NodeDance's local Compose plugin path.
# Source this file; it intentionally does not edit the user's ~/.docker.
set -euo pipefail
NODEDANCE_DOCKER_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export DOCKER_CONFIG="$NODEDANCE_DOCKER_ROOT/.artifacts/docker-config"
NODEDANCE_PLUGIN_DIR="$NODEDANCE_DOCKER_ROOT/.tools/docker/cli-plugins"
mkdir -p "$DOCKER_CONFIG" "$DOCKER_CONFIG/cli-plugins"
chmod 700 "$DOCKER_CONFIG" "$DOCKER_CONFIG/cli-plugins"
python3 - "$DOCKER_CONFIG/config.json" "$NODEDANCE_PLUGIN_DIR" <<'PY'
import json,os,sys,tempfile
path,plugin=sys.argv[1:]
value={"cliPluginsExtraDirs":[os.path.abspath(plugin)]}
data=json.dumps(value,indent=2)+"\n"
if not os.path.exists(path) or open(path).read()!=data:
    fd,tmp=tempfile.mkstemp(prefix="config.",dir=os.path.dirname(path),text=True)
    with os.fdopen(fd,"w") as stream: stream.write(data)
    os.chmod(tmp,0o600)
    os.replace(tmp,path)
PY
