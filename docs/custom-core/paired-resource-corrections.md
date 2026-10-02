# Paired resource and journal corrections

This records the bounded distribution-package portion of the authorized
whole-stage correction batch, based on clean source
`a980ef95012e34de12511e2fd807f5e6363f4392`. The original reviewer fixtures and
logs under `/tmp/paired-stage-review.YHV5Y3HH` were read and retained unchanged.
Fresh evidence is retained under
`/tmp/task-evidence/paired-resource-corrections.loCpC8`.

Installed verification (`VerifyFiles`, and the offline pair checks in `Verify`)
now permits changes to declared geodata while requiring a regular file, safe
path, positive size and the existing 512 MiB per-file bound. All manifest role,
target, source, compatibility, capability and toolchain checks remain intact.
Panel, core, control, service, source/license and legacy-helper files still
require their original sizes and SHA-256 hashes. Incoming verification
(`VerifyIncoming`) checks every original distributed hash, then rejects
undeclared files and runs the bounded offline pair probes. `Promote` uses that
strict incoming verifier before preserving resources or renaming an installed
tree. CLI wiring and runtime activation checks belong to the separate
integration portion of this correction batch.

An update uses new distributed geodata when the current bytes still match the
old manifest. Current geodata that differs from its old manifest is copied into
the candidate instead; geodata from an installation without a manifest is
treated as customized. The new distributed manifest is never rewritten.
Transaction journals record `packageSHA256` over its exact manifest bytes and
`resourceOverrides` entries containing path, role, size and SHA-256 for mutable
overrides, including undeclared geodata retained from the current installation.
These hashes can be checked independently against retained bytes with ordinary
SHA-256 and byte-count tools. They are staging snapshots, not signatures or a
requirement that later geodata rotations retain the same hash. Rollback adds
separate `rollbackSourceRevision`, `rollbackPackageSHA256` and
`rollbackResourceOverrides` snapshots while keeping the original update receipt.
Legacy trees have no original package manifest receipt to verify.

Rollback classifies immutable distribution files using both the current and
previous manifests. A previous tree without a manifest uses the existing
distribution path whitelist. A resource omitted from the newer manifest, such
as `licenses/old-resource.txt`, therefore cannot collide with the old code
copied into the rollback candidate. Current geodata replaces the candidate's
old geodata. Database, ledger, credential and unknown application-state files
come exclusively from the stopped current tree. The regressions preserve
`spent=900`, leave the retained previous ledger at `spent=5`, keep the failed
tree intact and prove that a credential deleted from the current tree is not
resurrected. This is code rollback; it does not downgrade a database schema.

Journal writes now use unique same-directory temporary files rather than one
fixed `.tmp` path. Existing fixed-name and unique orphan files remain evidence;
they do not obstruct subsequent journal writes. The file is synced before the
atomic journal rename, and rename/removal helpers sync affected parent
directories. Promotion and rollback use these helpers; recovery callers use
the same helper API in their separately owned integration files. Journal writes
also reject a receipt larger than the recovery decoder's 1 MiB limit before it
can become visible.

Verification results on Go `go1.27.1`, Linux arm64:

- `red-resource-journal.log` contains the expected pre-fix geodata, preservation,
  rollback and orphan-journal failures. After fixing installed verification,
  `red-rollback-classification.log` isolates the precise
  `licenses/old-resource.txt` rollback collision.
- `red-resource-receipts.log` and `red-journal-receipt-bound.log` show the missing
  receipt and oversized recovery-journal failures before their fixes.
- `red-final-tests-original-baseline.log` reruns the final regressions against a
  separately retained copy of the original clean distribution package. Its seven
  failing top-level tests are expected; no shared source was reverted.
- `green-resource-journal-final.log` records a passing complete
  `go test ./internal/distribution -count=1` run. `strict-boundaries.log` separately
  passes same-size incoming geodata checksum rejection and rejection by promotion
  before the current tree or ledger changes.
- `race-package-final-snapshot.log` records a passing
  `go test -race ./... -count=1` run over the saved distribution package snapshot
  (2.157 seconds). `vet-package-final-snapshot.log` records `go vet ./...` exit 0
  on the same snapshot. `snapshot-final-sha256.txt` identifies its exact files.
  This small standard-library module contains the full distribution package;
  it is not a whole-repository or rebuilt-release-artifact check.

Subprocess fixtures in these regressions are scripts reporting explicit compiled
capability JSON; they prove the verifier and transaction boundaries, not native
protocol acceptance of newly built release executables. The caller must stop
and settle the service before copying current resources. Directory syncing is
checked through successful filesystem operations, not an actual power-cut test.
Receipts may become historical after later mutable resource updates. No result
here completes broader coordinated-node fencing, backup/restore allocation
fencing, legacy data-plane migration, publication or production deployment.
