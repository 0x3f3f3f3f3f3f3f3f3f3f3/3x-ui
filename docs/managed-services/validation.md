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

At the initial audit, real-protocol tests and full verification were unexecuted.
The milestones below record subsequent evidence; matrix.md identifies the
remaining incomplete capabilities. Passing one adapter's tests never validates
an unbuilt adapter, multi-node behavior or the complete deployment workflow.

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

## SSH forwarding server milestone (management/routing integration pending)

The internal SSH server was tested with **OpenSSH_9.6p1
Ubuntu-3ubuntu13.19**, OpenSSL 3.0.13, and the repository's pinned
`golang.org/x/crypto v0.57.0`. Each fixture generates separate Ed25519 host/user
keys and a strict known_hosts file in temporary directories. The tests do not
read the Git credential, modify host sshd, or create non-loopback listeners.
The reusable server is not yet exposed by the panel/Runtime.

Reproduce with OpenSSH client `ssh` available in PATH and loopback sockets:

```sh
go test ./internal/sshtunnel -count=1 -v
go test -race ./internal/sshtunnel ./internal/policyflow -count=1 -v
golangci-lint run ./internal/sshtunnel/... ./internal/policyflow/...
```

- Real -L/-D processes deliver four simultaneous payload channels through two
  authenticated SSH connections. Raw upload/download and 2× billing match.
  The required dial adapter receives immutable client ID, inbound tag and the
  original `localhost` destination. The tests use TCP dialers; they do **not**
  prove Xray rule evaluation or production route integration.
- Real -R delivers asymmetric payloads: client upload 1,408 bytes, download
  9,216 bytes, billed 21,248 at 2×. In the final race run, disabling the client
  terminated SSH and released the reverse listener in 366ms (bound 1.25s).
- Authenticated requests verify default -R denial, exact bind address/port
  permissions, 16-listener capacity, cancellation/rebinding and returned
  capacity. Another test initially reached a reverse target before checking
  depleted quota; ProxyToClient now checks first, and that reproduction passes.
- Session, agent, X11 and client-originated forwarded-tcpip requests are denied;
  target allowlists and the 64-channel limit are exercised over real SSH. The
  channel-cap test first failed when a 65th channel was accepted, then passed.
- OpenSSH rejects an unauthorized client key and an unexpected server host key.
  Adding a new key preserves old authorized sessions; removing the old key
  closes only those sessions, keeping the new key and another client alive.
  A signed handshake paused across key removal is rejected before target dial.
  Removing its post-handshake recheck made that real target reachable; restoring
  the check returned GREEN.

The quota test uses a 1 MiB billed allowance, four long TCP channels and two
real OpenSSH processes. Independent target reads match every admitted byte in
the final race run. Its bound was fixed beforehand: target bytes must not exceed
the raw allowance and may fall short by at most four 32 KiB grants. Removing the
quota gate at 2× admitted 557,056 rather than 524,288 bytes and failed the test;
the gate was restored before final verification.

| Multiplier | Raw admitted / target bytes | Billed bytes |
|---|---:|---:|
| 0.5× | 2,097,152 / 2,097,152 | 1,048,576 |
| 1× | 1,048,576 / 1,048,576 | 1,048,576 |
| 1.5× | 699,050 / 699,050 | 1,048,575 |
| 2× | 524,288 / 524,288 | 1,048,576 |

At 1.5× the remaining billed byte cannot pay for another whole raw byte. Every
case terminates the existing transfers and rejects a new OpenSSH transport
after replacing both the SSH server and controller against the same database.
This is in-process instance restart coverage, not panel-process kill or machine
power-loss recovery. Separate download/reverse quota stress remains to add.

The duplex rate test uses two same-IP clients, two OpenSSH processes/four
channels each. A bills at 2×, B at 1×; rates depend only on raw payload. Its
limits match the previously fixed controller test: 300ms warmup; 1.8s initial
window; 250ms settling plus 1.5s live windows; upper rate × time × 1.06 + burst,
lower rate × time × 0.80. Each change window finishes within two seconds.

Initial runs failed the download floor. Diagnostics found 144,166 bytes admitted
but only 104,848 observed for the measured client: a startup connection probe
had opened real forwarding channels and consumed download credit outside the
observed group. Startup now uses `ssh -S <private-control-socket> -O check`, which
opens no target channel. No duration, rate or acceptance tolerance was loosened.
Ignoring Configure changes made real SSH exceed its rate bound and fail.

| Final race run | A upload / download B/s | B upload / download B/s |
|---|---:|---:|
| Initial A=64/128, B=128/64 KiB/s | 65,520 / 131,039 | 127,399 / 61,880 |
| A changed to 32/64 KiB/s | 32,737 / 64,127 | 135,347 / 65,489 |
| A changed to 128/128 KiB/s | 131,079 / 126,976 | 135,440 / 65,530 |

Unlimited SSH baseline was 2.51/2.47 MB/s under race instrumentation (5.12/5.13
MB/s without it), above the required eight times the largest cap. High-rate
capacity work noted in the controller milestone remains open.

- `make test-go`: exit 0 across the full Go suite after server/rotation changes.
  Added quota/rate fixtures then passed the complete SSH/controller race run.
- Final race: SSH PASS 14.987s; shared flow controller PASS 14.339s.
- Final package lint: exit 0, zero issues; diff whitespace check clean.
- All mutation changes were reverted. No skipped tests are counted here.

Pending: actual Xray routing/egress/block tests, strict-host-key SSH upstream,
production supervision and capability reports, CRUD/API/UI, persistent policy
controls, first-use/reason lifecycle, logs/online/IP limits, export, node sync,
deployment and complete backup/recovery acceptance. Standard -R client-side
target enforcement remains constrained by unavailable protocol metadata.

## Authenticated SSH → Xray TCP routing bridge

This extends the internal backend tests above; panel Runtime/CRUD/UI activation
is still pending. The test runs a separate real Xray child built from the exact
module `v1.260327.1-0.20260908222543-52a412d9e2f5`. Its version command reports
**Xray 26.9.9 Custom, Go 1.27.1, linux/arm64**. Client is the same real OpenSSH
9.6p1. Configurations and generated bridge credentials are written to temporary
mode-0600 files. No host service or network configuration is changed.

```sh
# Build from the pinned module directory, using that module's dependency versions.
cd "$(go env GOMODCACHE)/github.com/xtls/xray-core@v1.260327.1-0.20260908222543-52a412d9e2f5"
go build -o /tmp/3x-ui-xray-pinned ./main
# Return to this repository before these commands.
XRAY_E2E_BINARY=/tmp/3x-ui-xray-pinned \
  go test -race ./internal/routedbridge ./internal/sshtunnel ./internal/policyflow -count=1 -v
golangci-lint run ./internal/routedbridge/... ./internal/sshtunnel/...
```

The routing fixture checks an existing native SOCKS ingress first: Alice sends
32 bytes and receives six, and the actual Xray stats API reports precisely
32/6. It then uses independent OpenSSH identities through the managed bridge:

- `route.invalid:443` reaches exit A for Alice and exit B for Bob. The unresolved
  `.invalid` domain arrives at Xray; no DNS rewrite to a guessed IP is involved.
  Alice uses an exact user rule, Bob a `regexp:^bob-` user rule.
  Independent target sockets verify the actual source addresses: A uses
  `127.0.0.3`, B `127.0.0.4`, selected by Xray's sendThrough. Thus success requires
  both the correct target and egress source, within the isolated loopback setup.
- `192.0.2.7:8443` reaches exit B before Alice's later catch-all rule. Requests
  to `blocked.invalid:443` are blocked before that catch-all, and Bob's request
  to port 444 is also blocked. Rules retain the original inbound tag and TCP.
- A real OpenSSH transport bound with `-b 127.0.0.2` reaches the source-specific
  exit B, while its local dynamic-forwarding client remains on 127.0.0.1. Thus
  the SSH transport source survives the private loopback hop instead of being
  replaced by the channel's reported origin.
- Alice's admitted SSH raw totals are **128 upload / 18 download**, with
  **292 billed bytes at 2×**. This includes the accepted 32-byte request whose
  target was blocked, consistent with the documented admission boundary.
  Subsequent Xray user deltas are exactly zero: bridge payload is not added to
  the existing native 32/6 counters. The existing native policy still meters.
- Credentials generated by another bridge instance are rejected by actual
  Xray. Killing only the test Xray process makes subsequent SSH requests fail,
  including a request to a still-running directly reachable local target.
  There is no direct fallback.

A deliberately broken loopback endpoint separately verifies anonymous-auth
rejection before credentials are sent. A stalled endpoint verifies that context
cancellation closes the handshake socket within one second; its IPv6 PROXY
source is also checked. Configuration tests verify that the separate nonbilling
policy does not overwrite native or referenced levels, and a duplicate listener
application fails without partly changing the configuration.

RED → GREEN evidence: the initial route test failed because the bridge package
was absent. After implementation, deliberately replacing the authenticated
source, enabling bridge user stats, accepting an anonymous negotiation, and
reusing an already referenced policy level each triggered the intended failure.
Every mutation was restored. Final complete focused race run passed:
`routedbridge` 1.306s, `sshtunnel` 15.754s, `policyflow` 13.645s. Static lint reports
zero issues. The route test explicitly skips when XRAY_E2E_BINARY is absent;
these reported results set it and contain no skips.

Full `make test-go` also passed with XRAY_E2E_BINARY and the isolated
XUI_TEST_PG_DSN set, including the existing Xray API and PostgreSQL-enabled
tests. The subsequently strengthened target-side source-address assertions
passed in a focused race rerun. This milestone changes no frontend source;
the earlier frontend baseline record remains applicable.

This verifies a concrete internal TCP route path, not the still-missing panel
service or every routing capability. Balancer selection, routed sustained
rate/quota and DNS-refresh scenarios, production credential/rename recovery,
SSH upstream, and full management/node/deployment paths remain unverified or
unimplemented as recorded in the plan and matrix.

## Admission-account lifecycle in existing panel services

This prerequisite covers the existing service-layer reset/renewal/traffic-tick
entry points with real database state. The panel still has no public admission
account activation path or production SSH manager. Tests create owned accounts
explicitly; their VLESS attachment records do not start a native VLESS service.

Behavioral RED cases before the service changes:

- A traffic tick disabled a 0.5× client at raw 150 / billed 75 with quota 100.
- Panel reset paths left the account at raw 150 / billed 225 / revision 2 while
  zeroing its legacy projection. Automatic renewal had the same stale account.
- The initial managed single-client branch sent no remote reset request and
  omitted inbound reset timestamps. These regressions were reproduced and fixed.
- Increasing quota from 100 to 1100 incorrectly enabled a manually disabled
  account. The old raw-depletion auto-enable path now excludes owned accounts.

Focused verification command, with the task toolchain on PATH:

```sh
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
  go test -race ./internal/database ./internal/web/service \
  -run '^TestAdmissionReset|^TestManagedUsage|^TestResetTrafficOfDepletedClient|^TestAutoRenewClients' \
  -count=1 -v
```

Observed after the batching fix: database PASS 1.774s; service PASS 39.912s.
SQLite and PostgreSQL 16.15
run real transactions; PostgreSQL cases use temporary per-test schemas in the
isolated test instance. The seven service PostgreSQL cases were executed, not
skipped. They cover restriction reasons, bulk rollback, expired reset, renewal,
quota editing, live TCP reset and mixed legacy/managed batching. Existing legacy renewal/reset regression tests
also pass with their original assertions.

Seven panel reset paths clear raw/billed/carry together, preserve multiplier and
manual disable, and retire old sources. Four concurrent admission sources race
reset in each DB backend; no old source can commit into the new period. A bulk
reset containing an unsettled observed source rolls back earlier clients,
source closures, raw projections and group baselines. A reset leaves expired
clients blocked; a scheduled renewal leaves manually disabled clients blocked.

Real loopback TCP tests keep two clients connected through the actual policy
controller while calling the panel reset service. The reset client's old flow
closes in 501.7ms on SQLite and 541.5ms on PostgreSQL, below the predeclared
1.25s bound; the unrelated client remains usable. Two inbound attachments still
produce exactly one account revision increment. After explicit controller
reconfiguration, seven new download bytes bill ten bytes plus 0.5 carry at 1.5×.

The node case sends actual Runtime HTTP requests to a local test endpoint:
two attachments on one node produce one reset request, and local timestamps/
dirty markers persist. It verifies dispatch and local bookkeeping only, not
remote ledger enforcement or distributed atomicity. A first run of the new
bulk-rollback test used a nonexistent fixture column; that fixture error was
corrected to the schema's `group_name` before the passing verification above.

