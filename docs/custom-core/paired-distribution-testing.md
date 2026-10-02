# Paired distribution acceptance progress

The distribution stage remains open. The clean Task 1 package uses source
`d2b30425e4c013b0d34b5e7f50a4b8fbea74f188`, Go 1.27.1 and Node 26.10.0.
Native Snell, mieru, SSH and shared policy remain the central acceptance gates.
These results do not claim installer/Docker/release integration, coordinated
node policy, restore allocation fencing, commercial device coverage or legacy
sidecar migration is complete.

## Source-matched package

The common builder compiles both executables from clean managed source, verifies
locked Go modules, rebuilds the frontend, normalizes the core runtime filename,
records all resource hashes and includes project source, complete original
license resources, immutable dependency origins and the exact compiled Go
module source archives. Local replacements marked `(devel)` are managed source,
not downloadable versions. Package provenance is separate from stable/dev
version display. A source context without Git requires an explicit full revision.

The offline core report reuses the authenticated control API's compiled feature
list. It never starts an instance or advertises configured-store durability.
The panel dispatches `package info`/`verify` before loading service environment
files. Verification requires source/target/API agreement, pinned toolchains,
exact files and native3/billing/rate/Tunnel features; subprocess time and output
are bounded. Duplicate or unknown JSON fields, trailing JSON, unsafe paths and
links fail closed.

| Evidence | Result |
| --- | --- |
| `paired-distribution-task1-root-regression.log` | Entire root Go suite passed at the initial Task 1 implementation; optional native fixtures are proved separately below |
| `paired-distribution-core-cli-sockets-regression.log` | Compiled feature/RPC authorization, main-command and core race regression passed |
| `paired-distribution-manifest-probe-toolchains-green.log` | Manifest, mutation, path/link, strict JSON, bounded subprocess and report mismatch tests passed |
| `paired-distribution-dependency-local-replacement-green.log` | Observed compiled `(devel)` replacement bug reproduced then fixed; race test passed |
| `paired-distribution-task1-retry-clean-build.json` | Fresh clone, locked module verification, npm install/build, both executables, source/manifest generation and real package self-verification passed; checkout remained clean |
| `paired-distribution-task1-retry-package-proof.json` | Three real successful CLI probes; seven changed/incomplete/incompatible candidates rejected; configured state sentinels and original package hashes unchanged; 120 compiled dependency source archives independently rehashed |
| `paired-distribution-task1-native-http-receipt.json` | Exact packaged static core: all 11 required native3/bulk/Tunnel cases passed on SQLite and PostgreSQL; zero skips/failures |

HTTP acceptance crosses actual controllers, SQL, generated native core
configuration and TCP/UDP/QUIC/OpenSSH traffic. Its existing harness sets the
fixture authenticated user context; it does not test the login middleware.
State sentinel preservation during offline probing is distinct from the real
protocol lifecycle, identity and accounting checks in that HTTP acceptance.

All evidence names refer to retained files under `/root/task-evidence`. The
successful intermediate package is
`/root/3x-ui/build/paired-distribution-task1-retry-review`; it is not a published
release or deployment. The earlier failed package and log remain retained:
`paired-distribution-task1-review` and `paired-distribution-task1-clean-build.log`.
The default sandbox socket rejection was retained separately; the core race
suite passed after permitting its temporary local TCP/UDP sockets.

## Staging work in progress

A static, short-lived `x-ui-package` installation CLI shares the panel's verifier
and supplies bounded Go tar/gzip extraction without Go/Python on the destination.
It is not a data-plane service. Extraction creates a new private tree, rejects
traversal, unnormalized/duplicate paths, links, special files, privileged modes,
oversize/truncated archives and trailing nonzero content. Failed candidates
remain staged for diagnosis and never replace an existing tree.

`paired-distribution-stage-command-green.log` records the extraction and CLI
failure tests. `paired-distribution-real-stage-proof.json` records the actual
Task 1 package archive extracted and verified by a separate dirty-source review
helper, followed by independent rehashing of every declared staged file. That
helper is retained as `build/x-ui-package-stage-review`; final clean distribution
and installer replacement/rollback proof are still required.

The 41 existing top-level executable checksum receipts were verified unchanged
in `paired-distribution-prior-artifact-preservation.json`. New intermediate
packages and the staging helper use distinct paths. No private management/Git
key was read or packaged, and no default branch, release or deployment changed.

## Resource preservation and promotion foundation

The replacement foundation is implemented but not yet wired into the shell
installer/updater or service lifecycle. `PreserveResources` uses rooted file
operations, retains unknown app resources and symlinks without following them
outside the application, and keeps both members of the incoming pair. The
manifest now binds each resource role to its allowed path; an installation
cannot replace a database or host key by labeling it as a license.

