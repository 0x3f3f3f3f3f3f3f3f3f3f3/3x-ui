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