Deliberately removing the observed-source guard made its reset test fail.
Removing source closure alone did not defeat the live-flow test: the unchanged
revision guard still closed the connection. Removing both closure and revision
fencing made the real TCP test fail at its original 1.25s deadline. All mutations
were restored before regression validation; these checks did not relax bounds.

The initial lifecycle implementation regressed legacy bulk-reset cost: resetting
838 clients made 2,531 query/update callbacks. A test capped these at 100 before
the fix. Sorted, chunked canonical-row locking and batched ownership lookup
reduce the count to at most 25 on both SQLite and PostgreSQL, including the
post-reset verification queries. Raw and billed totals still reset together.
This is a bounded-query regression check, not a many-client throughput claim.

Final `make test-go` passed after the batching change with both
`XRAY_E2E_BINARY=/tmp/3x-ui-xray-pinned` and the isolated `XUI_TEST_PG_DSN` set.
This includes actual OpenSSH/Xray regression paths; other externally gated tests
remain subject to their documented prerequisites and are not counted as executed.
`golangci-lint run ./internal/database/... ./internal/web/service/...` reports
zero issues; `go build -o /tmp/3x-ui-panel-lifecycle .` succeeds. Formatter output
and `git diff --check` are clean. This milestone changes no frontend source or
public API schema; prior frontend results remain recorded above.


## SSH production Runtime increment (partial vertical)

Existing Inbound/Client service methods now create and edit an actual dedicated
SSH listener through Runtime. `TestSSHInboundRunsThroughProductionXrayLifecycle`
uses fresh test keys, strict OpenSSH known_hosts, the pinned real Xray executable,
a domain-routed echo target and a second independent SSH identity. It verifies:

- Default 1× billing: the first 16-byte upload and 16-byte download produce exactly
  32 billed bytes. Bridge traffic is not billed again.
- Starting the service leaves negative expiry untouched; signed authentication
  sets the same positive expiry in canonical, traffic and inbound settings.
- Metadata updates retain credentials; key revocation, reset, quota reduction and
  manual disable close affected flows while the unrelated existing flow continues.
- Reset automatically obtains a fresh source; one subsequent byte each way gives
  exactly 2 new billed bytes. Raising quota restores eligible access.
- Disabled listeners stay closed before the next core application; re-enable and
  actual core restart recover. An occupied public port logs one protected-state
  warning and recovers after release. Core configuration permissions remain 0600.
- Directly stopping the actual child process, without the panel's SSH shutdown,
  closes the SSH listener in approximately 206–232ms (test limit 1.25s). Panel stop
  also closes an established flow. A native Xray listener is removed when converted
  to an empty SSH service.

Behavioral RED preceded fixes for missing canonical credentials, missing account
ownership, delayed-expiry authentication, missing-key creation, resurrection of a
disabled listener, unrelated-client disruption on disable, silent bind failure,
detached credential edits, blocked authentication behind the writer, saturated
queue cancellation, protocol-conversion listener leakage, partial preparation and
new SSH logins authenticating before their renamed routing identity was applied.
A real short-lived child-process race test exposed `Cmd.Start` publication racing
`IsRunning`; `2ec493e05ccd8ca2a18acbc035373ec7a70f7b81` fixes it, was pushed, and
was matched exactly by remote SHA lookup.

Observed checks before the final full regression:

- Focused race tests for SSH services and existing managed lifecycle paths:
  PASS, 35.702s, including actual PostgreSQL cases. An earlier broader run failed
  with `sql: Scan called without calling Next`; replacing scalar protocol Pluck
  with an existence count fixed the legacy synchronization regression.
- All `internal/sshtunnel` tests with race detection, real OpenSSH and real Xray:
  PASS, 16.806s. `internal/routedbridge` also passed. Rate/quota tests were executed,
  not inferred from the service test's default unlimited/1× settings.
- SQLite and PostgreSQL old-schema/identity tests plus SSH JSON/merge and real
  service tests: PASS (`database` 17.420s, `model` 1.135s, `service` 10.920s).
  SSH credentials/permissions survive SQLite backup restore and actual
  SQLite→PostgreSQL migration; stale merges cannot restore old credentials.
- `npm run gen`, `npm run typecheck`, `npm run lint`, `npm run build`: exit 0.
- Existing generated API/example/runtime-contract tests: 3 files, 67 tests passed.

Reproduce using the pinned Xray binary described in the routing milestone:

```sh
XRAY_E2E_BINARY=/path/to/pinned-xray \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
go test -race ./internal/web/service -run '^(TestSSH|TestManagedUsage|TestTrafficWriter)' -count=1 -timeout=240s -v
XRAY_E2E_BINARY=/path/to/pinned-xray go test -race ./internal/sshtunnel ./internal/routedbridge -count=1 -timeout=240s -v
```

The PostgreSQL server and port are a task-local test fixture, not deployment
instructions. Missing executable/database prerequisites skip their gated cases;
those skips never count as data-path verification. SSH upstream, public rate and
multiplier controls, UI/export, native mixed attachments, node policies and the
other new protocols remain open. This increment does not pass whole-task acceptance.


Final rename guard verification: race-enabled SSH service and writer cases passed
in 16.770s on SQLite and PostgreSQL. Before the guard, both focused and full Go
runs correctly rejected authentication with a newly renamed label while the
bridge still held the old identity. After application, the new login forwards
and retains the original billing policy UUID. Saturated writer queue cancellation
also passed; queue waits no longer hold the global writer-state mutex.

Final increment checks: `make test-go` with both real-backend environment variables
passed all 50 tested packages (database 65.279s, service 71.509s). Final Go lint
reported 0 issues; `go build ./...`, generated-file freshness and `git diff --check`
passed. Logs are `/tmp/3x-ui-ssh-full-regression-final.log`,
`/tmp/3x-ui-ssh-final-lint.log`, `/tmp/3x-ui-ssh-final-build.log` and
`/tmp/3x-ui-ssh-gen-check.log`. These checks validate this partial increment, not
unimplemented protocols, UI controls, global node policy or full acceptance.

The commit hook initially failed because staged generated TypeScript files match
lint-staged but are intentionally ignored by oxfmt. The formatter's documented
`--no-error-on-unmatched-pattern` option now permits that empty ignored set; normal
source files still run through formatting and linting. This tooling prerequisite
is committed separately as `263a7c78`; hooks were not disabled.

## Durable local policy API and production SSH shaping

The existing client API now reads and edits raw upload/download B/s, an exact
positive decimal billing multiplier, explicit local scope and an edit version.
Requests carry the immutable policy ID as well as the version, preventing a
stale editor from changing a newly created client with a reused email. Byte
counters are decimal strings, including values above JavaScript's safe integer
range. The account remains the sole authority for the multiplier and carry.

- Service tests first failed on missing policy methods. SQLite and real
  PostgreSQL now verify atomic rates/multiplier boundaries, stale edits,
  observed-source rejection with full rollback, reset persistence, and legacy
  reads that neither activate accounting nor reprice previous usage.
- Recreated-label regression first accepted the obsolete edit. Requiring the
  immutable ID alongside the edit version rejects it without changing the
  replacement client. Concurrent editors must produce one success and one
  conflict.
- HTTP tests execute the real authorization and scope middleware: anonymous
  XHR requests get 401, monitor/node-sync tokens get 403, and admin requests
  reach the actual database service. Raw 9007199254740993 bytes is returned
  exactly; changing the multiplier does not reprice that history.
- Actual OpenSSH clients traverse two service-created SSH inbounds and the
  pinned Xray. Each of two clients uses two SSH transports and four channels,
  simultaneously uploading/downloading from the same source IP. Before runtime
  wiring, a requested 65536 B/s cap delivered 6782976 bytes in 1.5 seconds.
- The new runtime wiring reads persisted policies in bounded batches and updates
  the shared per-client limiters. The existing unlimited-rate controller no
  longer overrides saved limits. Invalid/unavailable policy protects listeners.
- Initial multi-client runs also exposed SQLite writer starvation: independent
  busy-handler retries caused `context deadline exceeded` and false stream
  cutoff. A cancellable database-local writer queue now covers ledger operations
  and serialized panel transactions. Nested transactions keep their existing
  ownership; PostgreSQL retains concurrent row-level transactions. The original
  500 ms database operation limit and one-second freshness policy are unchanged.
- With that correction, the race-enabled real path passed a 1.55–1.64 MB/s
  unlimited baseline and initial 65536/131072 B/s caps. Live edits measured about
  32752/65514 and 131074/131063 raw B/s for the changed client; the other client
  retained about 131063/65526 B/s. The unchanged acceptance window is 80% to
  106% of the configured rate plus the declared 100 ms burst, capped at 64 KiB.
  Live update plus its 1.5-second measurement fits within two seconds. Existing
  streams stay open; multiplier 2 produces exactly twice the raw admission sum.
- The same production test passed on real PostgreSQL. Schema omission tests
  failed with a missing policy table before the migration was enabled; identity,
  SQLite backup/restore and cross-dialect policy persistence then passed with
  race detection (database 16.986s).

Reproduce with the same actual core and isolated PostgreSQL fixture:

```sh
XRAY_E2E_BINARY=/path/to/pinned-xray \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
go test -race ./internal/web/service -run '^TestClientPolicy' -count=1 -v
go test ./internal/web/controller -run '^TestClientPolicyHTTP' -count=1
```

Limits: this increment exposes local policy through the authenticated API;
policy UI, portable client import/export, other backends and global node leases
remain required. Non-SSH or remote attachments and global scope are explicitly
rejected until their real execution paths exist. The broader goal is incomplete.

Final increment verification: complete `GOFLAGS=-p=1 make test-go` with the actual
core and PostgreSQL passed all 50 tested packages (database 47.827s, service
80.735s). Package serialization prevents unrelated tests from competing with
bandwidth windows. Final focused race tests also passed the extra restart and
concurrent-edit assertions: production SSH 11.93s on SQLite and 12.06s on
PostgreSQL, including reloaded nonzero caps after complete core/manager shutdown.
No existing transfer ended during the rate-only edits. Existing managed
restriction/reset/renewal and bounded writer-cancellation tests passed too.

Go lint reports zero issues, the main Go build passes, frontend typecheck/lint/
build pass, and 70 generated/OpenAPI contract tests pass. The documentation
site's pinned pnpm 12.6.0 frozen install, API MDX generation and typecheck pass.
Its current generator mechanically replaces the old v10 component fallback in
all generated API pages. Full logs are `/tmp/3x-ui-policy-full-go.log`,
`/tmp/3x-ui-policy-final-race.log`, `/tmp/3x-ui-policy-final-lint.log`,
`/tmp/3x-ui-policy-final-build.log`, `/tmp/3x-ui-policy-frontend-contracts.log`
and `/tmp/3x-ui-policy-docs-typecheck.log`. No full-frontend suite, complete UI
workflow, kernel adapter, packaging/deployment or whole-goal claim follows from
this increment's focused frontend/API checks.

## Existing-client policy editor and real browser path (2026-09-28)

This increment adds the Traffic policy tab to the existing client editor. Six
component tests first failed because that tab was absent. They now exercise the
real modal, schema, query cache and forms (only HTTP responses are test doubles):
exact `9007199254740993 B`, fractional `9007199254740995.501 B` billing and
`999.499 B` remaining, version/identity submission, invalid rate/multiplier,
background refresh retaining dirty inputs, ambiguous post-commit failure and
unsupported attachments. A malformed usage fixture also reproduced a BigInt
conversion exception; validation now rejects it before conversion.

The standalone browser fixture uses a freshly built panel binary, temporary
SQLite database, independent generated client and host keys, Chromium, system
OpenSSH and the pinned real Xray. It creates a managed SSH inbound through the
real authenticated HTTP endpoint, opens Clients → Edit → Traffic policy, saves
32768/65536 B/s and multiplier 1.5, then checks the returned policy. A real
16384-byte echo over strict-host-key OpenSSH → managed policy flow → Xray →
loopback target produces exactly 16384 B raw upload, 16384 B raw download and
49152 B billed. The browser displays those bytes. An independent API update
changes the version; the dirty browser draft remains intact and cannot submit
until an explicit reload restores the saved rates. The browser reports no
uncaught page errors. This is connectivity/accounting/UI evidence; the larger
rate windows and live-flow bounds remain those of the preceding runtime tests.

The first browser attempt had a login-label capitalization mismatch. The next
attempt found a real defect: the inbound HTTP validator's `oneof` omitted `ssh`.
`TestManagedSSHInboundRequestValidation` reproduced that rejection before the
model validation tag and generated API contracts were fixed (`724c41ff`). A
later attempt selected a card-view menu on the default table; the fixture now
uses the actual accessible Edit button. None of those failed runs count as
acceptance passes.

