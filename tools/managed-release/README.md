# Managed release assembly

`prepare-linux.sh` assembles a disposable Linux build directory. It compiles the
pinned, patched Xray source and a static `update-stage`, copies this checkout's
installer/updater/menu and service units, includes the exact prepared Xray
source and license, and creates the complete `release.json` inventory. A panel
binary must already exist in the directory. Existing required output members
are rejected. Optional sidecars and routing assets can be added before assembly.

Use Go 1.27+, Git, a POSIX shell, GNU tar and gzip. Build the panel with CGo and
the frontend assets as usual, and stamp the full source commit independently of
the optional dev-version display stamp. For example, from a committed native
Linux arm64 checkout with the frontend built:

```sh
commit=$(git rev-parse HEAD)
bundle_parent=$(mktemp -d)
mkdir "$bundle_parent/x-ui"
go build -buildvcs=true \
  -ldflags "-X github.com/mhsanaei/3x-ui/v3/internal/config.buildSourceCommit=$commit" \
  -o "$bundle_parent/x-ui/x-ui" .
sh tools/managed-release/prepare-linux.sh "$bundle_parent/x-ui" "$commit" local linux-arm64
"$bundle_parent/x-ui/x-ui" verify-release --directory "$bundle_parent/x-ui" \
  --commit "$commit" --tag local --platform linux-arm64
```

All files must be finalized before creating the manifest. The command does not
install files or control host services. Source declarations are not independent
provenance attestation; build from the selected commit and retain its source.
An ordinary dirty VCS build is rejected by the runtime preflight.

The source export uses sorted GNU tar entries with fixed timestamps, numeric
owners and normalized permissions, then `gzip -n`. It contains the actual
prepared source tree used for the core build, including its `go.mod`, `go.sum`,
license and modified source files. From the extracted `source` directory, the
core build command is:

```sh
CGO_ENABLED=0 go build -trimpath -buildvcs=false \
  -ldflags '-X github.com/xtls/xray-core/core.build=3x-ui-managed-1' -o xray ./main
```

## Distribution entrypoints

- The Linux release workflow builds the managed core, verifies the complete
  bundle with the candidate panel, then writes a GNU tar archive and SHA256.
  It also exports the platform's static bootstrap helper, its SHA256 and the
  release manifest. ARMv5/v6/v7 use `xray-linux-arm32` and `mtg-linux-arm`.
  The amd64 job additionally exports the portable `install.sh` and its mandatory
  checksum once for the menu's verified first-install bootstrap.
- Cross-platform Linux preflight uses the existing Docker QEMU action to
  register emulators. See its [upstream documentation](https://github.com/docker/setup-qemu-action).
  These CI gates have not been executed locally on the foreign architectures.
- The Windows workflow builds the managed core and runs its authenticated UDP
  bridge test on the Windows runner. The upstream archive supplies support
  files; its `xray.exe` is discarded. The managed executable, modified source
  archive and license are packaged. This workflow has not been run here.
- Docker builds use Node 26 and require `SOURCE_COMMIT`; `RELEASE_TAG` defaults
  to `local`. Compose reads `XUI_SOURCE_COMMIT` and optional `XUI_RELEASE_TAG`.
  `DockerInit.sh` selects the exact ARM variant and uses the shared assembler.
  The candidate preflight runs in the builder before producing the runtime
  image. The image workflow targets the current repository's GHCR namespace;
  manual runs build without pushing. No image has been built or published here.

Optional geodata and MTProto downloads retain their prior dynamic selection.
Thus byte reproducibility of the complete image or every release dependency is
not established. The core and its exported source are checked separately below.
No Snell binary is redistributed by this build path.

## Native artifact probe

```sh
GOFLAGS=-p=1 python3 tools/managed-release/probe_linux.py --panel /path/to/native/x-ui
```

The supplied panel must report an unmodified full source declaration. The probe
uses a private temporary directory and builds the real core and staging helper.
It rebuilds the core/source export to compare their bytes, archives the bundle,
passes it through mandatory SHA256 and manifest verification, and executes the
staged panel's real managed-core handshake. It checks source/license inclusion,
probe-file cleanup and an unchanged previous-install sentinel. Subprocess groups
are bounded and killed on timeout; no installer or service manager is executed.
This requires Python 3.11+ and permission for owned loopback listeners.

Current Linux arm64 fixture evidence: 12 inventoried files, a 71,910,795-byte
archive, byte-identical repeated core/source builds and successful authenticated
preflight after staging. The fixture panel used a declared `f` repeated 40 times
commit, so this observation is not a claim that the artifact came from an actual
commit with that identity. See [validation](../../docs/managed-services/validation.md).

The standalone installer and regular menu/web updates now use verified release
staging. Transactional activation and program/database rollback remain
unfinished. A passing assembly probe does not establish installation, upgrade,
container runtime or full protocol/policy acceptance.

The actual updater's validation order has a separate isolated negative-acceptance
probe, `probe_update.py`. It requires a built static `update-stage`, an unmodified
native panel with full source metadata, managed and stock cores, Python, static
BusyBox and Linux namespace/mount privileges. See
[validation.md](../../docs/managed-services/validation.md#fork-download-and-actual-updater-preflight-2026-09-29)
for its invocation and scope. It exercises the whole `update.sh` in owned
chroots, including real candidate preflight and service-stop refusal; it does
not yet validate successful activation or program/database rollback.

`probe_update.py --menu` exercises the actual stable/dev menu commands and a
modified installed script. `--web` starts an actual panel with an owned SQLite
database in the private chroot, uses an ephemeral administrator token, checks
HTTP update/status run IDs and verifies early rejection for changed scripts and
containers. A deliberately dirty native build can be checked with
`--web --dirty-source-rejection`. These modes keep validation failures away from
host services and check that the existing panel/data survive; they do not yet
exercise successful activation or schema rollback.

`probe_update.py --install` runs nine installer rejection cases against an
existing installation; add `--fresh-install` for nine cases without an installed
panel. `--install --fresh-install --install-success` instead performs first
SQLite installation, checks installed file hashes and private credential-file
permissions, then starts the real installed panel and checks authenticated HTTP.
Package and service managers are substitutes inside the owned chroot; no real
systemd/OpenRC deployment, ACME issuance or upgrade rollback is claimed.

`--menu-maintenance` checks menu refresh, selected-release input validation and
the standalone installer bootstrap, including missing/wrong/malformed checksums,
an oversized script and first installation followed by authenticated panel HTTP.
Use `--case NAME` to rerun a single case from the selected mode. Refresh must make
no network request or service restart; a shell expression in the release tag must
be rejected before a fixture command marker can be created.

`--migration-failure` seeds a real SQLite schema and a malformed inbound in the
owned chroot, runs the updater through candidate preflight and file replacement,
and requires a failed migration with no service start/restart. Combine with
`--install` (and optionally `--fresh-install`) to check the corresponding installer
path. Service/package managers remain fixture substitutes, and the copied program
files are not rolled back. This mode requires a clean panel built with migration
error propagation; it does not prove an upgrade recovery transaction.