`Promote` verifies first, retains the previous tree, writes a synchronized recovery
journal and restores the previous path if the candidate rename fails. Its caller
must stop the old service and finish settlement before copying state. Linux
kernel locking rejects simultaneous replacements and releases ownership after
process exit; lock files are retained to avoid unlink/recreate races. This does
not by itself implement post-start rollback or interrupted-run recovery.

`paired-distribution-promotion-lock-green.log` and
`paired-distribution-promotion-vet.log` prove the path/rejection, resource
preservation, role binding and concurrent ownership checks. The actual Task 1
archive was promoted into a temporary prefix by a distinct dirty review helper
in `paired-distribution-real-promotion-proof.json`; old-tree hashes, a real
SQLite **test** table/record, business-key sentinel and custom-resource hashes
were preserved, and the journal was present. That test table is not the full
3x-ui schema, and no service was started. Real protocol/SQL acceptance remains
the separate 11-case SQLite/PostgreSQL record above. The promotion review helper
was built before the subsequent source-level lock addition; final clean helper,
installer, interruption and rollback acceptance still remain.

## Installer lifecycle integration checkpoint

The ordinary install/update functions now use the shared paired-package library.
They validate an incoming candidate before dependency/service operations, execute
its hashed installer under a lifecycle kernel lock, stop the previous service,
recover any interrupted directory replacement, promote, install packaged controls
and activate. Remote archives and standalone verifiers each require their own
checksum. Local package directories/archives have an explicit offline route.
No official/upstream-core download remains in these active paths. The panel web
updater uses the fork, with source-stamped builds pinning the bootstrap script to
their own full commit; CLI menus use their installed paired scripts.

Incoming validation rejects unhashed scripts and runtime state; installed-tree
validation continues to allow preserved business files. A pending sibling journal
fences replacement until recovery/completion. Code rollback creates a candidate
from retained code and CURRENT business state, preserves the failed tree and the
original backup, and does not undo database schema migrations. It does not claim
coordinated-node or arbitrary backup-restore allocation fencing.

`paired-distribution-recovery-red.log`, `paired-distribution-rollback-red.log` and
`paired-distribution-lifecycle-inheritance-red.log` retain the missing behavior.
The corresponding green race logs prove interrupted first rename recovery, path/
journal rejection, use of latest usage instead of stale backup state, no
resurrection of deleted credentials, and kernel lock inheritance by an installer
child after its parent descriptor closes. Whole directory replacement remains
local to the installation parent and retains every transaction receipt.

The normal entrypoint preflight initially invoked apt/curl despite a bad local
pair; `paired-distribution-normal-preflight-{red,green}.log` preserves that change.
Both entrypoints now refuse the bad pair without dependency/service calls.
`paired-distribution-installer-recovery-route-green.log` uses the actual earlier
clean package through both offline entrypoints with the newer standalone helper:
installed state is preserved and no external service/package command executes.
The helper `build/x-ui-package-recovery-review` is an unstamped dirty engineering
artifact, not a final package. Earlier helper binaries remain unchanged.

Distribution/panel tests passed with local HTTP sockets enabled in
`paired-distribution-updater-recovery-sockets-green.log`; the preceding sandbox
run is retained separately and failed because httptest could not bind a socket.
The fixture does not represent a product bug. Incoming/lifecycle race and vet
logs are retained. Full temporary-prefix service activation/rollback and final
clean-source protocol/SQL/Docker acceptance remain the next gates. Independent
Docker/CI review evidence is recorded in `paired-docker-testing.md` and
`paired-ci-testing.md`, including their dirty-snapshot and platform limits.

The complete root Go suite passed in `paired-distribution-integration-root-regression.log`. Native optional fixture gates are covered separately by the required actual-core protocol/SQL receipts; this root-suite result alone does not assert zero skips.

## Whole-stage correction batch in progress

One whole-stage review of `83f9c08e..a980ef95` found eight Important issues and
no Critical issue. Its retained reproductions are under
`/tmp/paired-stage-review.YHV5Y3HH`. The single correction batch is documented in
`paired-resource-corrections.md`, `paired-activation-corrections.md` and
`paired-frontend-license-corrections.md`: mutable installed geodata with strict
incoming hashes; current-state rollback resource classification; retryable
synchronized journals; BusyBox control copying; actual managed-core readiness;
recovery before completion; local offline dependency handling; and complete
selected frontend source/notices. Earlier packages and passing receipts are
historical evidence, not proof of the corrected integrated artifact. Final
clean build and actual lifecycle/native protocol/container acceptance remain
open until their new receipts are recorded.