Reproduction (Linux with Node 26, Python 3, OpenSSH, installed Playwright Chromium
and the pinned Xray binary; no Docker or host network changes):

```sh
cd frontend
npm run build
cd ..
go build -o /tmp/3x-ui-policy-ui-panel .
XUI_E2E_PANEL=/tmp/3x-ui-policy-ui-panel \
XRAY_E2E_BINARY=/path/to/pinned/xray \
node frontend/scripts/client-policy-e2e.mjs
```

The script binds only loopback listeners, disables its temporary subscription
server, installs no system service, changes no firewall rules and removes its
own processes, keys and database in `finally`. `XUI_E2E_SCREENSHOT` optionally
captures the test client editor before cleanup. The test does not use Git keys.

Executed results:

- `npm test -- --maxWorkers=1`: **176 files / 1756 tests passed**, including
  headless Chromium Storybook; 450.77 s. No skipped test counted as a pass.
- Targeted component/schema/dead-i18n tests: **3 files / 10 tests passed**.
- `npm run typecheck`, `npm run lint`, `npm run build`: passed.
- `go test ./internal/web/middleware ./internal/database/model`: passed.
- Focused `golangci-lint run` for those Go packages: **0 issues**.
- `go build -o /tmp/3x-ui-policy-ui-panel .`, `make gen-check` and API website
  regeneration: passed.
- Standalone browser → real API → real SSH/Xray test: passed, process exit 0.

Logs: `/tmp/3x-ui-policy-ui-full-frontend.log`,
`/tmp/3x-ui-policy-ui-green.log`, `/tmp/3x-ui-policy-ui-browser.log`,
`/tmp/3x-ui-ssh-http-validation-red.log`,
`/tmp/3x-ui-ssh-http-validation-green.log`, `/tmp/3x-ui-policy-ui-build.log`.

At this policy-editor checkpoint, exact billing display was verified specifically
in that tab. The following increment covers client-list billing. Creation/bulk
policy, SSH credential and inbound UI/export, other usage consumers, backend
executors and distributed limits still remain open.

## Billed client-list integration (2026-09-28)

Client list, information-modal and list-summary reads now expose owned ledger
usage without creating accounts on read. The SQL predicates/order use billed
whole bytes plus thousandth-byte carry; legacy clients retain raw semantics.
The initial regression selected only the raw-exhausted discount account, instead
of the double-priced, fractional, traffic-cap and large-integer exhausted
accounts. Component regressions initially displayed raw consumption and omitted
the exact billed description; Ant Design's default ARIA percentage also truncated
99.5 to 99. The component now supplies the bounded precise percentage explicitly.

SQLite and actual PostgreSQL tests cover multiplier 0.5/1/1.5/2/4, an effective
traffic quota below the canonical quota, inability to fund one further raw byte,
exact 9007199254740993.5 B usage, inclusive integer filter boundaries, fractional
remaining/usage ordering, status counts and unpaged serialization. An unrelated
global raw overlay does not replace owned local billing. Reading these views
does not activate a legacy account.

The cleanup regression originally deleted a discounted client with 120 raw /
60 billed bytes while retaining a double-priced client with 50 raw / 100 billed
bytes at a 100-byte quota. Both cleanup entry points now use billed candidate
selection. Tests preserve interval/monthly/weekly canonical renewal even with
stale zero renewal fields in the traffic projection. Scoped cleanup removes the
spent client's selected inbound attachment and retains traffic referenced by its
sibling; subsequent global cleanup removes that spent traffic. These tests run
on SQLite and PostgreSQL. The original inbound fixture lacked required SSH
credentials/traffic ownership and was corrected to seed complete persisted
state; those failed fixture runs are not acceptance passes. At this milestone,
the old client bulk-delete fanout was still non-atomic with candidate selection
under concurrent reset/quota edits. The subsequent atomic-purge increment below
addresses that race for both depleted-cleanup entry points.

The real browser fixture now checks paged and hydrated billing after its actual
SSH/Xray echo: 16384 B in each raw direction, 49152 B billed at 1.5x, and
104808448 B remaining from 100 MiB. The table shows 99.95 MiB remaining. It then
reduces quota to 32769 B (above 32768 raw bytes, below 49152 billed bytes), checks
one depleted client in the API/summary, a 0 B table balance and a real OpenSSH
payload refusal with no target response. No connection-refused or test-timeout
error is accepted as the quota assertion. The final screenshot was inspected.

Early browser attempts exposed a readiness assumption: public-key authentication
succeeded but the transport closed while the multiplier's old meter was fenced.
The controller checks/replaces that meter on its bounded reconciliation cycle.
The fixture now polls its own SQLite account/meter revision for at most two
seconds before the first post-edit connection, then still exercises actual
OpenSSH. The final run observed no current meter initially and readiness after
309 ms. This is a readiness gate, not a throughput measurement or a retry of
the exhausted-account assertion. The API's supported flag remains a capability
flag, not a health/readiness endpoint. Lifecycle-only diagnostics are emitted
on fixture failure; private keys and panel credentials are not printed.

Validation failures retained as failures:

- The first full Go command overlapped a Vite build deleting/replacing embedded
  assets; root/web compilation failed with missing dist files. Subsequent build
  and Go execution are sequential.
- The first full frontend run had four 5000 ms timeouts (three existing inbound
  form cases and one policy-editor case), with 1755 other tests passing, while
  competing with other heavy checks. No timeout or assertion was relaxed.
- A standalone full frontend run passed all 177 files / 1759 test assertions but
  exited 1 on an unhandled React `window is not defined` callback attributed to
  the existing Happ routing editor during environment teardown. Its isolated
  18-test rerun passed. This does not retroactively turn the full run into a pass.

Executed checks so far for this increment:

- Focused Go list/billing/purge regression including actual PostgreSQL: passed,
  4.711 s (`/tmp/3x-ui-billing-targeted-final.log`).
- Frontend generated contracts, typecheck, lint, formatting (714 files), MSW
  worker consistency and production build: passed.
- Embedded `go build`, API website generation and website typecheck: passed.
- Final real browser + HTTP + SSH + Xray fixture: passed, exit 0
  (`/tmp/3x-ui-billing-browser-verified.log`), screenshot
  `/tmp/3x-ui-billing-list.png`.
- A subsequent full Go runner received SIGTERM (exit 143) during the database
  package without reporting an assertion failure. Its partial run is not a pass;
  the cause was not identified. A dedicated-terminal rerun finished with only
  the existing AmneziaWG fixed-port regression failing (TCP 58930 already in
  use); the service package passed in 96.616 s. An owned loopback listener on
  58930 reproduced the old fixture failure, and replacing that constant with
  the existing freePort helper passed unchanged assertions while the listener
  remained active. That one-line prerequisite was committed separately as
  `f81bbf56` and its approved fork push was independently verified. No unrelated
  listener was stopped. The fresh full Go rerun passed all 50 packages with
  tests, with 5 packages reporting no test files; exit 0. Database tests took
  47.658 s and service tests 87.819 s
  (`/tmp/3x-ui-billing-full-go-verified.log`).
- Focused billing/list/purge race tests on SQLite and PostgreSQL passed in
  13.549 s (`/tmp/3x-ui-billing-race-final.log`). Full Go lint reported
  0 issues (`/tmp/3x-ui-billing-lint-final.log`), and `make gen-check` passed.
- Two standalone full frontend runs passed all 1759 assertions but exited 1
  with the same Happ/React teardown exception (415.34 s and 413.77 s). Its
  static Ant Design messages own a separate React root and auto-close timers.
  The test now destroys those messages inside awaited React act during cleanup;
  no assertions, error detection or timeout values changed. The focused 18-test
  rerun passed in 16.48 s (`/tmp/3x-ui-happ-cleanup-green.log`). The final full
  suite passed **177 files / 1760 tests**, exit 0 with no unhandled errors, in
  411.93 s (`/tmp/3x-ui-billing-full-frontend-cleanup-fixed.log`). This includes
  the new billing Storybook example. The cleanup is a separate test-only commit,
  `41b498d1`; all application behavior assertions remain intact.
- The reusable traffic cell now documents its billing prop and a fractional
  1.5x example in its existing Storybook file. Typecheck/lint and
  `npm run build-storybook` passed; Vite reported its existing advisory about
  chunks over 500 kB (`/tmp/3x-ui-billing-storybook-build.log`).

Reproduce with the preceding browser command and the same pinned binaries.
This increment does not verify global node dashboard counts, inbound widgets,
notifications/subscription billing, distributed policy, other protocol adapters
or deployment. No whole-goal completion is claimed.

### Atomic depleted-client purge (2026-09-28)

Both depleted-cleanup entry points now capture the traffic row ID, canonical
client ID and policy identity in one candidate query. They acquire affected
inbound mutation locks in sorted order before entering the serial writer, then
lock canonical and traffic rows in sorted order and recheck both eligibility
and identity inside the deletion transaction. A reset or quota increase that
commits before those locks survives; a replacement identity is excluded even
when its label and numeric IDs are reused and it is itself depleted. Distinct
case-sensitive canonical labels remain distinct throughout cleanup.

Settings, membership, traffic/IP/global/node counters, group baselines and,
for global client deletion, HWIDs, external links and canonical rows are changed
together. Inbound-only cleanup retains canonical records, preserves traffic
referenced by a sibling inbound, and removes empty inbounds as before. Surviving
clients are not resynchronized from possibly stale settings. An attachment
added outside the initially locked inbound set causes an explicit retry error
and a rollback. Runtime calls and routing-reference cleanup occur after commit;
node dirty flags are persisted with the deletion. Ordinary explicit BulkDelete
is unchanged by this increment.

The first regression run paused the actual candidate query with a GORM callback,
completed the public reset or bulk quota edit, then resumed cleanup. Old client
cleanup deleted credited clients on SQLite and PostgreSQL; old inbound cleanup
deleted their inbound on PostgreSQL and also exposed SQLite lock failures.
Replacing the canonical policy identity reproduced stale-label deletion too.
Injecting a traffic-row deletion failure showed that the old client path could
leave settings empty while reporting success. Logs:
`/tmp/3x-ui-purge-concurrency-red.log` and
`/tmp/3x-ui-purge-identity-rollback-red.log`.

A first implementation locked every inbound. A parked runtime removal proved
that this blocked an unrelated client's public quota edit for the test's full
five-second deadline. The implementation now locks only affected inbounds;
the same test completes the edit while the runtime remains parked and verifies
that deletion was already committed. The traffic writer is also free during
that wait (`/tmp/3x-ui-purge-isolation-red.log`).

Additional mutation checks removed row locks and identity checks, and restored
case-folded membership matching. They reproduced replacement-identity deletion,
allowed independent PostgreSQL `FOR UPDATE NOWAIT` probes to acquire both state
rows after eligibility was checked, and removed a distinct healthy uppercase
identity. With the real implementation restored, the PostgreSQL probes report
SQLSTATE 55P03 for both rows. Logs:
`/tmp/3x-ui-purge-lock-identity-mutation-red.log`,
`/tmp/3x-ui-purge-state-lock-mutation-red.log`, and
`/tmp/3x-ui-purge-case-identity-red.log`.

Test-harness corrections: the rollback PostgreSQL cases initially shared a
schema and collided on an inbound tag; each case now owns its schema. The lock
probe initially also counted GORM DryRun subquery callbacks; it now observes
only executed queries with returned rows. An intermediate compile failed on an
unused import left by extracting the old cleanup function; that import was
removed. None of these runs count as acceptance passes.

Focused regression with SQLite and actual PostgreSQL passed in 9.642 s:

```sh
export XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable'
go test ./internal/web/service \
  -run '^(TestDepletion|TestBilledDepletionPurge|TestDelDepleted)' \
  -count=1 -timeout=90s
```

Log: `/tmp/3x-ui-purge-final-targeted.log`.

Further verification:

- `go test -race -shuffle=on -count=1 -timeout=5m ./internal/web/service
  -run 'Test(Depletion|BilledDepletionPurge|DelDepleted|ManagedUsage|ResetTrafficOfDepleted|GroupTotalsSurviveClientDelete)'`
  passed in 47.618 s with the same PostgreSQL DSN and pinned Xray binary
  (`/tmp/3x-ui-purge-race.log`).
