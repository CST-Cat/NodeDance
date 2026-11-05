# Bundled rsync / 内置 rsync

NodeDance Agent embeds rsync **3.4.1** for Linux amd64 and arm64. Only the executable matching the Go target architecture is embedded. The single Agent binary, its Docker image, installer and built-in updater all carry that executable; nodes do not install or download rsync, and runtime never checks system PATH for it. Core forwards the independent WSS data streams and does not need an rsync executable.

Agent 内置对应架构的 rsync 3.4.1。原生二进制、Docker 镜像、安装器与内置更新器共用这一产物，不增加独立安装或更新通道；节点无需安装系统 rsync，也没有系统版本优先或兜底分支。

[The manifest](../internal/transfer/rsyncbundle/manifest.json) pins the upstream source URL and SHA-256, both architecture archives, executable digests, ELF machines and COPYING. `scripts/build-rsync.sh --architecture amd64` or `--architecture arm64` downloads only that source, verifies its digest, compiles with `-Os` and section garbage collection, links statically and strips symbols. OpenSSL, xxhash, zstd, lz4, ACLs, xattrs and iconv are disabled; included zlib and popt are used. The source is not forked. Archive timestamps and owners are fixed. The compiler/libc may change output bytes, so each verified build records its resulting SHA-256 and regenerates the architecture's embedding constants.

构建脚本只接受固定源码与校验值，按目标架构分别编译、裁剪、静态链接并移除符号。归档的时间与属主固定；不同工具链可能产生不同二进制，因此构建后按实际产物更新校验值。两种架构的已验证归档保存在仓库，`make build` 会离线校验，普通本地构建无需重新下载源码或安装 C 工具链。

Agent Dev and Agent release CI use native amd64/arm64 runners. Each job rebuilds its own rsync before compiling Agent, checks the archive/ELF/digests, executes the packaged Agent's `--rsync-version` as root with `PATH=/nonexistent`, and runs native root bundle and rsync transfer tests. Release-image verification executes this same diagnostic inside the exact tested Agent image. Source Docker builds also compile for the target architecture. These workflow checks do not create a release tag; publication remains gated by the existing authorized tag and rehearsal rules.

CI 在 amd64、arm64 原生 runner 上各自构建对应 rsync，并执行 Agent 内置版本诊断和真实 rsync 收发测试。Docker 发布镜像封装已经测试的同一 Agent 二进制；源码镜像按目标架构构建 rsync。配置工作流不等于已完成远端 CI 验证，报告以实际运行结果为准。

Runtime requires the root Agent and its protected `0700` state directory. Each attempt verifies both embedded archive and executable SHA-256, confirms the static ELF architecture, and creates a temporary `0700` executable beneath a private `0700` subdirectory; upstream [COPYING](../internal/transfer/rsyncbundle/licenses/COPYING) is extracted with `0600` permissions. Root ownership, one-link regular files and protected ancestors are checked. The attempt removes its executable workspace after its processes stop. Retained transfer payloads have a separate lifecycle and remain available for retry.

运行时校验包与二进制的 SHA-256、静态 ELF 架构及 root 受保护路径，生成 `0700` 的私有可执行文件与 `0600` 的许可证。参数模板探针及接收进程只执行这一文件；Landlock 限制仍然生效。程序目录在本次进程退出后清理，失败传输的 partial 继续保留。缺少 Landlock 或 rsync 数据通道不可用时，中继模式使用既有分片中继；严格直连模式明确失败，不转中继，也不改用系统 rsync。

```sh
sh scripts/build-rsync.sh --architecture amd64
sh scripts/build-rsync.sh --architecture arm64
python3 scripts/vendor-rsync.py --check
make build
sudo .build/nodedance-agent --rsync-version
go test ./internal/transfer/rsyncbundle ./internal/agent/rsync
```

Cross builds require the corresponding GCC/binutils and static libc; `RSYNC_CC` and `RSYNC_STRIP` can select those build tools. The runtime executable remains the embedded bundle. [Transfer security and recovery](file-transfers.md) applies unchanged.
