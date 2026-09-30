# Build, upgrade and recovery

Status: no deployment/release performed; installation changes pending. Existing installer/Docker currently fetch official Xray plus sidecars, so they do **not** yet deploy the requested final architecture.

Target reproducible workflow: clone this fork's feature branch; install pinned Go/Node toolchains; build `core/xray` from managed source and panel against its local module replacement; build frontend into the existing embedded dist; record panel/core revisions, compatibility version and SHA-256 for artifacts. CI must use these same steps and source. No developer cache edits or separately hosted custom-core repo.

Upgrade must validate custom capability v1 and new config before applying it, persist policy/accounting state before replacing binaries, retain the prior known-good binary/config, and restore those if validation/startup fails. User policy changes use hot control updates and should not restart all listeners. Official-core update paths must not silently replace a required custom core.

Backup must include SQLite/PostgreSQL data, policy/identity mappings, ledger/cursors, node allocations, separate business SSH host keys and recoverable core budget state. Keep all credentials protected. Restore requires an epoch/lease fence so a restored snapshot cannot issue quota already granted to a live or disconnected node.

Rollback after schema/ledger migration uses a coordinated pre-upgrade backup and matching panel/core binaries; never promise safe in-place DB downgrade. Revoke/fence outstanding node allocations before restoring. Exact tested commands will be added when packaging and recovery are implemented.


## Development initialization (not a completed installer)

### Clean-source Tunnel checkpoint build

Commit `c635910d40efb320cd361ad138dfb9b24ad3981c` was rebuilt on Linux arm64
from an independent local clone without hardlinks or copied `node_modules`.
Go 1.27.1, Node 26.10.0 and npm 11.19.1 used the existing module/npm download
caches. This verifies clean-source builds, rather than an empty-cache/offline
installation, Docker package or deployment. No service was started.

From that commit with these toolchains installed:

```sh
cd frontend
npm ci
npm run build
cd ..
task_revision="$(git rev-parse HEAD)"
GOTOOLCHAIN=go1.27.1 GOFLAGS=-p=1 go build -trimpath -buildvcs=false \
  -ldflags="-s -w -X github.com/mhsanaei/3x-ui/v3/internal/config.buildCommit=$task_revision" \
  -o build/x-ui .
GOFLAGS=-p=1 bash tools/build-custom-core.sh
build/x-ui -v
build/custom-xray version
sha256sum build/x-ui build/custom-xray
```

The panel reports `dev+c635910d`; the core reports
`Custom Xray-core 26.9.9-custom.1` and the full source SHA without `-dirty`.
For this exact build, panel SHA-256 is
`a519914dccf95320593cdbd09671bc458a38dadb9bdd24c8135691b80f0ecef6`,
and core SHA-256 is
`63e875aef9ee2e31b2a83a9538125dc61d969d225880c4fd789bf8eadccefebb`.
Later source commits have different version stamps/checksums. Installer,
Docker, platform-matrix and recovery requirements remain open.

### Clean-source password identity checkpoint build

Commit `e9adcccf8865fbd8f05b7788ea3c6f96bdb0333d` was rebuilt using the same
clean-clone procedure and toolchains above. `npm ci`, the frontend build,
panel/core builds and both version smoke commands passed; the clone remained
clean. The panel reports `dev+e9adcccf` and the core reports the full revision
without `-dirty`. These artifacts verify the native password-proxy increment;
the panel compiler still refuses managed Mixed/HTTP activation until canonical
account bindings and migration are implemented.

Distinct local checkpoint files preserve the earlier Tunnel artifact provenance:

- `build/x-ui-password-checkpoint`: SHA-256
  `d258c582002af664b931e2acc60011293be086abca3bb06df63378dd92980b43`.
- `build/custom-xray-password-checkpoint`: SHA-256
  `61aec53ad615b98584101ef7fd0e15feb6271dce24d7ece9fe33c4164da4fbd1`.

Evidence: `/root/task-evidence/password-clean-build-results.json` and
`password-clean-build-manifest.json`. No installation, service startup or
production deployment was performed.

### Clean-source password owner checkpoint build

Commit `6550613112d073f246cb4aea44d75d8a2ee8f7cb` was rebuilt from a new local
clone with shared verified caches and the same pinned toolchains. `npm ci`
(16.07 s), frontend build (4.34 s), panel build (21.36 s), core build (3.54 s)
and both version smoke commands passed. The clone remained clean. The panel
reports `dev+65506131`; the Custom Xray version reports the full revision
without `-dirty`.

Distinct artifacts preserve both preceding checkpoint builds:

- `build/x-ui-password-owner-checkpoint`: SHA-256
  `cae85b0e70e39fb26560ac0d3a3f87126e042d26c389a7dd528701443c8530f1`.
- `build/custom-xray-password-owner-checkpoint`: SHA-256
  `84604a2f5e55a41eadab0ac956f37c2ea06ebc2886bac3c509e5b8e8f7ee1565`.

Evidence: `/root/task-evidence/password-owner-clean-build-results.json` and
`password-owner-clean-build-manifest.json`. This verifies the authoritative
password-owner database foundation and its build. Canonical runtime binding,
owner selection UI and legacy username-counter handoff remain unfinished;
owned accounts cannot enter an unmanaged configuration. No installation,
service startup or production deployment was performed.

### Clean-source password runtime configuration checkpoint build

Commit `44df54288ebb77f09074a8ff53e845397852b73d` was rebuilt from a new
clean local clone using the pinned toolchains and verified shared caches.
`npm ci` (15.87 s), frontend (4.15 s), panel (15.74 s), core (6.41 s)
and both version smoke commands passed. Source status remained clean. The
panel reports `dev+44df5428`; the Custom Xray version includes the full
revision without `-dirty`.

Distinct artifacts preserve all preceding checkpoints:

- `build/x-ui-password-config-checkpoint`: SHA-256
  `2e0761c9a9bf0743a202f0b0cf308c811d207fed261f046b7870edb6063fa9c5`.
- `build/custom-xray-password-config-checkpoint`: SHA-256
  `5f50576fe8281d397ac356c4cd53cf7de04c36bb1e1be41b2df54271c27f2124`.

Evidence: `/root/task-evidence/password-config-clean-build-results.json` and
`password-config-clean-build-manifest.json`. Canonical managed password
configuration is verified; owner UI, grouped credential hot changes, generic
lifecycle and legacy username-counter handoff remain open. No installation,
service startup or production deployment was performed.

Build with `bash tools/build-custom-core.sh`. For a **new** panel-assigned instance, initialize a private persistent path once:

```sh
build/custom-xray policy-init -file /private/path/policy.db -instance panel-assigned-instance-id
```

Set `clientPolicy.stateFile` to that path and `clientPolicy.instanceId` to the same ID, alongside `policies`. Existing, missing, corrupt, mismatched or locked state must not be deleted/reinitialized to bypass an error. Lost state requires authoritative ledger reconciliation; that workflow is not implemented yet. Keep the state on durable local storage; installer/Docker volume setup and backup fencing remain open.


### Clean-source grouped password hot-change checkpoint build

Commit `6f9705206d81ea772ba082c8f6607d45c8aef098` was rebuilt from a new
clean local clone using pinned toolchains and shared verified caches.
`npm ci` (16.11 s), frontend (4.20 s), panel (25.26 s), core (2.46 s)
and both version smoke commands passed; source status remained clean.
The panel reports `dev+6f970520`; Custom Xray reports the full revision
without `-dirty`.

Distinct artifacts preserve all preceding checkpoints:

- `build/x-ui-password-hot-checkpoint`: SHA-256
  `5aa8c419bdab720f9a5a371e682a87f533c1dab73cb513f221cd8c78285107f4`.
- `build/custom-xray-password-hot-checkpoint`: SHA-256
  `5252e28091f3f58f3f9d97b10aa11ce5adf193d5c009a7ebd488aa6fbe77793d`.

Evidence: `/root/task-evidence/password-hot-clean-build-results.json` and
`password-hot-clean-build-manifest.json`. Scoped managed credential hot changes
are verified. Owner UI, generic lifecycle and legacy username-counter handoff
remain open. No installation or production deployment was performed.

## Interrupted live handoff

A live legacy-to-managed transition records its old child boot in the panel SQL
source before draining. Back up this source together with `legacy_traffic_receipts`,
client traffic, identities and the private policy state; do not omit the source's
handoff fields when moving between SQLite and PostgreSQL.

After a transient SQL failure, keep the original panel process alive and retry
activation: it retains the frozen final snapshot and receipt ID. Once final SQL
has committed, a panel restart can finish managed activation using that committed
usage. If the original panel and snapshot are lost before final SQL commits, the
new panel refuses to start business listeners. Preserve the database, core state
and available logs for authoritative reconciliation. Do not clear the handoff
marker or create a fresh policy store to get past the error: doing so can restore
already-spent quota. Automated reconciliation of an irretrievably lost legacy
snapshot is not implemented. A rollback still requires the coordinated
pre-upgrade backup and matching binaries described above.
