# NodeDance repository rules

- Treat the current product behavior as the only implementation. Do not add compatibility branches, adapters, migrations, or tests solely for retired modes, APIs, or behavior. Remove deprecated duplicate code and documentation when it is in scope.
- Agent enrollment, recovery, runtime, file service, and systemd service are root-only. Do not generate or support a non-root restricted Agent or file-service mode.
- Preserve current security requirements: Agent credentials must remain owner-protected in a `0700` state directory and `0600` regular config file; file management must reject traversal, symlinks, Agent credential paths, and protected kernel/runtime trees.
- Tailscale is an optional network capability. NodeDance may detect it and expose the existing Agent installation entry when available. Agent installation remains root-only. NodeDance does not install or configure Tailscale or change its configuration.
- Work directly on `main`; do not create branches.
