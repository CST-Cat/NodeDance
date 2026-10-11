#!/bin/sh
set -eu
case "${1:-}" in
  version|--version|help|--help|-h) exec /usr/local/bin/nodedance-agent "$@" ;;
  install-updater|run|enroll|recover) ;;
  *) echo 'Docker Agent supports run, enroll, recover, and version.' >&2; exit 1 ;;
esac
if [ "$(id -u)" != 0 ]; then
  echo 'NodeDance Agent requires root.' >&2
  exit 1
fi
if [ ! -d /host/proc/1 ] || [ "$(readlink /proc/1/ns/pid)" != "$(readlink /host/proc/1/ns/pid)" ]; then
  echo 'NodeDance Agent requires Linux rootful Docker, privileged mode, host PID networking, and / mounted at /host.' >&2
  exit 1
fi
# Keep the static executable open across the switch into the host root/mounts.
# All runtime services then see the real host paths, shells, Docker CLI and CA store.
if [ "${1:-}" = install-updater ]; then
 shift
 set -- install "$@"
 exec 3</usr/local/bin/nodedance-updater
else
 exec 3</usr/local/bin/nodedance-agent
fi
exec nsenter --target 1 --mount --uts --ipc --net --pid --cgroup --root --wd --no-fork -- /proc/self/fd/3 "$@"
