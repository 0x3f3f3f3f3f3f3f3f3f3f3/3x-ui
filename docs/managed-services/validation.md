# Verification record

No new protocol or whole-system policy feature has passed acceptance yet.
This record distinguishes source review, arithmetic tests, integration and
real-client data-plane evidence. Skips are not passes.

## Environment

- Linux arm64, kernel 6.17.0-1018-oracle, UTC, isolated development checkout.
- Official Go 1.27.1 linux/arm64 SHA-256:
  `3450b45a3f9ee8568792736a5c5e70a1f2e9b36c35a8f74958c03e51d7d92bec`.
- Official Node v26.10.0 linux/arm64 SHA-256:
  `7a6353f63eb3d04765004b4adf172616243e4522434635cb1d26288658b04ab5`.
- `nft`, `tc`, `ip` are present; availability is not a privileged capability test.
- Docker and Go were absent initially. Go and Node installed under
  `/root/toolchains`, without changing host services.
- Surge Mac/iOS client and license are not present. Snell real interoperability
  remains unexecuted even after any future server/config test.

## Commands and observed evidence

| Command / check | Result |
|---|---|
| `git status --short --branch` on initial clone | clean main, no user changes |
| GitHub fork API / release API / `git ls-remote` | provenance and SHAs in audit.md |
| `git rev-list --count v3.8.5..HEAD` at baseline | 64 |
| Safe local credential classification | SSH private key, mode 0600; content never printed |
| SSH strict host check (first attempt) | failed: no known ED25519 host key |
| SSH with keys pinned from official HTTPS `/meta` | authenticated as fork owner; exit 1 is GitHub's expected no-shell response |
| `go mod download` | exit 0 |
| `make test-go` | restricted socket run failed; authorized rerun exit 0, 46 packages passed |
| `npm ci` | exit 0, 621 packages installed, npm reported 0 vulnerabilities |

Network access from the restricted shell failed DNS resolution initially;
authorized network tool runs succeeded. No host-key-check bypass was used.
No kernel networking changes, deployment, public release or default-branch
merge were performed.

## Acceptance not yet executed

All real-protocol bandwidth, quota, routing, authentication, recovery and
multi-node tests from requirements sections A–D remain open. Full static,
race, frontend, PostgreSQL and packaging checks also remain open until their
actual results are recorded. The implementation must establish pre-test burst,
sampling, cutoff and overshoot bounds; none is claimed for unbuilt adapters.

## Exact arithmetic milestone (not runtime billing integration)

- `go test ./internal/clientpolicy` first failed on missing arithmetic symbols.
- `go test -race -count=1 ./internal/clientpolicy`: passed, 1.244s.
- `go test -run '^$' -fuzz FuzzChargeMatchesArbitraryPrecision -fuzztime=10s ./internal/clientpolicy`:
  passed, 258,440 executions; checked against independent arbitrary-precision arithmetic.
- `go vet ./internal/clientpolicy`: executed before the arithmetic commit.
- Coverage includes exact multipliers 0.5/1/1.5/2/10, fractional carry across
  1,000,001 single-byte events, different batch sizes, segmented 10 GiB at 1×
  then 5 GiB at 2× = 20 GiB, quota allowance and checked int64 limits.
- No database persistence, data path, UI or backend feature is claimed from
  these arithmetic tests. Identity/migration and runtime integration follow.
- Frontend baseline `npm run typecheck`: exit 0. Full `npm test` still running.
- Git hook initially used host Node18 and failed on util.styleText; rerunning
  the commit with the repository-required task-local Node26 succeeded.
- First push of 4e2ff8c6 was rejected by automatic approval review because it
  did not recognize authorization for external data transfer/remote branch
  creation. No push occurred. Explicit user approval requested; local work
  continues. No authentication secret was copied into the repository.

## Push verification