- `GOFLAGS=-p=1 XRAY_E2E_BINARY=/tmp/3x-ui-xray-pinned
  XUI_TEST_PG_DSN=... make test-go` passed: 50 packages with tests, 5 packages
  without test files, exit 0. Database tests took 47.545 s and service tests
  96.693 s (`/tmp/3x-ui-purge-full-go.log`). Opt-in scale jobs were not enabled;
  package success is not a claim that skipped opt-in cases ran.
- Initial lint reported two test-only findings: a non-wrapping error format
  and an if-chain suitable for a switch. After correcting them, the focused
  SQLite/PostgreSQL suite passed again in 9.457 s
  (`/tmp/3x-ui-purge-final-targeted-lint-fixed.log`), full `make lint-go`
  reported 0 issues (`/tmp/3x-ui-purge-lint-final.log`), and `go build ./...`
  exited 0 (`/tmp/3x-ui-purge-build.log`). Production code was unchanged after
  the full Go and race runs. No frontend files changed in this increment.

This increment validates management
transaction boundaries, not a new protocol data path or distributed node
atomicity. Runtime failures still use the existing reconciliation mechanism;
the complete protocol, deployment and other usage-consumer requirements remain
open.

## SSH creation forms and strict OpenSSH export (2026-09-28)

Scope: existing inbound form, existing client create/edit form, and the client
information/QR export dialogs. The server endpoint adds only the actual host
public key, derived from its stored signing key. SQLite and PostgreSQL tests
check exact public-key identity, no private-key disclosure, and omission after
an invalid stored key. No new migration or endpoint is introduced.

Frontend form tests drive real React Hook Form/AntD controls. They verify empty
SSH listener creation without Xray transport/TLS/sniffing, lifecycle and host
identity preservation on editing, public-key client creation with explicit
permissions, default-off reverse forwarding, and rejection of empty/private-key
input. They also prevent presenting unimplemented SSH IP/device restrictions as
effective controls. Temporarily removing public-key validation made the private-
key case fail; the mutation was restored before subsequent checks.

`ssh-export.test.ts` invokes actual OpenSSH `ssh -G` on generated files and checks
an IPv6 endpoint, username/port, explicit trust source and restricted identity /
session settings. It also exercises missing pins and directive injection. This
requires the OpenSSH client executable; absence is a test failure, not a mock or
silently counted skip. In this sandbox the child invocation needs elevated exec.
The real browser test consumes the files to authenticate, which additionally
checks known_hosts path resolution and cryptographic host-key enforcement.

`frontend/scripts/client-policy-e2e.mjs` now creates the inbound and client in
Chromium, downloads the two files from Client Information, and compares the pin
with the authenticated options endpoint. A deliberately different host key must
produce `Host key verification failed` and no bytes. Restoring the downloaded
pin permits the real OpenSSH → managed SSH → pinned Xray → echo path. Independent
socket observations remain 16384 raw bytes each direction and 49152 billed bytes
at 1.5x. Reducing quota to 32769 bytes still marks one depleted client, shows 0 B
remaining and rejects a new OpenSSH request with no bytes.

The clean run in `/tmp/3x-ui-ssh-browser-diagnostic.log` exited 0. Initial listener
application took 21434ms through the pre-existing 30-second pending-configuration
job. The later multiplier-boundary meter was initially unavailable and became
ready in 670ms, within the unchanged 2s live-policy bound. Startup now waits for
an actual SSH banner (one scheduler cycle plus 5s observation), separately from
that live-policy check. No explicit restart API is used to bypass creation.
The final screenshot `/tmp/3x-ui-ssh-browser-diagnostic.png` was inspected.

Failed attempts are retained and do not count as successful acceptance:

- Previous embedded panel: no SSH protocol in the real creation form (expected
  RED, `/tmp/3x-ui-ssh-browser-red.log`).
- Component test harness: virtualized last dropdown option and generated port
  input ID were initially selected incorrectly; scoped non-virtual test rendering
  and accessible labels fixed those selectors. The actual browser still tests
  the original virtualized dropdown.
- Client component harness: AntD's plus icon contributes to button naming, and
  this project does not install the `toHaveValue` matcher. Correct selectors and
  direct input-value assertions replaced those harness assumptions.
- First export implementation rejected bracketed IPv6 and allowed the generic
  share-host helper to replace an invalid custom address. Tests caught both;
  export now strips valid IPv6 brackets and rejects invalid custom input.
- Default sandbox denied `ssh -G` process launch with EPERM; the same real parser
  tests passed with the required exec permission.
- First browser run used an exact add-target name without its icon; corrected
  the selector. The next run applied policy before the initial 30s configuration
  job; it failed the 2s meter check. Readiness observations now distinguish initial
  deployment from a live policy update instead of changing the policy bound.
- One run reached correct API/socket billing but timed out waiting 10s for the
  list progress bar. A diagnostic rerun passed with unchanged UI assertions and
  timeouts; this intermittent refresh failure remains recorded, not hidden.
- Initial typecheck found test-only matcher options/incomplete HTTP fixture
  fields; initial lint found a redundant effect dependency and a control-regex
  rule violation. These were corrected before the passing checks.

Focused backend export tests passed SQLite and PostgreSQL (1.045s). Frontend
creation/credential tests passed before broad regression (2 + 3 cases), and
actual OpenSSH export tests passed (2 cases). Generated contracts, frontend
TypeScript, lint and Vite build passed; the rebuilt embedded panel was used for
the browser run. Full-suite and final static results follow below once terminal.

SSH upstream, online/IP/device enforcement, bulk/portable client policy export,
inbound-specific client actions, full applied-state health, remote nodes and
installation remain open. This is a verified creation/export slice, not full SSH
or whole-request completion.

The first full frontend run exited 1: 180 files, 1765 passing assertions, two
5s timeouts (`ssh-client-form` creation and the existing `inbound-form-modal`
Reality validation case), and one uncaught React `window is not defined` during
`happ-settings-presets` teardown. That preset test also calls AntD static messages;
an initial fixture change added awaited `act(message.destroy())`, following the
editor test's earlier cleanup. That attempt was insufficient, as the serial run
below demonstrated. No production Happ code or assertions changed.
`/tmp/3x-ui-ssh-full-frontend.log` records the first full failure.

An additional compatibility regression reproduced `clients: null` from an empty
Go SSH settings slice being rejected by the new form schema. The schema now
normalizes null/absent clients to an empty array, preserving host key and bridge
port. RED is `/tmp/3x-ui-ssh-empty-inbound-red.log`. Focused SSH creation/edit,
credential and Happ preset tests then passed all 15 cases across three files in
22.59s (`/tmp/3x-ui-ssh-forms-presets-focused.log`). A full serial frontend rerun
retains the original 5s per-test deadlines and every assertion; no timeout or
numeric acceptance limit was widened.

The serial rerun passed all 180 files / 1768 cases in 429.03s but still exited 1
with the same preset teardown exception
(`/tmp/3x-ui-ssh-full-frontend-serial.log`). Inspecting the installed AntD and
rc-component implementation showed that `message.destroy()` closes notices but
retains the independently created React root. Both Happ fixture files now track
their document fragments, await real root unmount through rc-component's utility,
and reset AntD's test-only holder reference. The components and assertions remain
real; no exception suppression or message mocks were added. Focused presets,
editor and SSH inbound tests passed all 30 cases in 22.03s
(`/tmp/3x-ui-happ-root-unmount-focused.log`). Full regression then passed all
180 files / 1768 cases in 431.56s, exit 0 with no unhandled errors
(`/tmp/3x-ui-ssh-full-frontend-root-cleanup.log`). This test-only fix was committed
separately as `5f51dccb` and pushed; an independent remote query matched the full
local SHA. Frontend typecheck, lint and formatting (724 files) also passed.

The earlier browser list-refresh timeout now has a reproduced cause. Xray's
five-second `client_stats` events patch the query cache; TanStack Query resets
its five-second observer polling timer on each patch. The new
`clients-billing-refresh.test.tsx` drives the actual hook/cache with repeated raw
statistics and a controlled HTTP boundary. Old code retained billed usage `0`
instead of the independently expected `49152`
(`/tmp/3x-ui-billing-refresh-red.log`). An independent five-second poll now keeps
REST-owned billing/sorting/summary updates from being postponed by those patches,
preserving focus gating and unmount cleanup. Nine focused cases passed in 3.89s
(`/tmp/3x-ui-billing-refresh-green-final.log`). An intermediate test run confirmed
the balance fix but read the last raw-stat update before QueryClient's scheduled
notification; draining that zero-delay notification retained the exact raw-value
assertion. The rebuilt real browser path then passed unchanged in
`/tmp/3x-ui-ssh-ui-browser-final.log`: 21934ms for initial scheduled deployment,
409ms for the later meter replacement, exact duplex/billed bytes, wrong-host-key
refusal, live list refresh and exhausted-client denial. Screenshot
`/tmp/3x-ui-ssh-ui-final.png` was inspected. Full regression after the polling
change passed all 181 files / 1769 cases in 431.17s, exit 0 without unhandled
errors (`npm test -- --maxWorkers=1`,
`/tmp/3x-ui-ssh-ui-full-frontend-final.log`). Existing Node deprecation and
Vitest plugin-hook notices remain visible; no errors were filtered.
The polling fix was committed separately as `6d879640`, pushed to the approved
feature branch, and independently verified against the remote SHA.

Full Go regression with `GOFLAGS=-p=1`, the pinned actual Xray binary and the
isolated PostgreSQL DSN passed: 50 packages with tests, 5 without test files,
exit 0 (`/tmp/3x-ui-ssh-ui-full-go.log`). Database tests took 48.606s and service
tests 99.633s. Opt-in scale jobs were not enabled; package success does not count
skipped opt-in cases as executed acceptance.

Further final checks passed:

- `go test -race -shuffle=on -count=1 ./internal/web/service
  -run '^TestSSHInboundOptionsExportOnlyActualHostPublicKey'` with the same actual
  PostgreSQL DSN: 4.399s (`/tmp/3x-ui-ssh-ui-options-race.log`).
- `make lint-go`: 0 issues; `go build ./...`: exit 0; `make gen-check`: fresh
  generated contracts matched the staged artifacts. Logs use the prefix
  `/tmp/3x-ui-ssh-ui-` and suffixes `go-lint.log`, `go-build.log`, `gen-check.log`.
- Final frontend `npm run typecheck`, `npm run lint`, `npm run format:check`
  (724 files), MSW worker equality and frontend/docs OpenAPI equality passed.
  `npm run build-storybook` passed in 7.22s with an advisory chunk-size warning.
- Vite production build passed in 2.70s; `go build -o /tmp/3x-ui-ssh-ui-panel .`
  produced the embedded panel used by the passing final browser fixture.
- Docs `npm run typecheck` passed, including MDX generation, Next route types
  and TypeScript (`/tmp/3x-ui-ssh-ui-docs-types.log`).


### Portable restoration transaction (2026-09-28)

The old importer activated attached clients before restoring counters, left
created clients behind after a failed restore, and reused matching email/subId
identities despite the documented skip-existing behavior. New regression tests
first reproduced all three failures, plus a partially committed first attachment
when the second attachment failed. The node Runtime boundary observed 0/0 rather
than the fixture's 17/23 bytes before the change. After the change it observes
17/23 with resetCount=2 from the already committed restoration.

A separate RED test found the SSH ledger remained at 0/0 after restoring raw
5/6, and an orphan SSH import had no ledger. Both now restore 11 billed bytes at
legacy default multiplier 1 with a fresh policy identity; a quota of 11 rejects
admission. Negative upload/download, overflowing raw sum and negative reset
counts are rejected without creating a client. These tests exercise the actual
DB/service/admission ledger, not an SSH network connection; they do not prove
complete portable policy support or remote-node quota synchronization.

Commands so far:

- `go test ./internal/web/service -run '^Test(Import|ExportImport|BulkCreate|AddInboundClient)' -count=1`:
  PASS, 2.319s.
- `XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' go test ./internal/web/service -run '^TestPortableRestoration_Postgres$' -count=1 -v`:
  PASS, 6.321s, all six groups ran; no PostgreSQL skip.
- `go test ./internal/web/controller -run TestImportHandlerRestartsOnlyCommittedRestorations -count=1`:
  PASS, 0.477s; a failed item's DB state rolls back while an earlier successful
  item retains its restart flag and is counted once.
- `golangci-lint run ./internal/web/service/... ./internal/web/controller/...`:
  0 issues.

The initial SSH fixture run inside the network sandbox failed to allocate its
loopback routing bridge. The authorized isolated-network rerun reached the
intended ledger assertion failures before implementation; this environment
failure is not counted as a functional RED. A first test compile used pointer
dereferences on value records and was corrected before the meaningful RED run.

