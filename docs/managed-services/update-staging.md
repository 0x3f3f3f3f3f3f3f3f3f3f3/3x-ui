# Release archive staging

The `update-stage` command validates a Linux release archive into a new private
directory. Its optional download mode selects this fork's release and tag commit;
`--preflight` executes the verified candidate's managed-core check. The helper
does not stop services, replace an installation or migrate a database. `update.sh`
and `install.sh` run these checks before dependencies, service stop or program
replacement. The regular menu and web updates use the verified installed updater.
Menu installation, refresh and release selection also verify their script sources.
Independent core updates and transactional program/DB rollback remain unfinished;
preflight alone does not make activation atomic.

Build and run:

```sh
CGO_ENABLED=0 go build -trimpath -o update-stage ./tools/update-stage
./update-stage --archive /path/to/release.tar.gz \
  --sha256 EXPECTED_LOWERCASE_SHA256 --parent /path/to/existing/directory
```

A successful command prints the absolute path of its new `.x-ui-stage-*`
directory. The caller owns its cleanup and can inspect its `x-ui` child before
starting a separate installation transaction. Failure prints an error, exits
nonzero and removes its own staging directory. A failure to remove that directory
is included in the error. Existing sibling files and directories are preserved.

The helper builds without CGo or a runtime Python dependency. Its command
currently requires Linux; other platforms build and report the capability
restriction. Linux input must be a regular file; nonblocking open prevents a
FIFO input from hanging, and the final input component cannot be a symlink.
The source file and destination parent should be administrator-controlled.

## Accepted archive format

- One gzip member containing a complete GNU/USTAR tar archive. Its compressed
  bytes must match the supplied SHA256; concatenated gzip members and trailing
  compressed data are rejected. A checksum must come from a trusted distribution
  channel: a locally recomputed digest by itself does not authenticate a release.
- All logical members must be regular files or directories under `x-ui`.
  A nonempty executable `x-ui/x-ui` is required. This is a structural check,
  not evidence that the program is a compatible panel or can start successfully.
- Paths are at most 240 bytes and use ASCII letters, digits, periods, underscores,
  hyphens and directory separators. Empty, `.` and `..` components, absolute
  paths and trailing separators on regular files are rejected.
- Links, devices, FIFOs, sparse files, PAX metadata, privileged mode bits,
  duplicate entries and file/directory collisions are rejected. File owners,
  groups and timestamps from the archive are not applied.
- Files are created with requested mode 0644 or 0755 according to their executable
  bits; child directories use 0755 and the private staging root uses 0700.
  The process umask may further restrict these modes.

The limits are 512 MiB compressed input, 2 GiB complete decompressed input,
512 MiB per file, 1536 MiB total file payload and 4096 logical members. These
include bounded processing of archive padding; only zero padding is accepted
after the two tar end blocks. Reading through the gzip trailer validates its CRC.
Every archive write uses an `os.Root` for the new directory, with exclusive file
creation. No archive-provided link is created.

The caller must supply an existing trusted parent. This is not a sandbox against
another process with the same privileges, hostile mount changes, or disk
exhaustion below the declared limits. Cancellation is checked between reads;
it cannot interrupt an arbitrary caller-provided `io.Reader` that is already
blocked. The CLI restricts that input to a regular file. Staging does not claim
crash-durable activation or atomic service/database rollback.

## Validation

```sh
go test -p 1 -race -shuffle=on -count=1 ./internal/updatebundle ./tools/update-stage
go test -p 1 -run '^$' -fuzz '^FuzzStageArchive$' -fuzztime=30s -fuzzminimizetime=1s -parallel=1 ./internal/updatebundle
golangci-lint run --timeout=5m ./internal/updatebundle/... ./tools/update-stage/...
```

Tests cover accepted GNU/USTAR files and bounded padding; checksum errors;
truncated or corrupt gzip/tar; unsafe names and member types; duplicates and
directory collisions; all size/count limits and their exact boundary; cancellation
and input failure after actual file extraction; CLI argument/input checks; and
cleanup when publishing the output path fails. Existing program/core/unit/database
fixture bytes and an outside sentinel must remain unchanged. Fuzzing feeds
arbitrary tar bytes with a valid gzip stream and matching SHA256 through the
real staging path, with reduced resource limits.

Before this component was added, the actual repository updater was separately
executed inside an owned chroot with private PID/network/mount/proc namespaces.
Package manager, network, service and process commands were local stubs. A bad
checksum preserved the old program and unit. Correctly checksummed corrupt gzip
and a valid archive missing the panel both stopped the fixture service and
removed the old program/unit before failing. The database fixture remained.
Those observations reproduce the integration defect; this staging component
alone does not establish that the updater defect is fixed.