The user subsequently explicitly approved this task's feature-branch pushes.
`git push -u origin feat/unified-client-policy-backends` succeeded, and
`git ls-remote origin refs/heads/feat/unified-client-policy-backends` returned
`3e226aeaca84392dd3b534b1c341baa955cdbd0f`, exactly matching local HEAD.
This includes audit commit `4e2ff8c6` and arithmetic commit `3e226aea`.

Identity milestone `f2d23a46ab6daf09bcf25bf356e57273740daad1` was subsequently
pushed and independently matched by `git ls-remote` on the same branch.

## Immutable local client identity milestone

Implemented: create-only, internal UUID `ClientRecord.PolicyID`; existing rows
are backfilled before index creation. Public JSON cannot choose that identity.
Renaming/editing credentials preserves it; deletion and recreation generates
a new identity. Raw counters/quota/manual disable are untouched by migration.
This is local identity infrastructure, not yet node identity synchronization,
persistent billed usage or authenticated backend enforcement.

- `go test ./internal/database -run TestClientPolicyIdentity -count=1`:
  RED before implementation (missing PolicyID), then PASS.
- A 1,003-client nullable legacy fixture crosses the 500-row migration batch
  boundary, retains existing UUIDs, and survives DumpSQLite/RestoreSQLite with
  every identity preserved. Focused SQLite run passed in 10.919s.
- An isolated PostgreSQL 16.15 instance was unpacked into `/tmp/3x-ui-pg-tools`
  and run as `nobody`, listening only on 127.0.0.1:55432 with a private temp
  socket/data directory. No system PostgreSQL service was installed or changed.
- `XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' go test ./internal/database -run TestClientPolicyIdentityMigration_Postgres -count=1 -v`:
  PASS, 4.370s; 1,003 legacy rows, UUID uniqueness, disable preservation,
  reopen, duplicate rejection and actual SQLite→PostgreSQL migration.
- `npm run gen`: PASS; no generated API diff because PolicyID is internal.
- `npm run build`: PASS, Vite built the production bundles in 6.39s.
- Full frontend baseline initially exited 1: Chromium could not load libatk.
  Official Playwright runtime dependencies were then installed and the suite
  rerun. That run has component test timeouts; final details are still pending.

Additional checks for this milestone:

- Temporarily removing the migration call produced a behavioral failure:
  legacy policy identity was not a UUID. Restoring the call returned GREEN
  (1.461s); this proves the test detects a missing migration, not just a type.
- `go test -race -count=1 ./internal/database ./internal/database/model`:
  PASS (159.473s and 1.648s); PostgreSQL-gated tests are not counted from this run.
- `go test -shuffle=on -count=1 ./internal/database ./internal/database/model ./internal/web/service ./internal/web/controller ./internal/sub`:
  PASS (47.443s, 0.256s, 128.656s, 13.025s, 35.013s).
- `go build ./...`: exit 0 with real Vite bundles embedded.
- `git diff --check`: clean.

- `go vet ./internal/clientpolicy ./internal/database/...`: exit 0.
- `golangci-lint run ./internal/clientpolicy/... ./internal/database/...`:
  exit 0, 0 issues; formatter diff is empty with the Go toolchain on PATH.
- Full frontend baseline: 169 test files passed, 5 failed; 1,735 tests passed,
  7 timed out at the unchanged 5-second limit. An unhandled React scheduler
  `window is not defined` occurred during happ-settings-presets cleanup.
- The failed files were happ-routing-editor (3), client-bulk-calendar-renewal,
  client-calendar-renewal, client-qr-modal-qr-capacity, calendar-expire-setting.
  Isolated rerun of all five files passed (23 tests), without code or timeout
  changes. The original full-suite failure remains recorded; cleanup needs
  verification during the next complete frontend run.

The complete request's unresolved requirements in plan.md and matrix.md have
not been removed or reclassified as complete.

## Internal durable ledger milestone (runtime integration pending)

The new database operations write account totals, fractional carry, meter
cursor and raw client_traffics projection in one transaction. They use the
immutable identity and reject untracked writes to the legacy projection.
Activation preserves existing local raw usage at 1×; global node history,
existing collector replacement, public controls and data-plane cutoff remain
unimplemented. These tests are database evidence, not protocol acceptance.