The operation now holds sorted inbound locks, prepares every attachment, checks
identity inside the serialized writer and commits all attachments, counters,
HWID and group baselines together. Runtime dispatch follows commit and stays
outside the database writer. Normal AddInboundClient reuses the same separated
preparation/persistence/apply phases. Per-client transactions replace the old
inbound batch import; large-import performance has not yet been measured.
Portable policy rates, non-1x charged history, exact-string payloads and a
consistent export snapshot remain the next increment.


Full Go regression for the atomic-restore increment:
`XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' XRAY_E2E_BINARY=/tmp/3x-ui-xray-pinned go test ./... -count=1`
exited 0: 50 packages passed, six packages had no tests. Database package
58.621s, service package 109.401s, controller package 6.083s. This includes the
existing real SSH/Xray paths; a new real-client portable-policy round trip has
not yet been added. Optional tests gated by other environment variables (such
as scale and externally configured database commit-failure tests) remain
conditional; package success does not convert their skips into executed tests.


Focused race regression with the same PostgreSQL DSN:
`go test -race ./internal/web/service ./internal/web/controller -run '^Test(PortableRestoration_Postgres|Import|ExportImport|BulkCreate|AddInboundClient)' -count=1`
PASS, service 34.584s and controller 7.606s. `make gen-check` passes after
regenerating the API description; frontend and docs OpenAPI files match.


Frontend API-description validation: `npm run typecheck`, `npm run lint`,
`npm run format:check` (724 files) and `npm run build` all pass; Vite 2.67s.
No frontend behavior or schema changed in this increment. The full frontend
suite's prior 181-file/1769-test result is recorded above, not rerun or counted
as a fresh result for this documentation-only frontend edit.

`npm --prefix docs run typecheck`, `go build -o /tmp/3x-ui-portable-panel .`
and whole-repository `golangci-lint run` pass (0 issues). No schema migration
is required: this change orders writes to existing client, traffic and ledger
tables. The generated OpenAPI copy and MDX reference describe the new per-item
rollback/skip behavior.


### Portable policy format and exact history (2026-09-28)

This increment extends the prior atomic importer with version-1 managed policy
snapshots and decimal-string byte fields. Meaningful RED runs reproduced lost
rates/multiplier/carry and traffic quota, browser rounding above 2^53, silent
policy loss at ordinary create/bulk-create, and failure when importing over a
deleted client's retained traffic. The large attached-client test then caught a
second precision loss inside the inbound settings map decoder: quota
9007199254741013 became 9007199254741012. `UseNumber` now preserves both incoming
and existing settings while preparing client additions. Logs are
`/tmp/3x-ui-portable-policy-red.log`, `/tmp/3x-ui-portable-create-red.log`,
`/tmp/3x-ui-portable-large-attached-red.log` and
`/tmp/3x-ui-portable-retained-red.log`. An API contract check also
caught unattached `inboundIds: null`; export now emits the required empty array
(`/tmp/3x-ui-portable-orphan-schema-red.log`).

The concurrent export test holds the client read while actual quota and ledger
writes commit, then requires every exported value to belong to the original
snapshot. Removing the snapshot reproduced mixed states. SQLite's first
read-transaction implementation blocked accounting for 20.06s with `database is
locked`: the driver ignores `ReadOnly` and selects the writer DSN's immediate
transaction. A dedicated deferred connection fixed locking; reusing its GORM
query state exposed `no such column: client_id`, fixed by a fresh session. The
final SQLite and PostgreSQL tests require the concurrent writes to commit while
the reader remains open. No deadline was enlarged. Failed and passing logs use
`/tmp/3x-ui-portable-policy-focused*.log`; the deliberate mutation log is
`/tmp/3x-ui-portable-snapshot-mutation.log`.

Thirteen invalid-snapshot cases check version, scope, rate, multiplier, billed
bytes, carry, projection quota and required traffic/SSH fields with no partial
client or binding. Temporarily bypassing policy validation made eleven cases
fail (the two separate required-field guards still rejected their inputs), then
the code was restored: `/tmp/3x-ui-portable-validation-mutation.log`. Independent
manual/traffic disable and canonical/projection expiry survive restoration.
Deleted-owner traffic is replaceable, but a report from its old meter fails and
cannot charge the newly created policy identity.

Actual OpenSSH 9.6p1 → panel-managed pinned Xray 26.9.9Custom → loopback target
runs on both SQLite and PostgreSQL. The target independently observes 2 upload
and 3 download bytes; 1.5x billing records 7 whole bytes plus carry 500. Export,
delete with retained traffic and import preserve a traffic quota of 8. New SSH
sessions establish signed public-key authentication but are closed before any
forwarding, both after reconciliation and after backend stop/restart. The target
retains exactly one connection and 2/3 bytes. A separate admission check reports
`UsageQuotaError`. Adding 1000 bytes of quota permits the next exchange, yielding
raw 4/6 and exactly 15 billed bytes with zero carry and a fresh policy identity.
This test verifies restored rate fields, not a new throughput measurement; live
aggregate shaping evidence remains the separately recorded real-client tests.

The first new probe incorrectly required OpenSSH's authentication-rejection text.
Source inspection confirmed policy admission follows signed authentication; the
probe now requires successful authentication, exit 255, remote closure before
its five-second deadline, zero response bytes and unchanged independent target
counters. It does not accept a timeout or arbitrary connection failure as denial.
A deliberate bypass of policy restoration then returned actual payload `xab`
and made the probe fail, proving it detects quota bypass. Source was restored in
`finally`; `/tmp/3x-ui-portable-real-ssh-mutation.log` records that expected failure.

Focused command with actual PostgreSQL and Xray environment variables:
`go test ./internal/web/service ./internal/web/controller -run '^Test(Portable|Import|ExportImport|BulkCreate|AddInboundClient)' -count=1 -v`
passed: service 27.017s, controller 4.273s, no skipped tests in this run.
`/tmp/3x-ui-portable-policy-focused-final.log` records both real-SSH cases and
both portable PostgreSQL groups. Full regression/static/build evidence follows.


Fresh full Go command for this increment:
`GOFLAGS=-p=1 XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' XRAY_E2E_BINARY=/tmp/3x-ui-xray-pinned go test -json -shuffle=on ./... -count=1`
exited 0: 50 packages passed, six packages had no tests (including the existing
Go source directory under a frontend dependency). Database 48.355s, service
124.364s, controller 5.561s. The 29 skipped test/subtest cases are recorded in
`/tmp/3x-ui-portable-policy-full-go-skips.txt`, separately from passes. They need
scale opt-in, the alternate global PostgreSQL test configuration, unavailable
geosite fixtures, or a non-Linux platform. Both new real-SSH cases and the
portable PostgreSQL groups ran. Complete output:
`/tmp/3x-ui-portable-policy-full-go.jsonl`.


Focused race command with the same live PostgreSQL DSN and pinned Xray binary:
`go test -race -shuffle=on ./internal/web/service ./internal/web/controller -run '^Test(Portable|Import|ExportImport|BulkCreate|AddInboundClient)' -count=1`
passed: service 77.830s, controller 7.629s, exit 0
(`/tmp/3x-ui-portable-policy-race.log`).


Final static/build checks all exit 0: whole-repository `golangci-lint run`
(0 issues), `make gen-check` (65 schemas; 185 paths/197 operations), frontend
`npm run typecheck`, `npm run lint`, `npm run format:check` (724 files) and
`npm test -- src/test/generated-examples.test.ts src/test/openapi-runtime-contracts.test.ts src/test/openapi-request-bodies.test.ts`
(3 files/75 tests passed). Frontend production build passed in 2.87s; docs
`npm run typecheck` passed; `go build -o /tmp/3x-ui-portable-policy-panel .`
produced the panel embedding that build. Generated frontend/docs OpenAPI files
match. Logs use `/tmp/3x-ui-portable-policy-` followed by `go-lint`, `gen-check`,
`frontend-types`, `frontend-lint`, `frontend-format`, `frontend-contracts`,
`frontend-build`, `docs-types` or `go-build`, then `.log`.

Only generated frontend contracts and API descriptions changed; no frontend
interaction changed. The previously recorded 181-file/1769-test frontend suite
was not repeated and is not presented as a fresh result. No DB schema migration
is required. Large export/import throughput, complete distributed restoration,
other policy executors, SSH upstream and the remaining protocol/deployment/A–E
requirements stay open. This increment is not whole-task completion.

### SSH upstream connector backend (2026-09-28)

This is a tested internal TCP connector, not a selectable public outbound yet.
The private SOCKS bridge, Runtime configuration/rollback, editor/API/probes,
backup/node/deployment integration and Xray-to-upstream policy acceptance remain
open. No new database field, dependency version or production daemon was added.

Environment: Linux arm64, Go 1.27.1, pinned `golang.org/x/crypto` v0.57.0;
OpenSSH server `9.6p1 Ubuntu-3ubuntu13.19` with OpenSSL 3.0.13. The real-server
fixture uses an independent process group, loopback ephemeral port and generated
host/client keys under a private home-directory temporary folder. It retains
StrictModes and permits only public-key TCP forwarding, with no sessions, PTY,
agent or password access. It does not change the host management sshd or read
its keys. `SSH_E2E_SERVER` must identify an installed sshd; the Linux fixture
requires root privilege separation and an available IPv6 loopback. Without the
server environment variable, those tests explicitly skip, not pass.

The first real-server test failed against an unimplemented connector
(`/tmp/3x-ui-ssh-upstream-connector-red.log`). The first fixture under `/tmp` was
rejected by sshd's parent-directory permission check; moving its files into a
private home-directory temporary folder fixed the fixture without disabling
StrictModes. A later test caught double-closing raw and SSH transports, returning
`use of closed network connection`; connection Close now closes the SSH client
once and releases capacity once. Both failures remain in the local
`connector-green` and `close-red` logs under the same prefix.

Real OpenSSH forwarding now delivers exactly 43008 request bytes to an
independent IPv4 target reached by hostname, and exactly 43008 echo bytes back,
including TCP half-close. Wrong host pins and client keys open no target
connection. A separate IPv6 target observes `IPv6 request` and returns
`IPv6 response` through OpenSSH. No shell channel is used. Go SSH wire peers
verify original mixed-case/trailing-dot domain, IPv4 and IPv6 target strings;
canceling a stalled channel leaves an unrelated stream usable; closing the
connector revokes pending and established transports. At 128 live connections
the next is rejected, one close releases one slot, and repeated close cannot
release a second slot. A 100ms caller deadline stops an upstream that never
sends an SSH banner, with independent observation of TCP closure. Encrypted
private keys authenticate, while a wrong passphrase returns a non-secret error.

Three deadline regressions were reproduced before fixing them: a 4 MiB write
blocked on the exhausted SSH channel window survived its 100ms write deadline;
a timed-out read returned EOF; and an idle expired deadline irreversibly closed
the underlying transport. Logs: `write-deadline-red` and `read-deadline-red`.
The wrapper now tracks active application operations and closes only that
stream's dedicated transport on timeout, returning `os.ErrDeadlineExceeded`.
This wakes SSH window waiters as well as kernel I/O. Active timeouts are fatal
to that stream; an idle expired deadline can be cleared. Updating a deadline
during an observed ongoing write also interrupts it. Eight concurrent writers
send 1048576 total bytes, with exactly 131072 bytes belonging to each writer;
the wrapper serializes channel writes and half-close.

A multi-key upstream reproduced a separate interoperability bug: the default
SSH algorithm order selected a different host key from the administrator's pin.
The new test failed with a host-key mismatch despite the correct key being
available (`host-selection-red`). Negotiation now selects the pin's algorithm;
RSA pins allow SHA-512/SHA-256 signatures and the test peer offers SHA-256 only.
Host certificates and unsupported/insecure host-key algorithms are rejected.

Eleven deliberate mutations each produced the intended behavioral failure:
remove host-pin rejection; allow an extra connection; omit cancellation closure;
omit shutdown closure; bypass configuration validation; remove network or target
validation; replace the forwarded target; omit encrypted-key parsing; omit live
deadline scheduling; and remove write serialization. The last produced an
actual data race in the SSH packet buffer. Each temporary change was restored
in `finally`, and no mutation remains. Logs are
`/tmp/3x-ui-ssh-upstream-mutation-{pin,capacity,cancel,shutdown,config,network,target,preservation,encrypted,live-deadline,concurrent-write}.log`.

Final command:
`SSH_E2E_SERVER=/usr/sbin/sshd go test -race -shuffle=on ./internal/sshoutbound -count=1 -v`
passed all 16 top-level tests and their subtests in 11.057s, with no skip or race
report. Full output: `/tmp/3x-ui-ssh-upstream-race-final.log`.
Whole-repository `golangci-lint run` passed with 0 issues after applying its
De Morgan simplification to one timeout assertion; log:
`/tmp/3x-ui-ssh-upstream-lint-final.log`.

