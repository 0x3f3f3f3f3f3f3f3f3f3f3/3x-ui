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
state; those failed fixture runs are not acceptance passes. The old client
bulk-delete fanout remains non-atomic with candidate selection under concurrent
reset/quota edits; that separate lifecycle risk is still open.

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
