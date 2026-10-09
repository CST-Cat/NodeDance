# Linux release bundles

`nodedance-release bundle` creates a deterministic-layout `tar.gz` containing
the Core binary, Agent binary, and a signed manifest. The Ed25519 signature
covers the Linux architecture, version, and exact size and SHA-256 of both
binaries. The manifest deliberately does not carry a trusted public key.

Create a signing key once and keep the private key outside source control and
build logs. The public key should be distributed to operators separately and
must not be trusted merely because it appears beside a bundle:

```sh
umask 077
nodedance-release keygen --private-file ./release-private.key --public-file ./release-public.key
```

Cross-compile the two program binaries, then sign one bundle per architecture:

```sh
nodedance-release bundle \
  --core ./build/linux-amd64/nodedance \
  --agent ./build/linux-amd64/nodedance-agent \
  --private-key-file ./release-private.key \
  --version v1.0.0 --architecture amd64 \
  --output ./dist/nodedance-v1.0.0-linux-amd64.tar.gz
```

To produce both supported architectures in one step, use the locked Linux
release builder. It embeds the matching public key in the Agent for S16 signed
updates, creates each outer signed bundle, and verifies both before output:

```sh
scripts/release/build-linux-bundles.sh \
  v1.0.0 ./release-private.key ./release-public.key ./dist
```

Obtain the publisher's base64 Ed25519 public key through an independently
trusted channel. Never take a key from the bundle or accept a key supplied by
the same unverified download. Install from the source checkout containing the
reviewed `scripts/release` tools:

```sh
sudo scripts/release/install.sh \
  --bundle ./nodedance-v1.0.0-linux-amd64.tar.gz \
  --public-key-file /etc/nodedance/release-public.key \
  --prefix /opt/nodedance
```

The installer verifies the signature and component hashes before writing any
files, accepts only the current Linux architecture, and refuses any existing
prefix. By default it requires root and systemd, creates or validates the
dedicated non-root `nodedance` system account, then installs and starts
`nodedance.service`. Core listens only on `127.0.0.1:8180`; proxy or Tailscale
exposure must be configured separately. Core state lives in
`/var/lib/nodedance`, outside the release prefix. The Agent binary is installed
but no Agent unit is fabricated or started before enrollment.

For a manually managed or non-systemd setup, pass `--no-service`; that mode
installs only the verified binaries and manifest.

After enrollment, install the Agent unit with the existing CLI. Create and use
a separate non-root account first; the Core service account is not shared:

```sh
sudo useradd --system --user-group --create-home \
  --home-dir /var/lib/nodedance-agent --shell /usr/sbin/nologin nodedance-agent
sudo -u nodedance-agent /opt/nodedance/nodedance-agent enroll \
  --server https://panel.example.ts.net --token-stdin \
  --config /var/lib/nodedance-agent/.config/nodedance-agent/agent.json
sudo /opt/nodedance/nodedance-agent install-systemd --user nodedance-agent \
  --config /var/lib/nodedance-agent/.config/nodedance-agent/agent.json --enable
```

Enrollment configuration must be owned by the chosen Agent service account and
remain mode 0600. Do not run the Core installer as a substitute for Agent
enrollment.

Uninstall requires the same independently trusted key:

```sh
sudo scripts/release/uninstall.sh \
  --public-key-file /etc/nodedance/release-public.key \
  --prefix /opt/nodedance
```

It stops and disables `nodedance.service`, removes the unit only if its content
still matches NodeDance's generated unit, reloads systemd, then verifies and
removes only `nodedance`, `nodedance-agent`, and `release-manifest.json`. Any
added data or unrecognized files remain in the prefix. It never removes
`/var/lib/nodedance` or the service account. If a managed binary or unit has
changed, uninstall refuses to remove that item. Installation and removal
require Python 3 and OpenSSL with Ed25519 support; neither Go nor Node.js is
needed on the target host.