Cross-compilation of the package and portable tests also passed:
`GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/sshoutbound -o /tmp/3x-ui-ssh-upstream-windows.test.exe`
and `GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go test -c ./internal/sshoutbound -o /tmp/3x-ui-ssh-upstream-darwin.test`.
Outputs are PE32+ x86-64 and Mach-O arm64 respectively. Logs use the same prefix
with `windows-build.log` and `darwin-build.log`. These are compile checks only;
no Windows/macOS runtime test or whole-panel cross-platform build is claimed.

Only this new package and tracking documentation changed. Existing full Go,
frontend, generated-contract and database results above were not rerun or
counted as new results for this unconnected package. Its handshake-per-flow
cost, 128-flow per-connector admission and fatal active-timeout behavior are
specified in [the connector plan](ssh-upstream-connector.md). Aggregate
throughput/RAM/CPU, global outbound resource bounds, actual route selection and
single billing through the future bridge remain unverified requirements.

### Authenticated SSH upstream bridge and staged generations (2026-09-28)

The new internal bridge sends SOCKS5 CONNECT through the pinned connector. It
requires username/password authentication, keeps domain/IP targets intact, and
rejects UDP, BIND and malformed requests before any upstream connection. A
process-random HMAC secret derives generation-specific credentials; rendering
configuration opens no listener. Prepare stages new connectors beside active
ones; rollback leaves old streams and credentials usable; commit closes changed
or deleted streams and credentials while an unrelated existing stream survives.
Port conflicts and invalid settings preserve working state. Close and an empty
committed desired set release the listener; subsequent prepare restarts it.

The initial two tests failed against the unimplemented bridge
(`/tmp/3x-ui-ssh-upstream-bridge-red.log`). The first implementation exposed a test
assumption: x/net's context SOCKS dial returns a `socks.Conn`, not a TCPConn. The
fixture now uses its actual `DialWithConn` handshake on an owned TCP connection,
so half-close is tested through a real socket without asserting the wrong type.
No production behavior or expected payload was relaxed.

The actual OpenSSH 9.6p1 fixture receives `request-through-ssh` at an independent
loopback TCP target and returns `response-through-ssh` after request half-close.
Committing a syntactically valid wrong host pin makes the next SOCKS CONNECT
return connection-refused; the target connection count remains exactly one.
This verifies SOCKS → SSH traversal and no direct fallback. It does not yet
prove Xray routing or ingress accounting through the new outbound.

Capacity testing fills 512 accepted connections waiting for authentication,
observes immediate closure of the next connection, and observes capacity become
usable after one closes, with zero upstream accepts. A correctly authenticated
client that never supplies its CONNECT request is closed by the five-second
negotiation deadline, before the independent six-second observation timeout.
The parser rejects unsupported envelope/settings fields, invalid keys/tags,
duplicate SSH/native tags and more than 32 SSH outbounds. These limits apply to
this managed bridge; measured aggregate memory/throughput remains open.

Eleven mutations each caused the expected behavioral failure and were restored:
password verification, no-auth rejection, rollback selection, generation
revocation, connection cap, UDP rejection, strict unknown fields, duplicate tags,
outbound count, handshake deadline and automatic port fallback. Logs:
`/tmp/3x-ui-ssh-bridge-mutation-{auth,noauth,rollback,revocation,capacity,udp,unknown-fields,duplicate,outbound-capacity,handshake-deadline,port-fallback}.log`.
The real-server bridge run separately passed in 1.540s under the race detector
(`/tmp/3x-ui-ssh-upstream-real-bridge.log`).

Final complete package command:
`SSH_E2E_SERVER=/usr/sbin/sshd go test -race -shuffle=on ./internal/sshoutbound -count=1 -v`
passed 26 top-level tests and their subtests in 16.460s, with no skips or races
(`/tmp/3x-ui-ssh-upstream-bridge-race-final.log`). Whole Go static analysis passed
with 0 issues after replacing the production listener call with
`net.ListenConfig.Listen(context.Background(), ...)` as required by `noctx`;
log `/tmp/3x-ui-ssh-upstream-bridge-lint-final.log`.

The implementation and tests remain internal; the next service-layer RED test
still reports the current Xray validator's `unknown config id: ssh`. Saving,
previewing and applying SSH outbounds through the panel is Task 2, followed by
editor/probe/node/backup/deployment and full policy acceptance. Those requirements
remain incomplete. No new database migration or frontend contract is involved
in this internal bridge increment; previous full-suite results are not counted
as fresh evidence for it.

Updated package/test cross-compilation also passed for Windows amd64 and macOS
arm64 with `CGO_ENABLED=0 go test -c ./internal/sshoutbound`, producing PE32+ and
Mach-O artifacts under `/tmp/3x-ui-ssh-bridge-{windows.test.exe,darwin.test}`.
Logs `/tmp/3x-ui-ssh-bridge-{windows,darwin}-build.log` are empty on success.
These are compile checks, not runtime or whole-panel platform acceptance.

## SSH upstream settings, Runtime and double-SSH policy increment (2026-09-28)

The existing settings service now accepts authored SSH outbounds and compiles
only the core-facing representation to authenticated loopback SOCKS. Preview
stays independent of applied SSH ingress/outbound listeners. This is backend
integration; the dedicated editor/probes, node application and deployment
acceptance remain open. See `ssh-upstream-runtime.md` for capability boundaries.

Real service test `TestSSHOutboundRunsThroughProductionXray` uses the pinned
Xray 26.9.9Custom binary and isolated OpenSSH 9.6p1 server. Two independently
selected SSH tags and a native redirect reach observable targets; a higher
priority block reaches neither. Wrong pins and a stopped upstream reach neither
an upstream target nor a direct fallback. Pin edits revoke changed streams while
unchanged SSH/native streams survive hot application. Bridge port collision,
invalid core configuration, stop/restart and SIGKILL are exercised. A one-shot
wrapper exits 42 only for the next real child start (preflight still invokes the
actual core), proving startup failure restores the previous core/config and old
SSH generation. The same fault while removing the last upstream must preserve
its old generation. Previewing SSH then activating a native-only/API-less config
must not create a bridge or impose the SSH readiness requirement.

Named RED observations before their corresponding fixes:

- Save rejected valid SSH as an unknown native core protocol; log
  `/tmp/3x-ui-ssh-upstream-preview-red.log`.
- Pure configuration preview closed an existing SSH flow with EOF. Separating
  build/apply fixed it; `/tmp/3x-ui-ssh-preview-live-red.log`.
- Runtime route selected an unstarted bridge and reset the first request;
  `/tmp/3x-ui-ssh-upstream-runtime-red.log`.
- Graceful bridge retirement let the core retain the client's upload half,
  causing a one-second read timeout. Explicit retirement now resets private
  sockets while ordinary completion retains half-close.
- SIGKILL left the private listener bound until the watcher was added;
  `/tmp/3x-ui-ssh-upstream-crash-red.log`.
- An invalid replacement core and an immediately failed child start each returned
  nil; installed-core preflight and API readiness/recovery fix those separate
  cases (`ssh-upstream-invalid-core-red.log`, `ssh-upstream-startup-red.log`).
- Removing the final upstream skipped readiness and committed deletion despite
  failed child startup; `/tmp/3x-ui-ssh-upstream-remove-red.log`.
- A core snapshot changed before ingress commit caused EOF; explicitly staged
  fingerprint acceptance fixes the window without holding the ingress mutex over
  slow core I/O (`/tmp/3x-ui-ssh-pending-generation-red.log`).
- An invalid bridge-port environment value returned an error but omitted the
  held-back status; `/tmp/3x-ui-ssh-build-heldback-red.log`.
- The inherited restart path ignored a Stop timeout and proceeded with another
  core. A real SIGKILL with a deliberately held crash callback reproduced the
  unfinished lifecycle (`/tmp/3x-ui-ssh-core-stop-red.log`). Replacement is now
  refused until that lifecycle finishes, with a held-back stop reason.

Eight deliberate mutations produced the expected behavioral failures, then were
restored: TCP-reset revocation, core preflight, startup readiness, previous-core
recovery, crash cleanup, prepare/bind conflict, unused-preview capability
isolation, and replacing the compiled SSH exit with direct freedom. The last
mutation ran the real lifecycle/accounting test and failed because the independent
sshd observed zero authenticated upstream connections. Script
`/tmp/3x-ui-ssh-runtime-mutations.py`; logs
`/tmp/3x-ui-ssh-runtime-mutation-{name}.log`. The initial end-to-end FIN mutation
survived once due to close timing; a Linux idle-bridge ECONNRESET contract test
now catches it directly. The successful run does not conceal that initial survivor.

### Policy execution through an actual SSH upstream

The existing real ingress fixtures are reused with a native freedom redirect
chained through the SSH tag using `streamSettings.sockopt.dialerProxy`. The first
attempt used `proxySettings`; the installed core rejected that removed feature
before startup. The fixture was corrected to the pinned core's supported shape.

Actual OpenSSH clients authenticate to the managed ingress, traverse Xray and
another independent OpenSSH server, and reach independently counted targets.
Two clients sharing loopback source IP each use two inbound listeners and four
channels. The upstream observes exactly 8 authenticated transports before restart
and 8 after restart. No rate test opens a direct target socket as its client path.

The final focused race run used:

```sh
SSH_E2E_SERVER=/usr/sbin/sshd \
XRAY_E2E_BINARY=/tmp/3x-ui-xray-pinned \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
go test -race -shuffle=on ./internal/web/service \
  -run '^TestSSH(Upstream.*|Outbound.*|ConfigPreviewKeepsExistingFlow|PrepareFailureKeepsPriorAuthenticationState|InboundRunsThroughProductionXrayLifecycle)$' \
  -count=1 -timeout=180s -v
```

It passed in 75.293s with no skipped tests or race report. Log
`/tmp/3x-ui-ssh-runtime-bothdb-race-final.log`. SQLite unlimited baseline was
1,471,828 upload / 1,349,175 download B/s; PostgreSQL 1,709,385 / 1,587,286 B/s.
Targets were 32/64/128 KiB/s with the predeclared 80% floor and
`rate × seconds × 1.06 + min(65536,max(1,rate/10))` ceiling. Same-channel live
windows and restarted policies stayed within those unchanged bounds.

The first race run passed PostgreSQL but reported 2.043788597s for one SQLite
live-edit test despite all rates being within bounds. Its timing added a full
350 ms sleep after UpdatePolicy returned to a 1.5 s measurement. The corrected test
schedules the observation350 ms after edit invocation, keeps the same 1.5 s window,
and checks its end against the unchanged 2 s deadline. It does not loosen a rate
bound or the deadline. Final windows ended 1.8507–1.8519 s after invocation; writes
completed in 7.6–13.4 ms. The initial failed run is preserved in
`/tmp/3x-ui-ssh-runtime-bothdb-race.log`.

The lifecycle fixture independently checks the original 16-byte request and echo:
16 upload + 16 download = 32 billed bytes at 1x, then reset accounting, quota
reduction, credentials, disable/enable and unrelated-client isolation. Its new
upstream wrapper observes 9 real authenticated transports. At 2x, the duplex test
checks persisted billed bytes equal twice the admitted raw upload+download while
raw shaping rates remain unchanged. These are narrow real policy proofs, not
completion of every whole-task A–E workload or every protocol executor.

### Regression and build checks for this increment

The full repository command used the same real binaries and isolated
`XUI_TEST_PG_DSN`, plus `GOFLAGS=-p=1 go test -json ./... -count=1`. It exited 0:
51 test packages passed, 7 packages had no test files, and 29 conditional tests
were skipped. The service package took 187.394s. Skips are not passes: 16 opt-in
scale tests, 7 older tests requiring global `XUI_DB_TYPE`/`XUI_DB_DSN`, 5 missing
geodata cases, and 1 non-Linux update guard. New SSH upstream PostgreSQL cases
use `XUI_TEST_PG_DSN` and ran. Complete output and classified skips are in
`/tmp/3x-ui-ssh-runtime-all-go.jsonl` and `/tmp/3x-ui-ssh-runtime-go-skips.json`.

After the full regression, the final removal/re-add case and build-error status
were verified with the affected runtime tests under `-race`: SQLite and
PostgreSQL passed, service 17.247s, bridge reset 1.068s, no skips/race report.
`/tmp/3x-ui-ssh-runtime-final-cases.log` records that run. The real deletion case
closes the removed SSH stream, releases the private bridge port, rejects the
removed routing tag, and preserves an existing native stream across removal
and re-addition.

