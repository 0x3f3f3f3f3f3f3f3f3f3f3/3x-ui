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

### Clean-source password owner form checkpoint build

Commit `67f5336a70216ffa169efb8978f93aa11cb0b995` was rebuilt from a new clean local clone
using the pinned toolchains and shared verified caches. `npm ci` (15.99 s),
frontend (3.67 s), panel (16.16 s), core (2.82 s) and both version smoke
commands passed; source status remained clean. The panel reports
`dev+67f5336a` and Custom Xray includes the full revision without
`-dirty`. No installation, service startup or production deployment occurred.

Distinct artifacts preserve all preceding checkpoints:

- `build/x-ui-password-owner-ui-checkpoint`: SHA-256
  `d9f12c7e9ed45f3b4125788e3750622bce14e1f22693192cd86ea961ac035070`.
- `build/custom-xray-password-owner-ui-checkpoint`: SHA-256
  `555f71d98c67e59f68b9ff41f5ff9a6202c8314a4daa0013773599c479d85a9f`.

Evidence: `/root/task-evidence/password-owner-ui-clean-build-results.json` and
`password-owner-ui-clean-build-manifest.json`. Owner form and scoped API/full
database preservation are verified. Generic owner lifecycle, live legacy
username-counter handoff and foreign-owner remapping remain open. Default
frontend concurrency produced one unchanged-test timeout; the complete suite
passed with one worker. testing.md records the exact split validation gate.


### Clean-source password owner removal checkpoint build

Commit `e1efc255bea94da9d9b68e699a068c9f4f110e15` was rebuilt from a clean local clone
with the pinned toolchains and shared verified caches. npm ci (15.86 s),
frontend (4.30 s), panel (15.63 s), core (2.40 s) and both version commands
pass; source status is clean. Panel reports dev+e1efc255 and Custom Xray
reports the full revision without -dirty. The authorized fork feature branch
was pushed and its exact remote SHA verified. No deployment occurred.

Distinct artifacts retain all earlier checkpoints:

- `build/x-ui-password-owner-removal-checkpoint`: SHA-256
  `78bab4989d191c2eeb6876e345571fdc9076b4fec2ead742559697d7a2b18b6f`.
- `build/custom-xray-password-owner-removal-checkpoint`: SHA-256
  `f0d013b2ce138dbe234b0cc31633d304c8f13785423212d831fa51ee6e212651`.

Evidence: /root/task-evidence/password-owner-removal-clean-build-results.json
and clean-build-manifest.json. Scoped single/bulk owner detach/delete and
recovery are verified. Shared Update, bulk/by-email field lifecycle and live
legacy alias-counter handoff remain unfinished. Whole Task 5B and the original
goal remain incomplete; prior testing.md frontend timeout records still apply.


### Clean-source single password-owner Update checkpoint build

Commit `3d3590f99d897c1b920b21e79c86ab96d7e16f17` was rebuilt from a new clean
local clone `/tmp/password-owner-update-clean-source-y1gxt5a4` using pinned
Go 1.27.1, Node 26.10.0/npm 11.19.1 and shared verified caches. npm ci
(18.17 s), frontend (4.33 s), panel (16.14 s), core (2.62 s) and version
commands pass. Source status stays clean. Panel reports `dev+3d3590f9`;
Custom Xray reports the full revision without `-dirty`. The authorized fork
feature branch was pushed and its exact remote SHA verified. No deployment,
release, merge or service startup occurred.

Distinct artifacts preserve the earlier owner form/removal checkpoints;
their recorded hashes were checked again:

- `build/x-ui-password-owner-update-checkpoint`: SHA-256
  `2f6d99df72de565a2d5f86eedb5050a97c8ce0f6a4f250223b42589cfbc78e0d`.
- `build/custom-xray-password-owner-update-checkpoint`: SHA-256
  `a72a242f68000e5672008f12ab25c161cb08dbf3fd6ea94d56e6e6df7ff244fc`.

Evidence: /root/task-evidence/password-owner-update-clean-build-results.json,
clean-build-manifest.json and push.json. Single canonical Update and its scoped
runtime recovery are verified. Bulk/by-email lifecycle, live legacy alias-counter
handoff, remaining adapters and whole-system single-core migration remain open.
Prior default frontend timeout records and split suite acceptance remain in
testing.md; this clean build does not claim a fresh frontend test suite.