## Source and file manifest

A separate build command records an assembled bundle's declared source identity
and exact file inventory:

```sh
CGO_ENABLED=0 go build -o release-manifest ./tools/release-manifest
./release-manifest --directory /path/to/x-ui --commit FULL_SOURCE_COMMIT \
  --tag RELEASE_TAG --platform linux-arm64
```

It exclusively creates `release.json`; an existing file is never overwritten.
The repository is fixed to this task's fork,
`0x3f3f3f3f3f3f3f3f3f3f3/3x-ui`. A full 40-character lowercase commit, bounded
tag and supported Linux platform are required. The manifest binds those fields,
format version 1, declared policy/routing ABI 1, and every file's size, SHA256
and executable flag. The build caller supplies the commit; generating the
manifest does not independently prove which source produced a binary.

A complete bundle must contain `x-ui`, `update-stage`, `update.sh`, `install.sh`,
`x-ui.sh`, `x-ui.rc`, all three existing systemd unit variants, and its Xray
binary. ARMv5/v6/v7 bundles use the panel's canonical `bin/xray-linux-arm32`
filename; other supported labels are `amd64`, `arm64`, `386` and `s390x`.
Optional assets are included in the same inventory. Source files must be ordinary
files/directories with accepted names and no links or privileged mode bits.
The manifest cannot list itself. Its JSON is bounded to 1 MiB and rejects
unknown, repeated, case-aliased, missing or null fields and trailing input.

To require this identity and full inventory when staging, supply all three
release flags together:

```sh
./update-stage --archive /path/to/release.tar.gz \
  --sha256 EXPECTED_ARCHIVE_SHA256 --parent /path/to/staging/parent \
  --release-commit FULL_SELECTED_COMMIT --release-tag SELECTED_TAG \
  --release-platform linux-arm64
```

These values must come from the selected trusted release, rather than simply
copying untrusted values from the downloaded archive. A missing manifest,
incompatible declaration, wrong identity, changed file or extra/missing file
fails before the command prints a staging directory; it removes that stage.
Running without the release flags still performs structural staging only.

The full-inventory check is for an assembled or freshly staged bundle. Runtime
files added to an installed directory are not release members. Preparing the
installed updater verifies its individual manifest entry while permitting
runtime files. Activation still needs to preserve runtime state and restore
the old program/database on failure. The ABI values are declarations;
the runtime check below verifies the candidate panel and managed core before
activation. No complete fork-safe update claim follows from the manifest alone.

## Candidate runtime preflight

The panel exposes two standalone commands before loading service configuration
or opening a business database:

```sh
./x-ui release-info
./x-ui verify-release --directory /path/to/stage/x-ui \
  --commit FULL_SELECTED_COMMIT --tag SELECTED_TAG --platform linux-arm64
```

`release-info` emits JSON containing the fixed repository, full source commit,
modified-source flag, native platform, panel version and policy/routing ABIs.
Builds may set `internal/config.buildSourceCommit` through the module-qualified
Go linker flag; otherwise the commit comes from Go's VCS build information.
An unknown source remains empty. An explicit stamp does not hide a recorded
`vcs.modified=true`. ARM platforms use the compiled GOARM value. These fields
are build declarations, not independent source provenance attestation.

`verify-release` requires Linux, an unmodified matching compiled identity, the
selected manifest and its complete file inventory. It must execute the panel
inside that staged bundle. It then starts the bundle's Xray on an ephemeral
IPv4 loopback port with a random managed-bridge credential, a blackhole outbound
and private temporary configuration/log paths. The existing bridge performs a
real nonce/HMAC handshake; no external destination is requested. A stock Xray
cannot pass this check merely by declaring the ABI in its manifest.

The probe uses the existing managed process lifecycle, stops its owned core and
removes its temporary directory on success, failure or handled cancellation.
The readiness/handshake context is limited to 10 seconds; the existing version
query and process-stop bounds also apply. The command handles SIGINT/SIGTERM.
The cancellation test's 2-second bound measures an already pending handshake,
not every possible startup or shutdown stage. It checks the observed child PID
has disappeared, its exact listening address can be rebound and probe files
are gone. An external SIGKILL cannot run filesystem cleanup.

Actual Linux arm64 checks cover a managed-core success, stock-core rejection,
invalid source/tag/platform/ABI, changed files and execution from outside the
bundle. Rejected metadata/inventory cases must not launch even a marker core.
Database sentinels and unrelated runtime directories remain unchanged. Removing
signal-context propagation through a build overlay makes the cancellation test
fail at its unchanged 2-second deadline; restoring it passes.