The first whole-repository lint run identified three `noctx` violations in the
shared sshd test helper extracted from an `_test.go` file. Its listener, child
process and readiness dial now use the test context. The final
`golangci-lint run` reports 0 issues; `go build ./...` exits 0. Logs:
`/tmp/3x-ui-ssh-runtime-lint-final.log` and `/tmp/3x-ui-ssh-runtime-build.log`.
`golangci-lint fmt --diff` and local documentation link checks also pass.

SSH package/test compilation with `CGO_ENABLED=0` succeeds for Windows/amd64 and
Darwin/arm64. Logs `/tmp/3x-ui-ssh-runtime-{windows,darwin}-build.log` are empty
on success. These are compile checks, not runtime tests or whole-panel cross
builds. This increment changes no frontend source, public DTO, route registry or
DB schema; it does not claim a fresh frontend suite, generated contract change
or migration. Final shared-fixture verification follows below.

After adding context ownership to the shared sshd fixture, the complete SSH
upstream package passed `go test -race -shuffle=on ./internal/sshoutbound
-count=1 -v`: 27 top-level tests plus subtests, 16.764s, no skipped tests or race
report (`/tmp/3x-ui-ssh-runtime-bridge-race-final.log`). Final SQLite/PostgreSQL
runtime race verification, including the additional unconfirmed-stop case,
passed in 20.866s with no skips/race report
(`/tmp/3x-ui-ssh-runtime-service-final.log`). The stop guard also touches the
existing generic restart path, so a final complete service regression follows.

The final complete service regression after the stop-error guard passed:
`go test -shuffle=on ./internal/web/service -count=1 -timeout=10m`, with the same
actual binaries and isolated PostgreSQL DSN, 184.581s
(`/tmp/3x-ui-ssh-runtime-service-regression.log`). This supplements the earlier
whole-repository run; no test tolerance was changed to accommodate a runtime
failure. Final lint/build are refreshed against the stop guard before commit.


## SSH upstream probe service increment (after 3785fbfd)

The existing outbound testing service now owns a temporary SSH bridge per batch,
compiles SSH outbounds before starting the temporary core, and runs actual HTTP
requests through that route. It does not borrow the applied runtime manager or
modify saved settings. This is service/data-path acceptance; browser/API
authorization, editor, backup/node and deployment acceptance remain outstanding.

### Reproduced failures and behavior changes

- `TestSSHProbeUsesRealCoreAndOpenSSH` initially failed with the installed Xray
  reporting unknown protocol `ssh`; TCP mode returned no testable endpoint.
  After compilation through the independent bridge, real/http/tcp modes reach an
  HTTP target through an independently logged OpenSSH authentication. Real mode
  sends one request; http/tcp send a cold and warm request on one SSH transport.
- A wrong requested pin with a valid older context entry initially returned
  success and reached the target. Requested settings now override same-tag
  context; the wrong pin returns failure without a target request. An invalid
  sibling initially poisoned every isolated retry. Configuration selection now
  keeps only requested roots and transitive proxy-chain dependencies, preserving
  duplicate entries for normal validation. A valid sibling succeeds while the
  malformed one reports a generic error without its private-key sentinel.
- Case variants `SSH` and `Ssh` initially bypassed the panel adapter and failed
  in the native core loader, both in save/preview and in real probes. The shared
  SSH parser and settings validator now match protocol IDs case-insensitively;
  strict settings validation and canonical compiled SOCKS output remain intact.
- A real native SOCKS proxy, an intermediate freedom dialer and the SSH dependency
  form a three-outbound chain. A separate actual Xray hosts the native SOCKS
  endpoint; one independent OpenSSH authentication and one HTTP target request
  prove that the chain was retained and traversed. Unrelated invalid context is
  excluded. An initial test cleanup assertion incorrectly included that external
  fixture's live listener; it now checks only the probe-owned inbounds and SSH
  bridge. No production cleanup rule was weakened.
- Missing core executable, rejected core configuration and stopped upstream all
  fail without target access. Captured temporary core configs contain no upstream
  private keys; owned probe listeners can be rebound after success/failure and
  temporary JSON files are removed. The normal core writer supplies mode 0600.
- The existing real runtime test invokes a wrong-pin probe while two SSH flows
  and a native flow are open. Target counters remain unchanged, the applied core
  pointer remains the same and all three existing flows still exchange payloads.

RED and intermediate logs: `/tmp/3x-ui-ssh-probe-red.log`,
`/tmp/3x-ui-ssh-probe-context-red.log`, `/tmp/3x-ui-ssh-probe-case-red.log`,
`/tmp/3x-ui-ssh-settings-case-red.log` and `ssh-probe-package.log`. The first
combined case run also found a test variable-name collision at compile time;
that was fixed and the settings case was rerun to observe its behavioral RED.
The complete outbound package then passed in 1.543s before the added case checks.

Four temporary mutations were restored and each was rejected by an actual
behavioral test: omit bridge cleanup (owned port remains bound), replace SSH with
freedom (no upstream authentication), retain stale context (wrong pin reaches the
target), and omit chain dependencies (real native proxy chain fails). Script:
`/tmp/3x-ui-ssh-probe-mutations.py`; logs:
`/tmp/3x-ui-ssh-probe-mutation-{cleanup,direct-bypass,stale-pin,chain}.log`.

The initial focused race run passed the probe cases in 3.144s and SQLite runtime
plus save/preview cases in 11.637s. Its combined `-run` expression did not select
the PostgreSQL subtest or SSH package tests; the latter explicitly reported
`[no tests to run]`. Neither is counted as executed coverage in that run.
Subsequent full regression and correctly selected race commands are recorded below.


### Final regression for the probe increment

With `XRAY_E2E_BINARY=/tmp/3x-ui-xray-pinned`,
`SSH_E2E_SERVER=/usr/sbin/sshd` and the isolated `XUI_TEST_PG_DSN`,
`GOFLAGS=-p=1 go test -json ./... -count=1` exited 0: 51 test packages passed,
7 packages had no tests, and 29 conditional test cases skipped. The service
package passed in 192.451s. The PostgreSQL SSH subtests ran: runtime 8.40s,
rates 13.55s and quota 9.37s. Skips remain 16 opt-in scale cases, 7 older global
PostgreSQL-environment cases, 5 missing-geodata cases and one non-Linux update
guard. They are not counted as passed tests. Output and exact skip records:
`/tmp/3x-ui-ssh-probe-all-go.jsonl`, `/tmp/3x-ui-ssh-probe-go-skips.json`.


With the same actual binaries, `GOFLAGS=-p=1 go test -race
./internal/web/service/outbound ./internal/sshoutbound -count=1 -shuffle=on -v`
passed: outbound 3.094s, SSH bridge 16.551s, no race report. The outbound package's
older `TestAddTrafficReturnsDeferredCommitFailure` skipped because its separate
global PostgreSQL environment was absent; every new SSH probe case ran. Log:
`/tmp/3x-ui-ssh-probe-package-race.log`.

The correctly selected PostgreSQL check, `go test -race ./internal/web/service
-run '^TestSSHUpstream_Postgres$/^runtime$' -count=1 -v`, passed in 10.149s,
including the actual runtime subtest (8.92s), with no skip/race report.
`/tmp/3x-ui-ssh-probe-postgres-race.log` records the explicit parent/subtest names.
Whole-repository `golangci-lint run` reports 0 issues
(`/tmp/3x-ui-ssh-probe-lint.log`). No frontend source, public DTO, new route or
DB schema changed, so this increment does not claim fresh frontend/browser,
contract generation or migration checks.

`go build ./...` also exited 0 (`/tmp/3x-ui-ssh-probe-build.log`). Changed Go files pass `golangci-lint fmt --diff`; local documentation links and `git diff --check` pass.

## SSH outbound editor and browser acceptance

The editor increment adds the typed SSH registry/schema, defaults, form adapter,
dedicated connection fields and routed-probe UI selection to existing Outbounds.
Observed REDs preceded the implementation: absent protocol/schema/fields; URL,
wildcard and host-with-port acceptance; unsupported SSH JSON silently discarded;
UTF-8 tag byte overflow; private-key reveal surviving an editor-target change;
missing endpoint display; SSH dispatched through the TCP probe lane. Logs are
`/tmp/3x-ui-ssh-outbound-{form,modal,validation,modal-tag,editor-switch,row,hook}-red.log`.
The first editor-switch test accidentally remounted its provider and could not
detect retained reveal state. A stateful wrapper now changes the target without
remounting the form; that test failed before the keyed field reset was added.

Focused schema/adapter/modal/hook regression: 5 files, 87 tests passed in 15.56s
(`/tmp/3x-ui-ssh-outbound-focused2.log`). Full `npm test -- --maxWorkers=1`:
**183 files, 1810 tests passed**, no skipped test in the summary, 440.77s
(`/tmp/3x-ui-ssh-outbound-full-frontend.log`). `npm run typecheck`, `npm run lint`
and `npm run build` exited 0; the build ran the existing OpenAPI generator and
left generated contracts unchanged. No public route, DTO or DB schema changed.
The protocol form is local to the outbound editor; it introduces no shared
component requiring a separate Storybook story. Existing Node DEP0205 and
Vitest plugin-hook notices remain in the test log.

`go build -o /tmp/3x-ui-ssh-outbound-panel .` built a fresh panel with those assets.
Affected Go regression `go test ./internal/web/controller ./internal/web/locale
-count=1` passed the controller package in 5.669s; locale has no Go test files.
Translation key usage is covered by the complete frontend suite. This frontend
increment does not claim a new full-Go or race run beyond the preceding probe
increment's evidence.

Reproduce the browser/data-path acceptance from `frontend/` on isolated Linux
with Chromium installed, OpenSSH available and a freshly built panel:

```sh
XUI_E2E_PANEL=/path/to/fresh/panel \
XRAY_E2E_BINARY=/path/to/pinned/xray \
SSH_E2E_SERVER=/usr/sbin/sshd \
XUI_E2E_SCREENSHOT=/tmp/ssh-outbound-editor.png \
node scripts/ssh-outbound-e2e.mjs
```

The fixture currently requires root for its independent OpenSSH test account.
It owns a temporary SQLite database, independent Ed25519 keys, loopback ports,
process groups and echo target; it never edits host sshd or reads the Git key.
The real browser probe defaults to public `https://example.com` (override via
`XUI_E2E_TEST_URL` with another reachable public URL). Public-URL safety remains
enabled. Cleanup removes only fixture processes/files; credentials are redacted
from failure text and textareas/password inputs are masked in screenshots.

Executed assertions:

- Actual Chromium creates an SSH outbound, hides its PEM, saves the dialog/list
  and reloads. The admin API returns the exact authored key and host pin.
- An existing routing rule is added through the authenticated settings API.
  Actual Xray SOCKS → managed bridge → OpenSSH reaches the independent echo
  target; the target's exact bytes and sshd authentication log prove traversal.
  The compiled core config contains no upstream private key.
- Clicking **Check** while TCP mode is selected sends an HTTP probe. The response
  is mode `http`, status 200, with an independently observed SSH authentication.
- Browser pin edits reject traffic without direct fallback; restoring the pin
  restores the actual route. Reopened key fields start hidden.
- Anonymous template access returns 404. Valid monitor/node-sync tokens first
  access status successfully, then receive 403 for authored template read/write,
  probes, compiled configuration and full database backup.
- Admin downloads a real SQLite backup, changes to a wrong pin and proves
  refusal, imports the backup, waits for the panel's restart and logs in again.
  Restored credentials establish a fresh SSH connector and reach the target.

The complete run reports 3 echo connections, exactly 186 payload bytes,
6 upstream authentications, HTTP 200, anonymous 404, both restricted scopes
denied and `backupRestored: true`; there are no browser JavaScript errors.
Final log: `/tmp/3x-ui-ssh-outbound-browser-final.log`. Screenshot review found
mask rectangles moving during the modal animation; the fixture now waits for
finite animations and hides textarea/password glyphs before capture as well.
Initial browser harness runs failed
because the route was incorrectly written as `xray#outbounds` and the exact
button-name selector omitted Ant Design's accessible icon name. The harness was
corrected to `/panel/outbound` and its actual button label; these failed runs
are not counted as acceptance.

At this editor commit PostgreSQL outbound restoration was still open; the next
acceptance extension below closes that scenario. Automatic node-owned bridge
distribution, continuous upstream health, packaging and the remaining full-task
protocol/policy acceptance remain open.