- Initial ledger tests failed on missing ledger types/operations; the first
  implementation passed the SQLite suite in 5.423s.
- A behavioral regression test then demonstrated that a recreated label
  inherited a residual traffic row. Binding the projection to PolicyID fixed
  it; the extended suite passed in 4.933s.
- SQLite tests cover replay/out-of-order/conflicts, counter-regression rejection,
  connection reopen, eight concurrent sources, multiplier 0.5/1/1.5/2/10 with
  1/13/1001-byte reporting batches, retained fractional carry across changes,
  10 GiB at 1× + 5 GiB at 2× = 20 GiB, and integer-overflow rollback.
- An actual SQLite trigger aborts the cursor update after charging; the test
  verifies account/projection rollback and a subsequent retry. No mocked DB.
- DumpSQLite/RestoreSQLite preserves the cursor and half-byte remainder;
  replaying the restored report is free, and the next byte completes the carry.
- Reset requires final snapshots, closes the old meters, clears raw/billed/carry
  while retaining multiplier, quota and manual disable. Old reports stay retired.
- `XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' go test ./internal/database -run '^TestClientPolicyIdentityMigration_Postgres$|^TestClientUsageLedger_Postgres$' -count=1 -v`:
  PASS, 3.116s, against PostgreSQL 16.15. Includes concurrent sources, index
  enforcement, segment billing and actual SQLite→PostgreSQL migration of
  raw/billed values, revision, fractional remainder and acknowledged cursor.

- `go test -race -count=1 ./internal/database -run '^TestClientUsage|^TestClientPolicyIdentity'`:
  PASS, 26.595s (PostgreSQL-gated tests are excluded from this result).
- `make test-go`: exit 0, 47 packages passed; externally gated tests are not
  counted as data-plane acceptance.
- `golangci-lint run ./internal/database/... ./internal/clientpolicy/... ./internal/xray/...`:
  exit 0, 0 issues; formatter diff empty.
- `npm run gen`: exit 0, no generated API changes; `go build ./...`: exit 0.
- Removing the cursor persistence statement caused the replay test to charge
  the same 100 upload/50 download bytes twice. Restoring it returned GREEN
  (0.204s). The mutation was reverted before commit.
- Complete frontend baseline rerun is running with heavy Go work finished;
  existing test timeouts and source remain unchanged.

The subsequent full frontend run exited 1 after 298.99s: 173 files / 1,741 tests
passed; happ-routing-editor's `retains invalid JSON {"Name": until repaired`
timed out at 5s. Two React scheduler `window is not defined` errors were
attributed to happ-settings-presets cleanup. The two Happ files passed together
with `--project=components --maxWorkers=1` (27 tests, 24.87s); no source, assertions
or timeout limits changed. Default-concurrency full-suite instability remains.

`npm test -- --maxWorkers=1` subsequently passed the **entire** frontend suite:
174 files, 1,742 tests, 414.59s, zero unhandled errors. Assertions, test timeouts
and frontend source were unchanged. This supports resource contention as a
contributor; it does not erase the recorded failures at default concurrency.

## Shared stream shaping milestone (adapter integration pending)

Initial unit tests failed on missing limiter/writer operations, then passed.
Coverage includes invalid/zero rates, bounded grants/queue, cancellation,
live update wakeup, repeated-update credit preservation, four writers sharing
one cap, stream contents and partial writes. Billing does not affect rate.

`go test ./internal/clientpolicy -run '^TestLimiterTCPSharedClientsAndLiveChanges$' -count=1 -v`
passed in 6.017s with actual IPv4 loopback TCP. Both client groups use
127.0.0.1 and four connections, each group sharing one limiter. Target-side
socket reads supply independent counts. This validates the scheduler/writer,
not any claimed SSH/Snell/mieru/Xray protocol integration.