### Clean-source password-owner fields checkpoint build

Commit `5ec2d9dba46ddfee0b76c6166129539dc18ccf5d` was rebuilt from clean local
clone `/tmp/password-owner-fields-clean-source-ani62r_i` with pinned Go 1.27.1,
Node 26.10.0/npm 11.19.1 and shared verified caches. npm ci (15.89 s), frontend
(4.73 s), panel (18.49 s), core (3.95 s) and both version commands pass.
Source status remains clean. Panel reports `dev+5ec2d9db`; Custom Xray includes
the full revision without `-dirty`. Fork push and exact remote SHA match pass.
No deployment, release, merge or business service startup occurred.

Distinct artifacts retain earlier checkpoints; all six preceding owner form,
removal and Update binaries were rehashed unchanged:

- `build/x-ui-password-owner-fields-checkpoint`: SHA-256
  `50f9417bf15c44fc57a48b63568244e6061ba93a43e614b85cc97b35db73c99f`.
- `build/custom-xray-password-owner-fields-checkpoint`: SHA-256
  `7104e6e7f9339859da289a1806a391496d11123c6715b45624b58733fe10ff57`.

Evidence: /root/task-evidence/password-owner-fields-clean-build-results.json,
clean-build-manifest.json, prior-artifact-preservation.json and push.json.
Scoped shared-field and bulk-enable lifecycle is verified. Live legacy alias
handoff, generic creation, anonymous/foreign ownership, remaining adapters and
whole-system single-core migration remain incomplete. Existing default frontend
timeout/split test records remain in testing.md; this clean build does not claim
a new full frontend or native-core suite run.


### Clean-source unmatched native traffic retention checkpoint

Commit `0fa4416e2f1c1ea1379bdc8a1078852c775691cd` was rebuilt from clean local
clone `/tmp/legacy-unassigned-traffic-clean-source-az2dw40s` with pinned
Go 1.27.1, Node 26.10.0/npm 11.19.1 and shared verified caches. npm ci
(16.22 s), frontend (4.12 s), panel (29.47 s), core (3.15 s) and both
version commands pass. Source status remains clean. Panel reports
`dev+0fa4416e`; Custom Xray includes the full revision without `-dirty`.
Fork push and exact remote SHA match pass. No deployment, release, merge
or business service startup occurred.

Distinct artifacts preserve every earlier checkpoint. All eight preceding
owner form/removal/Update/fields binaries were rehashed unchanged before
and after this build:

- `build/x-ui-legacy-unassigned-traffic-checkpoint`: SHA-256
  `e942ec816204b33bcb3cd124387f5fed0abb989a4e4119f997b3ca9f5132ca81`.
- `build/custom-xray-legacy-unassigned-traffic-checkpoint`: SHA-256
  `d5c00e1ef5aa786fd6bdeeb35771a46767c74c674bf4269175e7947be4a78510`.

Evidence: /root/task-evidence/legacy-unassigned-traffic-clean-build-results.json,
clean-build-manifest.json, post-build-prior-preservation.json and per-revision
push receipt. Exact unmatched label/counter conservation, source-labelled
retention, original receipt binding and scoped SQLite/PostgreSQL recovery are
verified. Historical configuration proof, ownership mapping, atomic bucket
consumption and password live legacy handoff remain incomplete. Whole Task5B
and the original single-core/protocol/install goal remain open. The prior
default frontend timeout/split suite records remain in testing.md; this build
does not claim a new full frontend or native-core test suite.


### Clean-source native configuration provenance checkpoint

Commit `da5d86f575133959e4ab9326a56042232a18e3c1` was rebuilt from clean
clone `/tmp/legacy-traffic-config-proof-clean-source-gcv6vh8r` with Go 1.27.1,
Node 26.10.0/npm 11.19.1 and shared verified caches. npm ci (18.80 s),
frontend build (4.09 s), panel (18.70 s), core (10.20 s) and both version
commands pass. Source status is clean; panel reports `dev+da5d86f5`, and core
includes the full revision without `-dirty`. Fork push matches the exact SHA.
No deployment, release, merge or business service startup occurred.

Distinct artifacts preserve all ten preceding checkpoint binaries, rehashed
unchanged after this build:

- `build/x-ui-legacy-traffic-config-proof-checkpoint`: SHA-256
  `da1867bb63142ea925fc660f8ebb141569fadcd558bb3601229b2975dfab0e6c`.