### PostgreSQL backup and browser acceptance extension

The same script now optionally accepts `XUI_E2E_PG_DSN`, a PostgreSQL URL for an
isolated local test instance, with `psql`, `pg_dump` and `pg_restore` on `PATH`.
Its test account must be permitted to create databases. The script creates a
random `xui_ssh_ui_...` database and points the panel at that database; it uses
the supplied administrative database only to create/drop the owned fixture.
Connection credentials travel through environment variables, never psql command
arguments, and failure text redacts the URL/password. Cleanup drops only the
newly created database, on successful acceptance and failed fixture startup.

Executed against PostgreSQL/client tools **16.15**, using the task-local server
at `127.0.0.1:55432`. The downloaded backup has the native `PGDMP` signature;
the existing import API restores it after a wrong-pin refusal. After panel
restart and fresh login, the exact authored key/pin and actual route are restored.
All other browser, probe, target-count and authorization assertions run unchanged.
Result: 3 target connections, 186 bytes, 6 SSH authentications, HTTP 200,
anonymous 404, monitor/node-sync 403 and `backupRestored: true`,
`database: "postgres"`. Log: `/tmp/3x-ui-ssh-outbound-postgres-browser.log`.

The extended script's default SQLite path also passed all assertions with the
same independent counts (`/tmp/3x-ui-ssh-outbound-sqlite-browser2.log`). PostgreSQL
catalog inspection after success found zero `xui_ssh_ui_...` databases. A deliberate
missing-panel-binary run exited 1 with the expected startup `ENOENT`, then another
catalog check found zero fixture databases, proving failure cleanup as well
(`/tmp/3x-ui-ssh-outbound-pg-cleanup-failure.log`). Script syntax/format and
`git diff --check` pass. This test/documentation extension changes no production
code and does not claim another full frontend/Go/race run.

## Local SSH runtime observation API (after 5dcb0987)

The server snapshot reports actual Serve lifetime and authenticated SSH
transports under its mutex. The service joins a pure manager snapshot to the
requesting owner's SSH metadata. It does not reconcile or instantiate a manager.
The existing inbound controller exposes `/panel/api/inbounds/ssh/status`, with
admin/session access and owner filtering; monitor and node-sync are denied.

Server tests use an owned listener and real SSH handshakes. An unsigned socket
counts zero; two authenticated transports for one client count two; opening two
real echo channels on one transport still counts two. Closing a transport,
revoking credentials, closing the server, and unexpected listener termination
clear the appropriate counts/readiness. The missing interface and then zero-value
stub failed before implementation. Full `internal/sshtunnel` race/shuffle passed
13 top-level cases, no skips/races (14.538s). Both temporary mutations (count raw
connections; use the single-start flag as readiness) failed and were restored.
Logs: `/tmp/3x-ui-ssh-status-*.log`.

The cold service test initially rejected an empty implementation; real lifecycle
status initially reported pending for an occupied port and failed its protected
assertion. The implemented service checks enabled canonical attachments and
filters owners even for user ID zero. Cold reads leave the configured port
unbound and the manager absent. A missing database table returns an error.

The extended real OpenSSH/Xray fixture on SQLite and PostgreSQL now verifies:

- Occupied listener → protected; release/reconciliation → running.
- Actual authenticated connections → one/two; key rotation removes only the
  revoked transport while another client's echo remains alive.
- Desired disable and removed attachments, committed before runtime notification,
  remain pending with the actual two transports; a read does not apply changes.
- Applied disable → disabled/zero; full-edit suspension → pending/zero; router
  exit → protected/zero; no enabled clients → idle/zero.
- Full Stop → pending/zero without restart. Explicit start restores service.
- Reopening the owned database replaces its handle. Status does not reuse old
  counts/reasons or replace the manager; explicit application restores a fresh
  listener and real echo. Deletion closes the actual flow; a new empty inbound
  on that port is idle with no inherited count, and the old ID is absent.

The first extension run failed because the fixture used automatic restart after
an explicit Stop; the existing manual-stop guard intentionally refuses that.
The fixture now invokes explicit restart, leaving runtime behavior unchanged.
Both database paths passed in 10.624s, no skips (`ssh-status-transitions2.log`).
The earlier focused both-database race run passed in 17.399s before these added
transitions; final regression results are recorded below.

The HTTP test first failed with route 404. It now exercises actual token auth,
signed session cookies for a different owner, owner/native exclusion, the exact
four-field safe payload, and failure envelopes on database errors. Both valid
restricted tokens reach their allowed server-status endpoint before receiving
403 here. The test initially used inbound-list as the monitor control, which is
intentionally forbidden; the corrected control uses server-status. API test
passed in 0.523s (`ssh-status-api-green2.log`).

Generated DTO schemas, examples and endpoint documentation follow the existing
Go→Zod/OpenAPI pipeline. This API increment does not claim inbound-list rendering,
per-client online state, remote runtime execution, or the whole SSH vertical as
complete.

Six service mutations were rejected by behavioral assertions and restored:
remove owner filtering, count disabled canonical clients, swallow database errors,
ignore the database generation, report idle while detached clients still have
live transports, and report disabled before the listener stops. The generation
mutation exposed the old manager's `client rate policy unavailable` reason;
the correct read reports pending without leaking that old state. Logs:
`/tmp/3x-ui-ssh-status-mutations.log` and `ssh-status-mutation-*.log`.

`npm run gen` generated 66 schemas and 198 operations. The OpenAPI copy was
regenerated into the docs with `npm run gen:api` (same package script; `pnpm`
was absent from this shell's PATH). Typecheck, endpoint lint/format and the
68 generated-example checks passed (Vitest 0.810s). Local documentation links
and `git diff --check` passed. No new translation keys or UI behavior are part
of this backend/API increment.

Reproduce the backend checks with the pinned Xray binary, isolated OpenSSH server
and disposable PostgreSQL DSN from the preceding sections available:

```sh
export XRAY_E2E_BINARY=/tmp/3x-ui-xray-pinned
export SSH_E2E_SERVER=/usr/sbin/sshd
# Set XUI_TEST_PG_DSN to the owned PostgreSQL test server.
GOFLAGS=-p=1 go test -shuffle=on -count=1 -json ./...
GOFLAGS=-p=1 go test -race -shuffle=on ./internal/web/service ./internal/web/controller \
  -run '^TestSSHRuntimeStatus|^TestSSHInboundRunsThroughProductionXrayLifecycle$|^TestSSHInbound_Postgres$' \
  -count=1 -v
```

Final whole-Go regression: 51 test packages passed, 7 packages had no tests,
29 conditional test cases skipped, no failed packages/cases. Service passed
in 193.923s, controller in 5.651s, SSH tunnel in 13.160s, and web/route contracts
in 0.652s. The new PostgreSQL status wrapper and both real lifecycle paths
actually ran. Log: `/tmp/3x-ui-ssh-status-full-go.jsonl`.

Final focused race/shuffle after all transition assertions and restored mutations:
service 19.440s, controller 2.980s, 6 top-level tests total, no skips/race reports.
PostgreSQL real-data-path ran in 7.11s; SQLite real-data-path ran in 5.51s.
Log: `/tmp/3x-ui-ssh-status-final-race.log`. The complete frontend suite and
real status-list browser acceptance belong to the next UI increment; they are
not claimed by the API-only change.

Final static/build checks: whole-repository `golangci-lint run` reported
0 issues; frontend `npm run typecheck` and `npm run lint` returned zero;
`npm run build` completed in 4.82s, followed by a successful `go build ./...`
against the fresh embedded assets. Endpoint formatting, identical panel/docs
OpenAPI copies and `git diff --check` passed. Logs:
`/tmp/3x-ui-ssh-status-{go-lint,typecheck-final,fe-lint,fe-build,go-build}.log`.

## SSH runtime status in the existing inbound list (after 40392e16)

The existing list now displays a localized status/count tag beside its enable
switch, in both desktop table and mobile cards. It consumes the generated DTO
through strict schema validation and an owner-filtered status query every 3s
while SSH rows exist. Errors and paused offline queries hide cached results;
missing rows and unknown states show unavailable. Negative/invalid counts are
rejected. A tooltip available by hover or keyboard focus explains authenticated
transports, shared channels and independent client quota/expiry restrictions.
EN and zh-CN are translated; all 13 locale files contain the new fallback keys.

Eight original component cases first failed because the list had no status tag
or query. The initial green run exposed a test transport stub that incorrectly
returned status rows for a list refetch; the recovery test now invalidates the
status query it is exercising. Invalid payload assertions wait for the query to
settle, avoiding a false pass against the initial unavailable placeholder.
Final focused tests: 10 status cases plus 3 existing WebSocket identity cases,
13 passed in 18.42s. Three temporary mutations were rejected and restored:
retain cache after an HTTP failure, retain it while offline, and accept negative
connection counts. Logs: `/tmp/3x-ui-ssh-status-ui-{red,green,green2,final-focused}.log`
and `ssh-status-ui-mutation-*.log`.

The real browser fixture remains `frontend/scripts/client-policy-e2e.mjs`.
Build the frontend and a fresh embedded panel before running it:

```sh
cd frontend && npm run build && cd ..
go build -o /tmp/3x-ui-ssh-status-ui-panel .
XUI_E2E_PANEL=/tmp/3x-ui-ssh-status-ui-panel \
XRAY_E2E_BINARY=/tmp/3x-ui-xray-pinned \
XUI_E2E_STATUS_SCREENSHOT=/tmp/3x-ui-ssh-status-ui-desktop.png \
node frontend/scripts/client-policy-e2e.mjs
```

The fixture creates the inbound and client through actual forms, binds its own
collision listener only after creating the empty SSH inbound, and waits for
scheduled runtime application. The enabled row shows protected with the real
listener-unavailable reason; releasing the owned port changes it to running.
A pinned OpenSSH connection carries 16384 B each way through Xray to an independent
loopback echo target. A second authenticated OpenSSH transport for the same
client changes the displayed count from 1 to 2; closing it returns the count to 1.
The desktop and 390px mobile displays are checked; the mobile document does not
overflow horizontally. Clicking the existing enable switch finally shows disabled.
No status request is mocked in the browser fixture.

The earlier policy assertions remain: actual exported host pin, wrong-pin refusal,
49152 B billed at 1.5x, saved-policy conflict/reload, billed list balance, quota
reduction below usage, and real SSH denial. The first expanded browser run passed,
with initial application/collision recovery in 24877ms and meter recovery in 655ms;
no browser JS errors. Desktop/mobile screenshots were visually inspected.
Logs: `/tmp/3x-ui-ssh-status-ui-browser.log`; screenshots:
`/tmp/3x-ui-ssh-status-ui-desktop.png` and its `.mobile.png` companion.

Typecheck and lint passed; frontend build completed in 2.65s and the fresh Go
panel build succeeded. Affected controller tests passed in 5.632s; locale had no
test files. No backend Go behavior, database schema or API route changed in this
UI increment. Complete frontend and final browser results follow below.

The final browser run additionally used Chromium's actual offline network mode:
cached running/count values became unavailable, then a fresh response restored
running/1 after reconnection. It passed with application/collision recovery
27772ms and meter recovery 284ms, no browser JS errors. Log:
`/tmp/3x-ui-ssh-status-ui-browser-final.log`. The screenshots were refreshed by
that run. The complete frontend suite is recorded separately below.

The first complete `npm test` run finished in 330.84s: 183 files passed and
1 failed; 1819 tests passed and 2 timed out. Both failures were in the unchanged
`inbound-form-modal.test.tsx` (initial add render and missing TLS certificate),
at its existing 5000ms timeout. The new status cases and locale checks passed.
A separate run of that unchanged file passed all 8 cases in 15.41s without
changing source, assertions or timeout. The complete suite was then rerun with
`npm test -- --maxWorkers=1` to check behavior without concurrent workers;
no timeout was relaxed. Logs: `ssh-status-ui-full-frontend.log`,
`ssh-status-ui-form-recheck.log`, and `ssh-status-ui-full-frontend-serial.log`
under `/tmp/3x-ui-`.

The complete single-worker run passed all 184 files and 1821 tests in 450.14s,
including unit, component and actual Chromium Storybook projects. There were no
skips or failures in its summary. This establishes the complete test result with
reduced concurrency; it does not erase the two recorded parallel-run timeouts or
claim a root cause from that comparison alone. Final format check passed all
731 source/tool files; documentation links and `git diff --check` passed.
The UI browser acceptance uses SQLite; the backend status service's PostgreSQL
lifecycle evidence remains in the preceding API increment. No new full-Go or
PostgreSQL browser run is claimed for this frontend-only change.
