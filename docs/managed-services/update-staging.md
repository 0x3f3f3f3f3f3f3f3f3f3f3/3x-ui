# Release archive staging

The `update-stage` command validates a downloaded Linux release archive into a
new private directory. It does not stop services, replace an installation,
execute archive members or migrate a database. Installer/updater integration,
fork provenance, managed-core compatibility and transactional rollback remain
required work. The current `update.sh` has not yet been changed to use it.

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