This command does not start the candidate web service, migrate or restore a
database, validate every user configuration, replace an installed directory or
prove a packaged image works. The installer integration below has separate
native-process evidence. Safe activation with program/database rollback remains
unfinished work.

## Fork download and updater preparation

```sh
./update-stage --download --preflight --parent /trusted/staging-parent \
  --release-platform linux-arm64
# Explicit opt-in to a prerelease:
./update-stage --download --preflight --parent /trusted/staging-parent \
  --release-platform linux-arm64 --release-tag dev-latest
```

The production downloader uses only
`0x3f3f3f3f3f3f3f3f3f3f3/3x-ui`. It resolves the release's Git tag reference
to a full 40-character commit, including bounded annotated-tag dereferencing;
release notes and `target_commitish` are not authoritative commit sources.
Default selection requires a published stable release. Explicit selection can
use a prerelease but must match its tag. Draft releases fail.

Both the archive and mandatory `.sha256` sidecar must have uploaded asset
metadata with bounded size and SHA256 digest. Downloads use the captured numeric
asset IDs, support GitHub asset redirects, and check exact length and digest.
The sidecar must name the selected archive and agree with its API digest. Full
manifest identity/inventory verification follows extraction. A moving rolling
tag whose asset identity differs fails; there is no upstream or unchecked
fallback. Metadata is bounded to 1 MiB and the sidecar to 4096 bytes. Network requests
share a five-minute context deadline; local file I/O uses the staging cancellation
semantics above. Transfer failure, cancellation, validation failure and
failed candidate preflight remove the owned download/stage directories.

`--preflight` requires complete release verification. It invokes that stage's
`x-ui verify-release` with the selected identity before printing the stage path.
The subprocess has a two-minute ceiling; cancellation first sends SIGTERM for
owned-core cleanup, with a ten-second force-kill fallback. These are process
bounds, not a promise to validate arbitrarily slow storage in that time.

The standalone `update.sh` fetches a static helper and mandatory checksum from
the selected fork channel over HTTPS. Bootstrap bodies are bounded even when a
server omits Content-Length. Only then does the helper select, download, verify
and preflight the actual candidate. Missing assets/checksums, incompatible
manifests, wrong sources and stock cores fail before package-manager actions or
service stop. The menu and service units installed by this script come from the
verified archive. It checks service-stop errors and does not issue global
`pkill` patterns. Its subsequent copy/start phase still lacks the required
transaction journal, consistent database backup and automatic rollback.

For an administrator-controlled offline archive, the same script accepts
`XUI_UPDATE_ARCHIVE`, `XUI_UPDATE_SHA256`, `XUI_UPDATE_COMMIT`, `XUI_UPDATE_TAG`,
`XUI_UPDATE_HELPER` and `XUI_UPDATE_HELPER_SHA256`. All are required together;
commit and hashes must come from a trusted release channel. This mode still
performs full inventory and actual runtime preflight. It is not an unchecked
recovery bypass and does not establish trust in a locally supplied hash.

## Installed updater and entry points

`x-ui prepare-update` copies the installed `update.sh` to a private executable
temporary file outside the installation directory and prints that path. It
runs before business database/service-environment loading, requires a known
unmodified compiled source, and checks the installed manifest's repository,
full commit and platform against the running panel. The script must be a regular
executable file with the exact recorded length and SHA256, bounded to 2 MiB.
A staging parent inside the installation, including a symlink alias, is refused.
The caller removes the copied script after use.

This is an individual installed-file check: generated runtime configurations,
certificate directories and other runtime files are permitted and untouched.
Fresh release staging still requires its complete exact inventory. Neither
check protects against a hostile administrator rewriting trusted local files.

The regular `x-ui update` and `x-ui update-dev` menu commands use this copied
script. Failures return nonzero and clean up the copy; an unsuccessful download
is no longer reported as a successful menu update. The authenticated web updater
uses the same library operation, retains its run-ID/status polling contract,
and refuses host-style updates in Docker/container installations. Containers
must be updated through their image/runtime workflow. Container and unverified
build messages have English/Chinese translations and use the existing English
fallback for other languages.

Stable and development version queries use this fork's tag resolver. Development
availability compares full source commits; short display hashes and release-body
markers do not determine equality. Metadata lookup does not download or execute
the candidate and is not evidence of its runtime compatibility.

The separate core updater and safe activation/rollback are still open. Do not
interpret source verification as evidence of complete distribution/recovery safety.

中文说明：常规菜单更新和网页更新已改用已安装发布包内、经过清单校验的
更新脚本。脚本会复制到安装目录之外；运行时配置等额外文件不会因此被拒绝或
修改。下载、完整文件校验和真实受管核心预检均在依赖安装与停服之前完成。
网页更新在容器内会明确拒绝，并提示通过镜像更新；来源未知或带源码修改标记的
构建也不能启动自动更新。独立核心更新和
失败后的程序及数据库事务回滚仍未完成，不能将上述检查视为完整升级回滚保证。