- `build/custom-xray-legacy-traffic-config-proof-checkpoint`: SHA-256
  `e64447158cc9cfbd9163a334f132de36187fcb38c6c6300c1c926184635d5e84`.

Evidence: /root/task-evidence/legacy-traffic-config-proof-clean-build-results.json,
clean-build-manifest.json, post-build-prior-preservation.json and per-revision
push receipt. Startup proof and conservative saved-configuration drift retention
are verified; historical API mutation proof, owner mapping and live legacy
handoff remain open. Further password migration work is deferred: the execution
priority is native Snell, mieru and SSH. This build does not claim a new full
frontend or native protocol test suite.

### Clean-source native mieru core checkpoint

Commit `6c475f5003e94b7307395a2fd6b30862ce163bd2` was rebuilt from clean
clone `/tmp/native-mieru-clean-source-z4w0_h3e` with Go 1.27.1,
Node 26.10.0/npm 11.19.1 and shared verified caches. npm ci (22.43 s),
frontend build (6.88 s), panel (73.50 s), core (5.13 s), both version commands
and core module build-info checks pass. Panel reports `dev+6c475f50`; core
reports Custom Xray-core 26.9.9-custom.1 with the full revision and no `-dirty`.
The compiled official mieru dependency is v3.38.0. Source status is clean and
the authorized fork feature branch matches the exact implementation SHA.

Distinct local artifacts preserve all 21 preceding checkpoint binaries and
the separate review binary, with all 22 rehashed unchanged after this build:

- `build/x-ui-native-mieru-core-checkpoint`: SHA-256
  `6e973687326352a284e3f60b1e33ed0be0e92be501325d72e2d5a6e0ac139d4f`.
- `build/custom-xray-native-mieru-core-checkpoint`: SHA-256
  `9f77bf94df2e21054d0c4f37a8d4d895af92c63ce5be04e6591c1c47c9d9b2e2`.

Evidence: /root/task-evidence/native-mieru-clean-build-results.json,
native-mieru-clean-build-manifest.json, native-mieru-post-build-prior-preservation.json
and native-mieru-push-6c475f5003e94b7307395a2fd6b30862ce163bd2.json.
This validates the compiled native mieru core increment and a compatible panel
build. Mieru panel/API/DB/forms/export/runtime integration remains open; Snell
and SSH remain the other active core priorities. No deployment, release or
default-branch merge occurred. The build adds no new full frontend test claim.

### Clean-source native SSH core checkpoint

Source `c282cc6b7b7c62f644cb88014d9c35a5c38ff129` was built from clean clone
`/tmp/native-ssh-clean-source-5qeovxsn` using Go1.27.1, Node26.10.0/npm11.19.1.
Clean npm install, frontend, panel, core and version/build-info checks exited0.
Panel version is `dev+c282cc6b`; core26.9.9-custom.1 reports the full source SHA
without a dirty suffix. Previously verified compiled mieru v3.38.0 is retained.

- `build/x-ui-native-ssh-core-checkpoint`: SHA256
  `47df25ecac2cdd41f137ddd9bd06396fa9169fa549d55fb1fc89fcc493488b23`.
- `build/custom-xray-native-ssh-core-checkpoint`: SHA256
  `f7d796e47c10e6f75378033aeeaac4549e4e417d55595c0ab0ddd55f09294557`.

All24 preceding checkpoint/review artifacts covered by the preservation manifest
rehash unchanged. Evidence: `/root/task-evidence/native-ssh-clean-build-manifest.json`,
`native-ssh-clean-build-results.json`, `native-ssh-post-build-prior-preservation.json`
and exact fork receipt `native-ssh-push-c282cc6b7b7c62f644cb88014d9c35a5c38ff129.json`.
This closes the native core checkpoint provenance, not SSH panel/forms/export or
full Task7. Snell native QUIC and three-protocol panel integration remain open.


### Clean-source native Snell panel checkpoint

Source `a94e102b16677885e20f84a6c1bcecedef414b2e` builds the local Snell/mieru/SSH
panels and core from one clean clone. Distinct checksums,34 unchanged prior
binaries and exact-artifact native checks are recorded in
[native-snell-panel-testing.md](native-snell-panel-testing.md). Source publication
is verified on the fork feature branch. Custom-core installation, upgrade and
Docker packaging remain pending; no deployment or release was performed.