Predefined bounds: exclude 300ms startup; measure for 1.8s initially and 1.5s
after changes. Upper bytes = rate × elapsed × 1.06 + configured burst; lower
bytes = rate × elapsed × 0.80. The fixed 6% scheduling/delivery margin and burst
were set before execution. Existing streams are sampled 250ms after changes,
with the sample ending within the 2s update target. No tolerance was widened.

| Measurement | Raw target-side result |
|---|---:|
| Unlimited, 4 connections, 300ms | 2,603,859,815 B/s |
| Client A, 65,536 B/s, 1.801s | 65,516 B/s |
| Client B, 131,072 B/s, 1.801s | 131,031 B/s |
| A live decrease to 32,768 B/s, 1.500s | 32,749 B/s |
| B during A decrease | 126,661 B/s |
| A live increase to 131,072 B/s, 1.501s | 131,021 B/s |
| B during A increase | 135,377 B/s (inside burst envelope) |

- `go test -race ./internal/clientpolicy -count=1 -v`: PASS, 9.541s, including
  the real TCP test. Queue capacity/cancellation and concurrent writers pass.
- Static lint initially flagged a direct EOF comparison in the TCP harness;
  switching to `errors.Is` fixed it. Final package lint: exit 0, 0 issues.
- Formatter diff is empty. `make test-go`: exit 0, including the real TCP test.

## Atomic quota admission milestone (flow attachment pending)

`ClientUsageLedger.Admit` checks enabled state, expiry and billed quota within
the same transaction that commits raw/billed/remainder/cursor state. Both the
canonical client and traffic-row quota are honored; the stricter nonzero cap
wins while control-plane state is being reconciled. Already received usage is
still settled by Apply, even after disable, without granting forwarding rights.
Admit rejects stale or closed grants; exact last-request retries are idempotent
but must still pass current disable/expiry/lowered-quota checks.

The producer must forward only after a successful commit and serialize its
sequence. Actual adapters, existing-flow cancellation, first-use activation and
the separate disable/expiry/quota reason model remain to integrate. Pending
first-use expiry is explicitly rejected until activated by the control plane.

- New admission tests first failed on the missing entry point; initial green
  run passed in 0.269s. Multipliers 0.5/1/1.5/2/10 stop at the exact raw
  allowance, including a fractional charge that would cross the billed quota.
- Competing sources requesting 60 bytes each against a 100-byte quota admit
  one request; the other reports precisely 40 available bytes and may retry
  with 40. Total raw and billed usage both remain 100.
- Disable/expiry/lowered quota are checked on grant retries. Reset retires old
  grants. These are database admission tests, not TCP/UDP cutoff acceptance.
- A new test failed when 0.5× billing allowed raw upload+download to overflow
  int64 while billed usage still fit. Checked combined raw usage fixed it;
  unlimited and very large quotas still admit representable usage.
- `go test ./internal/database -run '^TestClientUsage' -count=1`: PASS, 8.319s.
- `XUI_TEST_PG_DSN=... go test ./internal/database -run '^TestClientUsage.*Postgres$' -count=1 -v`:
  PASS, 2.237s; real PostgreSQL ledger and competing quota admission.
- Package static lint: exit 0, 0 issues; formatter diff empty.
- `go test -race ./internal/database -run '^TestClientUsageAdmission' -count=1`:
  PASS, 1.769s; `make test-go`: exit 0 across the full Go suite.
- Disabling the admission quota gate caused the 0.5× boundary test to accept
  one byte beyond its exact allowance. Restoring the gate returned GREEN;
  the mutation was reverted before commit.

## Shared TCP flow controller milestone (protocol integration pending)

The new `internal/policyflow` controller joins shared directional shaping,
pre-forward durable admission and owned connection cancellation. Tests use
real TCP listeners and file-backed SQLite WAL/FULL, with independent target
reads. They do not yet authenticate SSH, Snell, mieru or existing Xray clients.