## Standalone installer integration

Run `bash /trusted/selected-checkout/install.sh [RELEASE_TAG]` as root on a native
Linux host. An omitted tag selects this fork's latest stable release; `dev` maps
to the explicit `dev-latest` prerelease. The selected release must contain the
managed helper, mandatory checksums and complete compatible bundle. There is no
upstream or missing-checksum fallback. Online bootstrap needs curl, trusted CA
certificates and the ordinary shell/file utilities before package installation;
missing prerequisites cause an error. The existing installation parent must
exist. The same explicit `XUI_UPDATE_*` offline inputs described above are also
accepted by the installer.

The standalone installer performs full staging and the real managed-core
preflight before package installation or service stop. Rejection cleans its own
temporary tree. A failed dependency installation or service stop aborts before
program replacement. Installation copies the preflighted inventory, preserving
runtime extras; it does not remove the old installation tree or kill processes
by a global name pattern. The control menu and distro-specific unit come from
that inventory. Containers are rejected with an image-update instruction.

Initial configuration keeps panel service operations deferred until the bundled
unit has been installed. Certificate renewal commands written by this installer
respect that temporary configuration guard; subsequent renewal uses the normal
service command. Actual ACME issuance/renewal has not been tested here. Normal
panel database initialization still creates/migrates the database. The legacy
inbound migration now accepts an empty set of traditional proxy inbounds,
including a fresh database or a database containing only SSH inbounds.

The Linux arm64 probe covers first SQLite installation, exact installed file
hashes, mode-0600 credential output and authenticated HTTP from the installed
panel. Package/service commands are isolated substitutes, so actual systemd,
OpenRC, external PostgreSQL installation, custom service paths and ACME remain
unverified. Consistent upgrade snapshots, health rollback and crash recovery are
still required. Do not interpret a successful first-install fixture as a safe
downgrade path; there is no automatic database downgrade/restore transaction yet.

中文说明：独立 `install.sh` 已使用 fork 的完整发布包校验和真实核心预检，
失败时不会先安装依赖、停止服务或删除旧目录。首次 SQLite 安装及安装后面板
HTTP 已在隔离环境验证；真实服务管理器、证书签发、PostgreSQL 安装和事务回滚
仍未验证或未完成。菜单安装入口的校验方式见下文。

## Menu installation, refresh and release selection

`x-ui prepare-menu` prepares a private verified copy of the installed `x-ui.sh`,
using the same compiled-source and individual manifest checks as `prepare-update`.
The menu's refresh action (also `x-ui update-menu`) sets the copied file's mode
before atomically replacing `/usr/bin/x-ui` on that directory's filesystem.
It restores the matching installed release, makes no download and does not restart
the panel. Missing or changed installed sources fail with the old menu intact;
temporary source/destination files are removed.

On an uninstalled native host, `x-ui install` downloads this fork release's
standalone `install.sh` asset and mandatory `install.sh.sha256`. The sidecar must
contain exactly one lowercase SHA256 and the expected filename. HTTPS redirects
remain HTTPS; the sidecar is limited to 4096 bytes and the script to 2 MiB even
without Content-Length, with a 120-second deadline per download. The installer
runs only after checksum verification. Failures return nonzero and do not request
service start. `XUI_UPDATE_TAG` selects a particular tag; absent it, bootstrap
uses the latest stable release. This bootstrap trusts the fork's HTTPS release
channel, as does the standalone staging-helper bootstrap; it is not an independent
signature or source-provenance attestation.

The Linux amd64 release job exports the portable installer and sidecar once,
alongside its architecture-specific assets. Other Linux hosts use that same shell
asset. The job change has been statically checked, but no release publication has
been performed. Releases lacking these required assets are rejected.

The existing `x-ui legacy` command now selects an exact managed-fork release tag
and runs the verified installed updater. The input is bounded and validated, and
is never evaluated as shell code. Upstream/old unmanaged releases cannot satisfy
the helper/manifest/core checks. An older compatible bundle still does not imply
safe database downgrade: consistent backup/restore and transactional rollback
remain unfinished. The menu explicitly states that automatic database downgrade
is unavailable.

中文说明：菜单刷新恢复已安装发布包对应的已校验脚本，采用原子替换且不重启
面板。菜单首次安装必须校验 fork 发布的安装脚本及 SHA256；下载失败不会再
尝试启动服务。旧版本入口改为选择受管 fork 的发布标签，禁止将输入作为 shell
代码执行。上述来源校验不提供数据库降级或失败回滚保证。
