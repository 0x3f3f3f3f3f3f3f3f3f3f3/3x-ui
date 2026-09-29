# Release archive staging

The `update-stage` command validates a Linux release archive into a new private
directory. Its optional download mode selects this fork's release and tag commit;
`--preflight` executes the verified candidate's managed-core check. The helper
does not stop services, replace an installation or migrate a database. `update.sh`
now runs these checks before dependencies, service stop or program replacement.
First installation, menu/web-updater integration and transactional program/DB
rollback remain required work; preflight alone does not make activation atomic.

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
files added to an installed directory are not release members. The installer
and web updater still need integration that respects this distinction, binds
scripts/helpers to the selected release, preserves runtime state and restores
the old program/database on failed activation. The ABI values are declarations;
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
prove a packaged image works. First installation, menu/web-updater integration
and safe activation with program/database rollback remain unfinished work.

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
fallback. Metadata is bounded to 1 MiB, the sidecar to 4096 bytes, and the whole
download to five minutes. Transfer failure, cancellation, validation failure and
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