- Four concurrent uploads through two bindings exhaust a 16 MiB billed quota
  at 2×: exactly 8,388,608 raw bytes admitted and 16,777,216 billed. In the final
  race run the target received all 8,388,608 bytes. The predeclared target bound
  is at most the raw allowance, and at least that allowance minus 256 KiB
  (four flows × two 32 KiB grants). No allowance was increased after a failure.
- Exhaustion closes existing streams, rejects a subsequent Open and still
  rejects it after constructing a new controller with the same durable DB.
  This is controller restart coverage; full panel/backend process restart and
  machine/power-loss acceptance remain required.
- Manual disable closed an idle TCP flow in 471ms; another same-IP client
  continued echoing. Idle expiry and lowered quota closed in 471/478ms. Source
  replacement closed in 481ms. A held SQLite write transaction caused watchdog
  cutoff in 1.225s, inside the unchanged 1.25s test bound.
- A cancellation test occupies the real SQL connection while admission starts,
  cancels that stream and then releases the connection. Its committed 5 upload
  bytes survive; another stream writes 6 download bytes, total billed 11. The
  earlier implementation poisoned the shared cursor with context.Canceled.
  Reintroducing that defect produced the expected test failure; it was restored.
- Admission-only source takeover fences the old cursor and idle-source check.
  Apply cannot use that source to bypass admission. Legacy observed meters
  retain their counters and false AdmissionOnly flag after column migration,
  and cannot be discarded without settlement. SQLite and PostgreSQL pass.
- Real database tests reject SQLite NORMAL/OFF synchronous settings, missing
  recovery journals and FULL rollback journals, plus PostgreSQL asynchronous
  commit. Normal WAL/FULL and synchronous PostgreSQL admission remain usable.

The duplex rate test uses two clients at the same loopback address, each with
four live connections across two bindings. Client A bills at 2× while B bills
at 1×. Independent socket counts check both directions. Before execution its
bounds were fixed to the existing scheduler test: 300ms warmup, 1.8s initial
window, 250ms settling plus 1.5s live-update window, upper rate × time × 1.06
plus burst and lower rate × time × 0.80. Every live window ends within 2s.

| Final race run | A upload / download B/s | B upload / download B/s |
|---|---:|---:|
| Initial caps A=64/128, B=128/64 KiB/s | 65,513 / 131,024 | 131,024 / 65,513 |
| A changed to 32/64 KiB/s | 32,755 / 65,522 | 131,051 / 65,520 |
| A changed to 128/128 KiB/s | 122,307 / 129,943 | 128,850 / 65,511 |

The durable-admission unlimited baseline measured 3.09/3.02 MB/s under race
instrumentation, above the required eight times the largest cap. This is much
slower than the scheduler-only baseline because each bounded grant commits to
disk. Higher-rate and many-client capacity, including safe commit batching,
remain performance work before production rollout. Rate tests do not certify
arbitrary rates up to the arithmetic/API maximum.

Ignoring Configure updates made the actual live TCP test fail its first reduced
rate window. The mutation was reverted. Initial race runs also exposed policy
checks starving behind SQLite writes; successful admission now refreshes the
monotonic freshness clock, and checks share the client's serialization lock.
No rate, quota, cutoff tolerance or test timeout was loosened for these fixes.

Verification completed for this milestone:

- Focused `go test -race` for Controller, Admission and ClientUsageAdmission:
  PASS, including real TCP and stalled-database tests.
- Real PostgreSQL ledger/admission/source/durability/migration tests: PASS,
  2.542s, with XUI_TEST_PG_DSN set to the isolated instance.
- Static lint initially found an embedded selector simplification; fixed.
  Final `golangci-lint run ./internal/policyflow/... ./internal/database/...`:
  exit 0, zero issues.
- `make test-go`: exit 0 across the full Go regression suite, including the
  new real TCP package; frontend source was unchanged in this milestone.

Production adapters, immutable auth bindings, rate persistence/API/UI,
first-use activation, separate restriction reasons, UDP, multi-node leases,
historical-backup reconciliation and the complete A–E protocol matrix remain
open. This controller milestone is not completion of Tasks 3–9.
