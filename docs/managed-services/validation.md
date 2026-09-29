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

## SSH client online and source IP observations (2026-09-28)

Plan: [SSH client presence](ssh-client-presence.md), within unfinished Task 5.
Initial tests failed for the missing server snapshot (compile, then an empty
snapshot stub), the absent idle OpenSSH client in the service result, the empty
existing online list, and missing IP observations when the native online RPC
was marked unsupported. A separate reproduction wrote a real temporary ban log
for two SSH sources. The implementation records those sources without passing
SSH observations to native host-wide bans. IP/device enforcement remains open.

Focused server test passed in 0.255s. The actual service lifecycle passed on
SQLite and PostgreSQL in 10.877s total (SQLite 4.90s; PostgreSQL wrapper 5.82s,
including its real data path 5.25s). It checks idle authenticated users, two
clients behind one IP, duplicate transport deduplication, detached membership,
revocation, router stop, DB handle replacement and deleted/reassigned inbound.
The real collector and no-ban regressions passed in 0.869s. The collector drives
actual Xray and SSH, marks only the core's online-RPC capability unsupported,
and verifies the existing online list, active tag, last-online timestamp, zero
raw counters, persisted local IP view and disconnect cleanup.

The fresh panel build `/tmp/3x-ui-ssh-online-panel` passed the expanded real
Chromium/OpenSSH acceptance. Before payload, the existing online/IP/last-online
APIs show the idle SSH user and actual 127.0.0.1 peer with zero raw bytes. The
existing client table displays Online. The original duplex echo still accounts
for 16384 B each direction and 49152 B billed at 1.5x; quota reduction, refusal,
offline browser recovery, transport counts and disable still pass. After
termination the user leaves the online set within the unchanged grace window.
Initial application/collision recovery took 24935ms; meter replacement 279ms.
No browser JavaScript errors. Log: `/tmp/3x-ui-ssh-online-browser-final.log`.

The first browser run reached final disconnect cleanup and then failed because
its new assertion assumed the existing empty online response was `[]`; the API
returns `null`. The fixture now handles that existing empty-set representation,
without changing production API behavior or increasing its timeout. The initial
race invocation was deliberately interrupted during compilation to serialize
real-backend checks; it is not a completed passing run. A mistaken invocation
of unavailable Prettier was canceled; the repository's installed oxfmt then
formatted the fixture and `node --check` passed. Final regression results follow.

Final focused race command:

```sh
XRAY_E2E_BINARY=/tmp/3x-ui-xray-pinned SSH_E2E_SERVER=/usr/sbin/sshd \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
go test -p 1 -race ./internal/sshtunnel ./internal/web/service ./internal/web/job \
  -run '^(TestOnlineSessions.*|TestStatus.*|TestSSHOnlineColdReadDoesNotCreateRuntime|TestSSHInboundRunsThroughProductionXrayLifecycle|TestSSHInbound_Postgres|TestSSHCollectors.*|TestSSHSourceCollection.*)$' \
  -count=1 -json
```

It passed all 9 top-level tests with zero skips/failures and no data-race report:
SSH 1.668s, service 17.217s, jobs 3.286s. The final log is
`/tmp/3x-ui-ssh-online-race-final.jsonl`. This is the affected concurrency suite,
not a claim of whole-repository race coverage.

The complete backend run used `go test -p 1 ./... -count=1 -shuffle=on -json`
with the same three real-backend environment variables. It completed with
50 test packages passing, one failing, and seven packages without tests.
There were 29 conditional test skips, including scale/configuration-dependent
PostgreSQL and golden-core cases; these are not counted as passed. Its sole
failed leaf was `TestSSHUpstream_Postgres/rates`: Xray exited before readiness,
before any rate window began. Its parent test also carries a failed event.
Log: `/tmp/3x-ui-ssh-online-full-go.jsonl`.

Only failure diagnostics were added to the existing rate fixture (the core's
last result on startup failure); no assertion, limit or timeout changed. The
same PostgreSQL rate subtest passed independently in 14.387s, including actual
live rate edits and restart. Log: `/tmp/3x-ui-ssh-online-upstream-recheck.log`.
The first run's startup failure has no established root cause. The full service
package is rechecked with its original shuffle seed `1790632389394318245`;
other packages already passed unchanged in the complete run. Results follow.

The first full service replay (194.154s) passed the original PostgreSQL upstream
rate case (13.87s), but exposed a different brittle assertion in
`TestPortablePolicyRealSSHSurvivesRestoreAndRestart`. The actual client
successfully authenticated, emitted no stdout and exited 255 without timeout;
its final message was `client_loop: send disconnect: Broken pipe`. The fixture
required the alternative read-side `closed by remote host` text. The rejected
transport returns before the newly added admitted-session marker.

The fixture now accepts either specific disconnect message. Authentication,
exit code, timeout, empty payload, independent target connection/byte counters,
typed exhausted-quota check, restart denial and successful recovery after credit
remain unchanged. This is a diagnostic-text correction, not relaxed payload or
quota acceptance. The replay is retained at
`/tmp/3x-ui-ssh-online-service-recheck.jsonl`; the initial core-startup failure's
root cause remains unestablished.

After the disconnect-text correction, both real portable-restoration cases ran
three times each: all six passed in 9.533s with no skips. They retained exact
2/3-byte payload accounting, quota 8 refusal before/after restart, and recovery
to 4/6 raw bytes and 15 billed bytes after credit. Log:
`/tmp/3x-ui-ssh-online-portable-recheck.log`.

A temporary panel binary omitted only the SSH-user append in the traffic job.
The browser fixture rejected it at `idle SSH client in the existing online
list`; original source bytes were restored immediately after the mutant build
(SHA-256 `154465880f44f3227144ca65488cd82ee09a7d6fa13f17b663dc10b5e65463cd`).
Log: `/tmp/3x-ui-ssh-online-browser-mutation.log`. The fixture's readiness wait
was then tightened to read actual authenticated transport status instead of
opening a probe connection to the local forward. Thus its idle assertions now
precede even a probe forwarding channel, preventing a native transient-online
signal from satisfying the intended idle check.

The final browser run with authentication-only readiness passed all assertions:
application/collision recovery 24895ms, meter replacement 409ms, no browser
JavaScript errors. Log: `/tmp/3x-ui-ssh-online-browser-idle-final.log`.
Affected Go lint reported `0 issues`; `make gen-check` passed with the existing
186 paths/198 operations unchanged. No frontend product source, locale, API
route or schema changed in this increment; the real browser used the existing
frontend bundle embedded in the fresh panel build. No new full frontend-suite
claim is made.

The final complete service-package run passed in 198.305s at the original
shuffle seed: 913 passing top-level tests and 13 top-level conditional skips
(1894 passing test/subtest events and 18 skip events), with no failures. Both
previously failing named cases passed. Log:
`/tmp/3x-ui-ssh-online-service-final.jsonl`. Together with the other 50 unchanged
packages from the original full run, all tested packages now have passing
results; this does not relabel the first whole-repository invocation as green
or establish a cause for its one-off startup exit. Other packages' skips and
seven packages without tests remain as recorded above. Local documentation
links and final diff whitespace checks passed.

## Native mieru data-path increment (2026-09-28)

The official Go module is pinned to v3.38.0 with the commit/checksums in
[the data-path plan](mieru-data-path.md). Tests run the official client API and
actual encrypted TCP/UDP wire engine against owned loopback listeners and the
panel's durable SQLite ledger. They do not replace protocol transport with a
mock. This increment does not launch the standalone client CLI or expose a
public mieru service in the panel.

The test path is: official client → native authenticated multiplexer → shared
policy controller → explicit test dial callback → actual TCP/UDP endpoint.
The callback preserves policy ID, inbound tag, original domain/port, network
and real source address. It is deliberately a test connector; real Xray UDP
routing, Runtime/API/UI/export/deployment/node integration remain open.

Verified behaviors and original failure evidence:

- Stream reuse initially granted only 1000 of a 6000-byte packet, forwarded a
  four-byte quota prefix, and lost empty UDP packets. Whole-packet admission
  fixes all three actual UDP cases; limiter/flow race suites passed.
- All four native underlay/payload combinations passed five repeats. Each
  same-IP user transfers 8192 bytes in each direction: 24576 billed bytes at
  1.5x and 8192 at 0.5x. Native framing and native diagnostic counters do not
  enter the ledger a second time. Native rolling quotas are left empty.
- At quota 15 and multiplier 1.5x, a six-byte upload consumes nine billed
  bytes; its six-byte reply is rejected whole. A subsequent two-byte upload
  and reply consumes the remaining six billed bytes. Existing TCP and UDP
  sessions close, new sessions and a restarted server remain denied, and the
  other user's real UDP request still works. A download-bypass mutation
  delivered the forbidden reply and was rejected by this test.
- Manual disable, malformed UDP fragments, bad passwords, explicit route
  denial and a partially occupied listen configuration are exercised over
  actual network connections. No direct fallback is provided. The bad-password
  case can consume the official API's ten-second handshake timeout.
- A native `Accept` can precede publication of its authenticated username.
  Initial real requests failed until identity lookup moved after the request
  read. A separate one-byte authenticated request showed that native `Read`
  resets its deadline; the fixed absolute handshake deadline closed it in
  5.47s, without payload billing (RED: still open at the seven-second client
  deadline).
- Backpressure exposed native `Session.Close` waiting on a blocked send lock.
  Owned TCP transport cancellation and joined asynchronous session cleanup
  fix shutdown and disable without holding the policy controller's cleanup
  loop. A deliberately synchronous-close mutation prevented target closure
  and failed the 1.25s cutoff assertion. UDP close notifications finish before
  the shared UDP listener is stopped.
- A 31-second upload-only UDP test initially changed source port after 30s:
  the target's idle deadline ignored uploads. Both traffic directions now
  refresh activity, and the test retains one target mapping with exact
  upload-only accounting.
- Concurrent native/adapter listener closes reproduced `EADDRINUSE` on
  immediate rebind: the second close returned before the first released its
  descriptor. Owned listener, packet and stream close operations now wait
  for their first completion. The collision/rate tests passed three race
  repetitions in 56.423s after the listener fix.

The mixed-rate test uses two same-IP clients, two listener transports, and four
real TCP/UDP combinations per client. The unrestricted baseline must exceed
524288 B/s in each direction (eight times the highest tested 65536 B/s cap).
Restricted traffic permits only one 4096-byte payload awaiting independent
receipt per path. The observation bound therefore includes exactly 16384 bytes
of in-flight payload per direction, in addition to the configured burst, one
4096-byte datagram debt and the predeclared 6% timing allowance:

`0.80 * R * elapsed <= observed <= 1.06 * R * elapsed + floor(R/10) + 4096 + 16384`

Initial windows last 1.8s; live-change windows start after 250ms and last 1.5s,
with the entire change check required to finish within two seconds. The two
nonzero rates are 32768 and 65536 raw B/s, independently for upload/download;
the other client keeps its existing rates. Multiplier 2x changes billing only.
Unrestricted baseline producers stop before the restricted measurements.

Earlier fixtures omitted native queued payload from their observation bound
and had both a low-window failure and a 70452-byte observation over a 69944-byte
bound. Those runs remain failures. The replacement workload bounds flight
explicitly rather than retaining unbounded queues and enlarging a percentage.
Pacing the unrestricted baseline was also rejected: it reached only
358–409 kB/s, below the required baseline. The final unrestricted baseline
remains unpaced. The corrected restricted fixture still rejects bypassed UDP
shaping: 17915904 observed bytes against an 86278-byte upper bound in 1.8s.

Reproduction commands (owned sockets and disposable databases required):

```sh
go test -race -shuffle=on -count=1 ./internal/clientpolicy ./internal/policyflow
go test -race -shuffle=1790635997657768933 -count=1 ./internal/mieru
go test -p 1 -json -shuffle=on -count=1 ./...
golangci-lint run ./internal/clientpolicy/... ./internal/policyflow/... ./internal/mieru/...
make gen-check
```

Run the native throughput package separately from other heavy tests. The full
Go command uses the pinned `XRAY_E2E_BINARY`, isolated `SSH_E2E_SERVER`, and
owned `XUI_TEST_PG_DSN` described earlier. Native adapter tests currently use
SQLite; PostgreSQL ledger/service regression is separate evidence.

Local logs are under `/tmp/3x-ui-mieru-*` and `/tmp/3x-ui-datagram-*`.
The first combined-package race run failed; the initial native-only replay
passed in 43.796s, and the next run found the listener/observation issues above.
These are not relabeled as a single clean run. Final checks are recorded below.

The complete Go regression passed with 52 tested packages, seven packages with
no test files, 2494 passing top-level tests and 18 skipped top-level tests
(4968 passing and 29 skipped test/subtest events). There were no failing tests.
It ran from 23:07:57 to 23:16:10 UTC; database 48.655s, native mieru 61.907s,
real SSH 12.521s and the full service package 194.528s. The three PostgreSQL
migration cases gated by `XUI_DB_TYPE`/`XUI_DB_DSN`, opt-in scale cases, platform
and fixture/transaction-specific skips remain skipped, not passed. This broad
run began before the final partial-handshake fix; a fresh complete native
race run follows it to verify that last code change.

`golangci-lint` reported zero issues and `make gen-check` passed with the existing
186 API paths/198 operations unchanged before that last parser edit. No API,
model migration, frontend product source or locale changed in this increment;
no new full frontend-suite claim is made. The shared datagram foundation was
committed separately as `c8e3988c378713e2618d1b9b255524fb2767a004` and pushed to
the approved fork feature branch; an independent `git ls-remote` matched it.

The final native race suite passed in 76.198s: nine top-level tests, 15
test/subtest events, no skips, failures or detected races. Log:
`/tmp/3x-ui-mieru-native-final.jsonl`. This run includes the final partial-read
deadline and synchronized transport-close changes. Its unrestricted baseline
was 735989 B/s upload and 572436 B/s download. Live-window rates were
32748–32764 B/s for the 32768 B/s cap and 65496–65528 B/s for 65536 B/s.
Existing TCP/UDP quota cutoff took 346.8/359.2ms; the stalled-read client's
target closed 52.0ms after disable. These are measured results within the
declared bounds, not new universal timing guarantees.

After those final source changes, affected Go lint again reported `0 issues`,
`go build ./...` succeeded, and `make gen-check` again preserved 186 paths and
198 operations. Documentation's local links and `git diff --check` passed.


## Pinned core UDP packet prerequisite (2026-09-28)

Native mieru commit `2f00235f51e91f97de5620be76ebd7c5fe37b03c` was pushed to the
approved fork feature branch; independent `git ls-remote` returned that exact
SHA. The following increment remains an internal routing prerequisite. It does
not activate public mieru routing or alter the installed panel core.

`tools/managed-xray` prepares the existing pinned 26.9.9 source with its module
checksum, runs `go mod verify`, applies a reviewed MPL-2.0 patch and builds a
separate binary marked `3x-ui-packets-1`. Existing source/output paths are
refused; functional checks preserved a source sentinel and the existing binary
SHA. The final test binary SHA256 is
`9d292ae3d32f64a44206ede9c39e2b9dfa2a65e953b1a5ccafa25b8f47a4fa64`.
Patch SHA256:
`5f09614959462e280048694668e5f3ebf8ea152fbf110c0de8522b4cc597f967`.
This is Linux arm64/Go 1.27.1 evidence, not all-platform packaging acceptance.

Actual TCP-authenticated Trojan UDP requests to an independent UDP socket
established 1/8170-byte controls. Stock core then lost empty uploads, failed an
8192-byte response with `buffer is full`, and rejected 8193/65507-byte uploads.
The initial fixture had failed even the one-byte control: debug logs showed the
core's default final rule blocking the loopback target. An explicit fixture-only
loopback allow rule corrected that setup; it is not a change to deployed ACLs.
Logs: `/tmp/3x-ui-core-datagram-red-corrected.log` and
`/tmp/3x-ui-core-datagram-green.log`. All six packet sizes subsequently completed
exact request/reply checks, with a further target read checking for extra split
or duplicate packets. The response source remains the core's original domain
alias; actual-IP response adaptation is still required for the mieru wrapper.

The patch allocates complete packets and complete address/length framing,
retains empty packet presence separately from payload byte counts, and charges
at least one normal buffer's capacity to each queued UDP packet. Native finite
pipe semantics retain one final whole-write admission beyond the limit. An
explicitly unlimited upstream buffer configuration is still unlimited; managed
activation must select a finite policy. The Trojan UDP handler now passes its
authenticated user's buffer policy through to the dispatcher.

Real stalled TCP protocol-outbound tests show why that policy propagation is
necessary. With an unlimited global default, stock core accepted all 33,644,544
framed bytes in both the unlimited control and configured-zero-buffer case.
The final fixture fixes only its owned Linux socket buffers, avoiding kernel
autotuning as the acceptance boundary. Patched-core repeats preserved the
unlimited control but stopped the bounded sender at 448,533 framed bytes in the
recorded final run, with the same two-second deadline and predeclared 8 MiB
ceiling. That observation includes socket flight and protocol framing; it is
not a new billing boundary, rate measurement or universal memory bound.
Logs: `/tmp/3x-ui-core-buffer-policy-controlled-red.log` and
`/tmp/3x-ui-core-bridge-package-final.log` (three full package race repetitions,
all passing, 10.856s). An earlier fixture run passed at 8,276,276 framed bytes;
its proximity to the fixed ceiling motivated explicit socket-buffer control,
not an enlarged tolerance.

Core-package RED evidence also covers empty connected UDP reads, empty packets
bypassing a zero-capacity pipe, maximum-width address framing and malformed
CRLF. Actual compile-time overlays individually removed overflow rejection,
full packet allocation and queue-capacity charging. Each corresponding test
failed: a 65536-byte length overflow wrote 65547 framed bytes, an 8193-byte
packet was truncated, or the second empty datagram bypassed the full queue.
The overlays modify only isolated test builds, not repository/module sources.
Self-review additionally caught loss of a valid final `io.Reader` payload with
EOF; its RED test passed after retaining nonempty final reads and refusing to
invent a subsequent empty packet. The first changed `IsEmpty` implementation
also failed the upstream nil-buffer regression; the final code retains nil
handling. These failures were corrected, not counted as passing runs.

The core's own full `go test -p 1 -count=1 -shuffle=on -json ./...` ran from
23:38:54Z to 23:49:23Z. It passed 80 test packages, including the 334.027s real
protocol scenario package, and failed three packages because the module archive
omits `resources/geoip.dat`/`geosite.dat`. The affected top-level failures were
`TestChinaSites`, `TestParseDomainRules`, `TestParseIPRules`, `TestIPMatcher4CN`
and `TestGeodataConfig`. Fetching the upstream CI's actual asset source at
commit `f810cb1a484824b94604872b82b1eb74ec7a43c3` and checking both published
SHA256 values made all three complete packages pass. The combined complete
package results are 83 passing packages, 560 passing top-level tests and one
skipped top-level test (`TestSockOptMark`), plus 85 packages without test files.
The initial invocation was not green. Logs:
`/tmp/3x-ui-core-full-go.jsonl`, `/tmp/3x-ui-core-geodata-rerun.jsonl`.

The final EOF-preservation edit followed that broad run. A fresh complete
`go test -race -count=1 -json ./common/buf ./transport/pipe ./proxy/trojan
./proxy/freedom` then passed all 46 top-level tests across the three packages
with tests; freedom has no package tests, and is exercised by the actual wire
tests. No test cases were skipped and no race was detected. Log:
`/tmp/3x-ui-core-patch-packages-final-2.jsonl`. The final isolated binary build
exited zero after source verification. The earlier in-progress shell script
was edited during execution and exited 127 despite emitting a binary; that
attempt is not counted as a successful build. The scripts were then finalized
and rerun with new output paths.

No public API, model, migration, frontend control or locale changed in this
increment. The packet-capable policy-ID bridge, response IP metadata, complete
route/egress tests, Runtime capability enforcement and installation/node paths
remain open in the main Task 6 and the packet bridge plan.


The final panel regression used the newly built binary for both
`XRAY_E2E_BINARY` and `XUI_MANAGED_XRAY_E2E_BINARY`, with the owned PostgreSQL and
OpenSSH fixtures enabled:

```sh
XRAY_E2E_BINARY=/tmp/3x-ui-xray-packets-verified-2 \
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-packets-verified-2 \
SSH_E2E_SERVER=/usr/sbin/sshd \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
LD_LIBRARY_PATH=/tmp/3x-ui-pg-tools/root/usr/lib/aarch64-linux-gnu \
go test -p 1 -count=1 -shuffle=on -json ./...
```

It exited zero from 23:51:56Z to 23:59:50Z on 2026-09-28: **52 test packages,
2497 top-level tests passed; 18 top-level tests skipped**, with seven packages
without test files. There were 4979 passing and 29 skipped test/subtest events,
and no failures. The skips remain explicit conditional paths, not acceptance.
This includes existing panel/SSH/core workflows and the final UDP tests against
the final core patch. Log: `/tmp/3x-ui-packet-core-panel-full-go.jsonl`.
Affected panel lint reported zero issues. No full frontend run is claimed for
this source-tooling/core/test-only increment.

Final `go build ./...`, affected lint, shell syntax and local documentation-link
checks passed. The first staged whitespace check flagged the required leading
context spaces in the unified patch file. A directory-scoped Git attribute
exempts only patch-context trailing spaces and space-before-tab; applied Go
source still passes `gofmt`, and patch application is checked before building.
All other source whitespace checks remain enabled.

## Managed authenticated routing increment (2026-09-29)

This increment keeps the same panel/core source pin and official mieru v3.38.0.
The standalone core now reports `3x-ui-managed-1`. It is not installed or selected
by public Runtime. Tests own loopback listeners, child processes and temporary
SQLite databases; the Git credential is not used by any test service.

The managed bridge's initial RED failed because its API did not exist. The first
actual old-core run then rejected user level 4294967295 as uint8. Patch 0002
widens inbound Trojan user levels and explicitly enables the private extension.
A 100-byte probe includes a reserved invalid address type and a fresh nonce;
only a versioned HMAC acknowledgement permits the later target stage. Readiness
checks never provide a target. The stock pinned core is tested with an actual
redirect outbound and cannot dispatch this probe. Wrong credentials, forged
acknowledgements and canceled peers fail closed.

Actual UDP tests preserve zero-, 1-, 8193- and 65507-byte packets and return the
socket's actual IP/port while routing on the original domain. A blackhole HTTP
response without UDP source metadata initially arrived as a fabricated
127.0.0.1:12345 peer (101 bytes). The managed server now rejects such responses
before the standard Trojan fallback to an assumed source. Ordinary inbounds
retain their existing response-address behavior. Parser tests reject domain
response addresses, zero ports, oversized lengths, invalid CRLF, insufficient
consumer buffers and partial payloads without exposing a partial packet.

Independent target sockets observe exits 127.0.0.2 and 127.0.0.3 for same-source
users, original domains/IPs, source IP/port, inbound/network/port selectors,
ordered blocking and balancing. The initial random-balancer fixture used prefix
`b`, which also selected its `blocked` outbound; a shuffled run exposed this.
The fixture now names that outbound `deny` and verifies round-robin exits
.2/.3/.2 across three separately authenticated flows. Neither production router
behavior nor acceptance tolerance was changed. A changed destination on an
existing UDP stream is rejected before it can reuse the first routing decision.

The fragmented-request fixture initially fragmented the PROXY v1 prefix too.
The pinned proxyproto implementation explicitly requires that header in one
write and rejected it before managed authentication. The fixture now follows
that requirement while still sending managed authentication and target headers
one byte at a time. No fragmentation tolerance is claimed for the PROXY v1
prefix itself.

Official mieru clients first timed out on UDP over both TCP and UDP underlays:
the native receiver inspected a logical domain `RemoteAddr` before reading.
It now consumes each `ReadFrom` peer, with a connected socket's known
`RemoteAddr` as the alternate contract. Real-client tests transfer 8192 raw bytes
in each direction per user across TCP and UDP payloads. The two users bill
24576 bytes at 1.5x and 8192 bytes at 0.5x. Core outbound counters independently
report 16384 bytes per direction for both users combined; no core user meter
exists. Manual disable closes existing TCP/UDP connections and leaves the
other user usable. With the other user's quota reduced to 8205 billed bytes,
a four-byte reply exceeding the remaining allowance sends no prefix and bills
nothing; a one-byte exchange consumes the remainder, reaching exact counters
8207 up / 8203 down / 8205 billed, closing old flows and denying a new session.

Temporary Go overlays proved the tests detect removed HMAC validation, forged
source metadata, duplicate core user meters, lost routing identity and removal
of the fixed-target guard. Overlays and mutant binaries stay outside the repo.
The first mutant build omitted `-buildvcs=false` and failed VCS stamping; that
build failure is not counted as a killed behavioral mutant.

The final tooling build succeeded at `/tmp/3x-ui-xray-managed-final-1` with
SHA256 `a9ffe3693e9b2b4b44a573fc8a815eeafdfe79f74a4e2c28b7617564c5d9cfb2`.
A fresh `prepare.sh` output matched all eight changed source files. Generated
protobuf uses protoc 33.5 and protoc-gen-go v1.36.11. Geodata test assets retain
the verified commit and hashes documented in the preceding increment.

The complete native package race run passed 10 top-level tests and eight
subtests, with no skip or race, in 77.547s. It includes existing native rate,
backpressure, one-way UDP and authentication tests as well as the new core
integration. Its accompanying bridge run failed only the balancer fixture
above. After correction, three complete bridge race runs passed 39 top-level
and 72 subtest events with no skip or race. The later owned-core-exit test and
full final regressions are recorded separately below. These logs remain local:
`/tmp/3x-ui-managed-panel-race-final.jsonl` and
`/tmp/3x-ui-managed-bridge-race-final.jsonl`.

Public CRUD/Runtime/API/UI/export/install/node/backup integration, IPv6, other
UDP outbounds, full core-routed rate/failure acceptance and all remaining
original protocol requirements are still open. Direct connector rate tests
are not evidence of rates through every core outbound. No frontend, migration
or generated API schema changed in this increment.

The final core full suite ran from 00:39:16Z to 00:49:24Z:

```sh
cd /tmp/3x-ui-managed-core-final-1
go test -p 1 -count=1 -shuffle=on -json ./...
```

That invocation failed: 82 test packages passed, `testing/scenarios` failed
`TestDokodemoTCP`, and 85 packages had no tests. The failing case attempted six
consecutive listener ports after selecting only the first free port. All five
attempts logged `bind: address already in use`; no managed request was involved.
Without code, assertion, kernel or network changes, isolated `-count=5 -run
'^TestDokodemoTCP$'` passed five times. The entire scenarios package then passed
all 66 top-level tests in 318.014s with the same shuffle seed:

```sh
go test -p 1 -count=1 -shuffle=1790642553566814748 -json ./testing/scenarios
```

Combining the 82 initially successful packages with that complete package rerun
produces **83 passing test packages, 561 top-level passes, 207 subtest passes and
one top-level skip (`TestSockOptMark`)**, plus 85 packages without tests. This is
a combined result, not a claim that the first invocation was green. Logs:
`/tmp/3x-ui-managed-core-full-final.jsonl`,
`/tmp/3x-ui-managed-dokodemo-recheck.jsonl`,
`/tmp/3x-ui-managed-core-scenarios-rerun.jsonl`.

The affected core package race command also exited zero:

```sh
go test -p 1 -race -count=1 -json ./common/session ./proxy/trojan ./proxy/freedom ./infra/conf
```

It passed 58 top-level tests and 12 subtests across two packages; session and
freedom have no package test files and are exercised by the real wire tests.
There were no skipped test cases, failures or detected races. Log:
`/tmp/3x-ui-managed-core-race-final.jsonl`.

Final panel regression and build used the final generated binary, owned
PostgreSQL fixture and independent OpenSSH server:

```sh
XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
SSH_E2E_SERVER=/usr/sbin/sshd \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
LD_LIBRARY_PATH=/tmp/3x-ui-pg-tools/root/usr/lib/aarch64-linux-gnu \
go test -p 1 -count=1 -shuffle=on -json ./...
go build ./...
golangci-lint run ./internal/routedbridge/... ./internal/mieru/...
```

All three commands exited zero. The Go run lasted 00:59:11Z–01:07:40Z on
2026-09-29: **52 test packages and 2507 top-level tests passed; 18 top-level tests
skipped**, with seven packages without tests. There were 5007 passing and 29
skipped test/subtest events, no failures. Native mieru passed in 70.667s,
routedbridge in 4.136s and the service package in 204.266s. The core-exit test
kills only its owned process, observes the existing packet stream close, and
requires authenticated readiness and new target creation to fail. The build
succeeded and affected lint reported zero issues. Log:
`/tmp/3x-ui-managed-panel-full-final.jsonl`.

The skipped top-level cases were `TestHostAutoMigrateCreatesColumns_Postgres`,
`TestClientWeeklyRenewMigration_Postgres`, `TestMigrate_Postgres`,
`TestUpdatePanel_UnsupportedPlatformReturnsNoRunId`,
`TestAddInbound_PostgresCommitFailureMakesNoRuntimeCall`,
`TestUpdateInbound_PostgresCommitFailureMakesNoRuntimeCall`,
`TestAddTrafficReturnsDeferredCommitFailure`,
`TestSetClientLimitHwidIsSerializedWithSyncInbound`, and the ten scale cases
`TestDelAllClientsPostgresScale`, `TestWsPayloadScale`,
`TestGroupAndListPostgresScale`, `TestGetClientTrafficByEmailABScale`,
`TestAddTrafficPollScale`, `TestAddDelClientPostgresScale`,
`TestAllAPIsPostgresScale`, `TestBulkOpsPostgresScale`,
`TestSyncInboundPostgresScale`, `TestGetXrayConfigScale`.
Their additional environment/platform conditions were not enabled by this run;
they are not counted as passes or complete second-dialect acceptance.

No new full frontend result is claimed for this internal Go/core increment.
Shell syntax, source formatting, relative documentation links and Git whitespace
checks passed. Public integration remains open as stated above.

A final focused `go test -race -count=1 -run
'^TestManagedCoreExitClosesExistingPacketFlowAndRefusesNewTarget$' -v
./internal/routedbridge` also passed against the final binary (1.150s, no skip
or detected race). Log: `/tmp/3x-ui-managed-core-exit-race.log`.

## Native mieru live credential replacement — 2026-09-29

This increment changes the internal native adapter; public model, Runtime,
API/UI/export, nodes and deployment remain open. The official v3.38.0 wire
engine and previously pinned managed core binary are unchanged.

Real client tests validate complete-batch credential replacement on both TCP
and UDP underlays, preserving unchanged clients' existing TCP/UDP payload flows.
They cover an invalid replacement batch, password rotation, removal/re-addition,
revoking every user without rebinding the listeners, rejecting updates after
shutdown, and pending or cached authentication. Reassigning the same external
username/password to a new policy ID closes the old association: the old account
retains 6 upload + 6 download bytes and 18 billed bytes at 1.5x; the new account
receives 5 + 5 raw bytes and 5 billed bytes at 0.5x. Pending target dial contexts
are cancelled, idle authenticated TCP sockets are reclaimed and an unrelated
user's existing connection remains usable. Actual receive backpressure is
established before the rotation cleanup test.

Failure evidence preceded the final implementation:

- The first test could not compile because `UpdateClients` did not exist.
  The first implemented version passed real credential replacement.
- A real idle-socket test then failed: two owned TCP sockets remained after
  rotating the user whose logical flow had already ended. Retaining an opaque
  authentication identity on owned sockets fixes targeted reclamation.
- Immediately aborting a retired TCP socket before logical close notifications
  caused a 2.155s cutoff, exceeding the unchanged 1.25s assertion. Native graceful
  close can spend 1000 one-millisecond waits per session if the close message
  cannot be sent. The final order starts logical closes, allows at most 100ms
  for notifications, then aborts only the previously captured retired sockets.
- The first cached-transport fixture used multiplex factor 1024, outside the
  official configuration choices. A native traffic-threshold shift overflow
  disabled TCP reuse. The fixture now uses official high factor 3 and requires
  matching actual local endpoints before exercising cached authentication.
- One repeated race invocation observed `io.ErrUnexpectedEOF` while reading a
  revoked TCP handshake. The denial helper accepts this specific terminal EOF;
  any complete SOCKS reply still fails, and independent target-dispatch count
  must remain zero. No cutoff or rate tolerance changed.

Four temporary Go source overlays were rejected by the real-client tests:
username-only authentication returned successful SOCKS replies after retirement;
ignoring a changed policy ID kept the old connection open; removing generation
cancellation left TCP/UDP target dial contexts live; replacing `UpdateClients`
with a no-op left the backpressured target active. Each failed its behavioral
assertion, not compilation. Sources and logs were kept only under
`/tmp/3x-ui-mieru-hot-mutants` and `/tmp/3x-ui-mieru-hot-mutant-*-final.log`.

The corrected focused race command passed three complete repetitions:

```sh
go test -race ./internal/mieru \
  -run '^TestNative(Credential|Rotation|PolicyReassignment|Shutdown)' \
  -count=3 -shuffle=on -json
```

It passed **18 top-level and 33 subtest executions**, with zero skips, failures
or detected races, in 39.991s. Log:
`/tmp/3x-ui-mieru-hot-race-repeated.jsonl`.

The complete native adapter race run also passed against the existing managed
core, including its real TCP/UDP routing and single-billing integration:

```sh
XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
go test -race -count=1 -shuffle=on -json ./internal/mieru
```

Result: **15 top-level and 17 subtests passed**, zero skipped or failed tests,
no detected race, 83.406s. Log:
`/tmp/3x-ui-mieru-hot-native-race.jsonl`.

In the repeated local race run, rotation closed the existing TCP/UDP payload
flows in 2.663–3.299ms; backpressured target cancellation took 0.738–1.183ms.
These observed local timings are distinct from the unchanged 1.25s assertion
bound and the 100ms TCP notification grace. They are not remote-network latency
guarantees. Shared UDP listeners and unchanged clients stayed usable.

Final whole-repository regression used the same pinned managed binary, owned
OpenSSH and PostgreSQL fixtures as the preceding bridge milestone:

```sh
XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
SSH_E2E_SERVER=/usr/sbin/sshd \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
LD_LIBRARY_PATH=/tmp/3x-ui-pg-tools/root/usr/lib/aarch64-linux-gnu \
go test -p 1 -count=1 -shuffle=on -json ./...
go build ./...
golangci-lint run ./internal/mieru/...
```

All commands exited zero. From 01:33:10Z to 01:41:14Z, **52 test packages and
2512 top-level tests passed; 18 top-level tests skipped**. There were 2509
passing and 11 skipped subtests, seven packages without tests, and no failures.
The complete top-level skip list is identical to the preceding bridge run's
explicit list above; those environment/platform/scale conditions remain
unverified, not passes. Native mieru took 74.417s and service tests 195.878s.
Build passed; affected lint reported zero issues. Logs:
`/tmp/3x-ui-mieru-hot-panel-full.jsonl`,
`/tmp/3x-ui-mieru-hot-panel-build.log`,
`/tmp/3x-ui-mieru-hot-panel-lint.log`.

Go formatting, new comment-block limits, relative documentation links and Git
whitespace checks passed. This increment changes no public API/model/migration
or frontend files and makes no new full-frontend validation claim. Native
pre-accept resource bounds, retained diagnostic group reclamation, public
Runtime activation and the remaining original acceptance requirements stay open.

## Native mieru resource bounds — 2026-09-29

This internal increment retains the v3.38.0 official client and embeds only a
maintained copy of the server protocol package. Source pins, GPL license,
per-file checksums, patch and reproduction instructions are in
[tools/managed-mieru](../../tools/managed-mieru/README.md). The public Runtime,
UI/API, PostgreSQL vertical and remaining protocol/outbound/platform acceptance
are still open. Queue payload bounds are not a process resident-memory bound.

Before the extension, actual official clients allocated 333 TCP / 335 UDP native
sessions while adapter admission was paused, beyond the adapter's 256-session
limit. A credential generation retained two native diagnostic time series.
After adding bounded queues, tests also exposed UDP loss with a one-item staging
channel and a duplex stall when a full receive queue blocked ACK processing.
The final staging queue has both a 64-item and 128 KiB bound. Packet input stays
nonblocking; application reads wake ordered delivery/window updates. TCP uses
backpressure. The original ten-second resumed transfer deadline was retained.

Actual tests use four users and five loopback listeners: three flood users must
reach exactly the shared 256-session peak and trigger rejections while an
existing fourth user's payload continues. Shutdown must return zero active
leases and queue bytes. Another test pauses admission during a 1,835,008-byte
upload for at least 300 ms after saturation, verifies the five-queue 640 KiB
payload ceiling, resumes the complete echo, checks exact durable raw/billed
bytes and requires zero resources after shutdown, over both underlays.

Closed-session churn first retained one native metadata entry after releasing
its admission slot. Twelve real connect/echo/close rounds per underlay now
require immediate metadata removal with slot release. A protocol-level test
also first accepted a pending authenticated session after underlay shutdown;
it now requires `io.ErrClosedPipe` and reclaimed resources. The complete copied
upstream protocol suite still exercises unconfigured native behavior.

Temporary Go overlays independently removed the tree byte cap, shared admission
cap and diagnostic suppression. All three were rejected by behavioral assertions:
1,212,426 queued bytes exceeded 655,360; 312 TCP / 321 UDP sessions exceeded 256;
a credential generation registered two retained series. The byte mutation was
caught by the TCP case; its UDP case passed, so that mutation is not separate
proof of the UDP byte limit. No working source or module-cache file was changed.
Logs: `/tmp/3x-ui-mieru-resources-mutant-{bytes,admission,diagnostics}.log`.

Final verification uses the same owned PostgreSQL 16.15/OpenSSH 9.6p1 fixtures,
Go 1.27.1 and managed Xray binary/source pin recorded in the preceding increment:

```sh
export XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1
export XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1
export SSH_E2E_SERVER=/usr/sbin/sshd
export XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable'
go test -race -p 1 -count=1 -shuffle=on -json ./internal/mieru/...
go test -p 1 -count=1 -shuffle=on -json ./...
go build ./...
golangci-lint run ./internal/mieru/...
python3 tools/managed-mieru/prepare.py --verify
```

Pinned upstream source keeps its existing formatting and lint conventions via
an enumerated-file exclusion for errcheck/errorlint/staticcheck/unconvert and
formatters. Compiler and vet still cover those files. Authored resource code
and tests retain all repository lint rules. No public API, schema, migration or
frontend source changes are included in this increment.

The first native race run passed both packages: adapter 19 top-level / 23
subtests in 121.343s; copied protocol 75 top-level / 1,838 subtests in 30.260s.
There were no skipped tests, failures or race reports. This includes the final
shutdown queue/write synchronization changes. Log:
`/tmp/3x-ui-mieru-resources-native-race.jsonl`.

Review then found a further ID-reuse boundary: a periodic cleaner can retain an
old session pointer while worker cleanup removes that session and a new session
reuses its ID. A real encrypted-UDP regression first returned EOF instead of the
replacement session's payload after that stale cleanup. Managed removal now
compares both ID and session pointer before deleting the entry. Five repeated
race runs passed with exact replacement payload in 1.057s. Logs:
`/tmp/3x-ui-mieru-resources-reuse-{red,green}.log`.

The pre-fix full repository run passed 53 test packages, 2,591 top-level tests
and 4,353 subtests, with 18 top-level / 11 subtests explicitly skipped and seven
packages without tests. Build, lint (zero issues) and exact source reproduction
passed. Because the ID-reuse regression required a subsequent production fix,
these are preliminary results; final checks are recorded separately below.

After the ID-reuse fix, the final native race run passed 95 top-level tests and
1,861 subtests across both packages, with zero skips, failures or race reports:
adapter 19 top-level / 23 subtests in 120.377s; copied native package 76 top-level /
1,838 subtests in 31.586s. Final log:
`/tmp/3x-ui-mieru-resources-final-native-race.jsonl`.

The subsequent full repository invocation did **not** pass on its first run.
`TestSSHUpstreamPolicyQuotaAndLifecycle` stalled in the test client's unbounded
`recovered.Dial` during post-reset authentication replacement. A SIGQUIT of only
the owned `service.test` process captured the blocked SSH channel-open stack;
the panel listener, policy watcher and unrelated channel were still running.
Recorded child fixtures were stopped after verifying their process start IDs.
Other packages continued. The failed run is retained in
`/tmp/3x-ui-mieru-resources-final-panel-full.jsonl`; diagnostic stack:
`/tmp/3x-ui-mieru-resources-stalled-stack.log`.

The fixture now uses `DialContext` with its existing two-second recovery deadline
and cancels that attempt when `Client.Wait` observes connection closure. It keeps
the actual recovery, peer continuity and exact post-reset billing assertions.
Five real OpenSSH/Xray repetitions passed under race detection in 63.844s, with
no skipped cases: `/tmp/3x-ui-mieru-resources-ssh-recovery-race.log`.
The complete service package is rerun with the failed invocation's exact shuffle
seed, `1790649930973753459`, with PostgreSQL, OpenSSH and both managed-core
environment variables set; this is not a focused-test-only replacement.

The complete service rerun passed in 194.538s: 913 top-level tests and 981
subtests passed; 13 top-level tests and five subtests were explicitly skipped.
It ran from 02:53:51Z to 02:57:05Z. The formerly stalled test passed in 11.79s.
Log: `/tmp/3x-ui-mieru-resources-final-service-rerun.jsonl`.

Combining that complete service-package result with the other 52 complete
packages from the full invocation gives 53 passing test packages, 2,592 passing
top-level tests and 4,353 passing subtests. There are 18 top-level / 11 subtest
skips and seven packages without tests. The exact skipped test set is unchanged
from the preceding hot-credential increment. This combined coverage does not
turn the earlier failed invocation into a successful first run.

After the service fixture change, `go build ./...` passed; lint over both
`./internal/mieru/...` and `./internal/web/service/...` reported zero issues;
source reproduction passed byte for byte. Logs:
`/tmp/3x-ui-mieru-resources-final-{panel-build,panel-lint,reproduce}.log`.
The final verification script exited zero. Relative documentation links, Python
syntax, source-manifest JSON, authored Go formatting and staged whitespace checks
also passed. No additional public-service or full-frontend acceptance is claimed.

## Shared protocol ownership and private hot insertion (2026-09-29)

This increment prepares public mieru integration; it does not add the public
protocol selector, service manager, API/UI/export, nodes or deployment. SSH now
acquires the common database-keyed controller owner. The native mieru adapter
can hold a second lease without fencing the SSH source or gaining a separate
rate/flow allowance. Final release closes active flows; stale repeated release
cannot close a later owner. A copied policy ID in a different database keeps
independent accounting. Owners must stop their own adapters/watchers before
releasing the lease, as the SSH manager does.

The initial shared-ownership test failed against the unchanged SSH manager with
`usage counter lifetime is closed`. After the manager acquired the common lease,
128 aggregate flows, exact 1.5× fractional charging, peer-stop survival, last-owner
shutdown, quota-preserving reacquisition and 20 concurrent owners passed.
A database-isolation mutation caused one database to receive three bytes instead
of its one byte, and the new isolation test rejected it.

Real `golang.org/x/crypto/ssh` and unmodified official mieru v3.38.0 clients then
streamed concurrently through both authenticated adapters into loopback TCP
peers. Both TCP and UDP mieru underlays were exercised. These tests use explicit
loopback connectors; they are cross-protocol policy evidence, not the public
Runtime or Xray routing acceptance. Measurement uses actual target upload bytes
and client download bytes, with 250ms settling, 1.2s sustained windows, rates
65536/131072 then 131072/65536 B/s, and the preset lower 80% / upper 106% plus
one 100ms burst bounds. Each live change completes its measurement within 2s.
The unlimited 300ms baseline must exceed 1048576 B/s in each direction.

The first SQLite run measured unlimited 4.27–5.34 million B/s and shaped
63,774–131,032 B/s across the configured directions, within those predefined
bounds. After closing SSH and releasing its manager, the existing mieru flow
continued at the same configured budget. A temporary overlay restored a separate
SSH controller with an independent source: the 65536 B/s aggregate case forwarded
5,059,381 bytes (TCP underlay) or 4,961,077 bytes (UDP underlay) in about 1.2s;
both cases correctly failed. No tolerance was changed.

Real gRPC hot insertion initially failed at level 4294967295 because the imported
core JSON builder used uint8; at level 255 it installed a listener but discarded
the managed flag, causing capability authentication to return EOF. The explicit
private serializer fixes both. The running-core test verifies authenticated
readiness, TCP echo, an 8193-byte UDP packet and its actual reply peer, no core
user counters for managed payload, and continuity of an existing stream before
and after insertion/removal. The core's required private policy definitions are
installed at process startup; this is not dynamic policy installation.

Two further overlays removed the managed flag or replaced the private level
with zero. They failed capability health and the duplicate-counter assertion,
respectively; the latter exposed 8208 upload plus 8208 download bytes in the core
user counters. Bypassing private-config validation also failed the rejection
cases, including public listeners, empty/ambiguous credentials and fallbacks.
Original-source logs and mutation logs are under
`/tmp/3x-ui-mieru-{shared-policy,shared-rate,hot-api}-*.log` and
`/tmp/3x-ui-mieru-public-mutants/`.

Focused `-race -count=2` verification passed 14 top-level executions and 42
subtest executions across service, routedbridge and xray, with zero skips or
race reports. Service tests ran on SQLite and the isolated PostgreSQL fixture,
including both real protocol underlays, in 62.761s; routedbridge took 1.315s and
xray 1.127s. Log: `/tmp/3x-ui-mieru-public-race.jsonl`. Affected-package lint before
full regression reported zero issues. Full repository checks follow below.

The first complete repository invocation failed the new UDP peer-stop recovery
test: 85,197 upload bytes in 1.200s at 131,072 B/s, below the preset 80% bound.
The other 52 tested packages passed; service failed, and seven packages had no
tests. That invocation recorded 2,598 passing top-level tests / 4,373 passing
subtests, one top-level/subtest failure, and 18 top-level / 11 subtest skips.
Its build, final lint and stock-core steps did not run because the script stopped
at the failure. Log: `/tmp/3x-ui-mieru-public-full.jsonl`.

Native receiver tests exposed three separate defects. Segment-only window
credits promised 335,872 payload bytes but retained only 259,776. Queued replies
could reopen a full receiver using stale credits. Finally, advertising more
than 64 staging slots lost fragments before the input worker could run.
Byte-aware credits, send-time window refresh and staging-aware credits each
passed their deterministic test after failing against the preceding source.
They were insufficient alone: unchanged real UDP recovery repeated runs still
failed once in 12, once in 15 and once in 20 runs, respectively. A temporary
inline-input experiment also failed (one baseline and one recovery failure in
20 runs); it was not adopted.

A sequence-history diagnostic then reproduced one failure in 30 runs. Missing
fragment 798 was first dropped by staging and repeatedly retransmitted while
the reorder buffer was full of later fragments. Those retransmissions were
discarded, leaving an empty readable queue and 64 later fragments stranded
behind the gap. The connection and download direction remained active.
The bounded reorder buffer now retains earlier fragments, evicting the latest
ones when necessary. Deterministic segment-capacity and byte-capacity cases
first failed after reading only 65,536/131,072 and 131,072/262,144 bytes; both
now read the complete ordered prefix with no duplication and no increase in
their queue payload bounds. The test fixture initially used `io.ReadFull` with
the native one-shot read deadline, which stalled on its second read; that owned
test was stopped and the fixture changed to reset its deadline for each read
before recording the meaningful RED results. No throughput expectation, rate,
settling period or measurement duration changed.

Diagnostic overlays and RED/GREEN logs are under
`/tmp/3x-ui-mieru-public-debug/`. Final original-source verification follows.

Priority retention alone still failed one unchanged recovery run in 20. The
remaining receiver issue was coupling cumulative ACK progress to movement into
the application queue: a full reader caused already-retained contiguous data to
remain unacknowledged, provoking repeated retransmission and sender backoff.
Managed sessions now keep separate acknowledgment and delivery cursors. Wire
ACK tests first observed next=128 where all 256 contiguous fragments were
retained; after the change they acknowledge 256 with a zero window. A deliberate
hole stays at 128 until its retransmission is retained, and neither duplicate
nor rejected overflow fragments advance the ACK. The existing ordering and
payload-bound tests remain unchanged and pass. A temporary behavior experiment
passed all 20 real UDP recovery repetitions in 101.095s before the production
change; this is recorded as experimental evidence, not final verification.

Private serializer review also added a duplicate-password rejection case:
separate emails sharing one Trojan credential were previously accepted. That
case failed before validation and passed after rejecting duplicate passwords.
Logs: `/tmp/3x-ui-mieru-public-debug/{ack,duplicate-credential}-{red,green}.log`.

Final full native `-race -count=1` verification passed 100 top-level tests and
1,865 subtests, with no skips or race reports: adapter 118.237s and copied native
package 31.103s. Final shared-policy/private-core `-race -count=2` passed 14
top-level executions and 44 subtest executions, no skips or races: service
62.672s, routedbridge 1.305s, xray 1.127s. These used SQLite, the isolated
PostgreSQL fixture, real SSH/official mieru clients and the pinned managed core.
Source reproduction passed byte for byte before these runs.

The final original-source UDP recovery test passed all 20 repetitions in
100.942s. Unlimited upload/download baselines measured 4,488,052–5,023,575 B/s.
After stopping SSH, mieru upload measured 125,493–125,599 B/s at 131,072 B/s;
download measured 63,768–69,279 B/s at 65,536 B/s. Every sample met the unchanged
80% lower / 106% plus 100ms burst upper bounds. Logs:
`/tmp/3x-ui-mieru-public-final-{native-race,shared-race}.jsonl` and
`/tmp/3x-ui-mieru-public-final-recovery.log`. Full-root/build/static/stock-core
results are recorded below when the remaining checks finish.

The final complete root invocation passed all 53 tested packages: 2,604 top-level
and 4,379 subtests passed, with no failures. It retained exactly the prior set of
18 top-level / 11 subtest skips; seven other packages have no tests. The complete
service package passed in 219.183s, including the formerly failing UDP recovery
case and SQLite/PostgreSQL management regressions. This successful complete
invocation supersedes neither the recorded first failure nor the experimental
runs; all are retained as separate evidence.

`go build ./...` passed. Final lint over mieru, xray, routedbridge and service
reported zero issues. Explicit rejection using the separate stock core passed
before any target access (test 0.12s, package 0.160s), with no skips. The frozen
`/tmp/3x-ui-mieru-public-final-checks.sh` exited zero. Full JSON, build, lint and
stock-core logs use `/tmp/3x-ui-mieru-public-final-{full,build,lint,stock}` with
`.jsonl` for full tests and `.log` for the other commands. Relative document links
and whitespace checks also passed. No frontend or public mieru completion is
claimed by these internal prerequisites, and no database schema changed here.

## Public mieru Runtime work in progress (2026-09-29)

The public vertical is not yet complete. Current source connects canonical
mieru inbounds and email/password credentials to the shared policy controller,
authenticated per-client Xray bridges and the normal `runtime.Runtime` mutation
path. Later entries below record completed local API/schema generation, UI,
presence/status, native profile exports and portable restoration checks. Node
integration, deployment and the remaining acceptance matrix are still open.
The earlier complete-suite results above describe the published prerequisites,
not these newer uncommitted changes. No physical database columns were added;
the existing string protocol column and canonical client/usage tables are reused.

`TestMieruInboundRunsThroughProductionXrayLifecycle` uses the normal
`InboundService.AddInbound` and `XrayService.RestartXray` paths, the pinned
managed core, and the unmodified official mieru v3.38.0 Go client. Each TCP and
UDP underlay carries both TCP and UDP payloads through domain/network routing
rules to separately observable loopback targets; the default outbound blocks.
The UDP assertion checks the actual reply peer. Each direction carries 8,193
bytes per payload transport: the durable account must record upload 16,386,
download 16,386 and billed 49,158 at multiplier 1.5. The test also checks that
startup preserves negative first-use expiry and authenticated use activates it.
Its initial RED was the public policy API reporting unsupported before Runtime
integration. The actual first GREEN passed both underlays in 3.310s.

A selected race run covering this path, native listener failure, shared policy
ownership, SSH lifecycle and preview preservation passed 14 top-level tests and
20 subtests with zero skips (service 47.551s, native adapter 2.815s). Log:
`/tmp/3x-ui-mieru-public-runtime-race.jsonl`. This predates the following port and
shutdown changes and is not a complete backend regression result.

Managed SSH/mieru loopback TCP bridge reservations now participate in inbound
save/update conflict checks, including disabled owners, other managed bridges,
public listeners, the API and AWG egress ports, AWG relay slots and peer forwards.
Address, transport and node separation remain valid. The original RED accepted
all ten conflicting SSH/mieru saves, a conflicting bridge update, and four AWG
resource directions. A test assertion initially compared compact JSON with the
service's pretty-printed JSON; it was corrected to decode and inspect the stored
port and native transport. The next focused run passed 39 top-level tests and
37 subtests, zero skips, including PostgreSQL canonical ownership. Logs:
`/tmp/3x-ui-managed-port-{red.log,green-2.jsonl}`. The first attempted green run
had one test assertion failure and one PostgreSQL skip because its environment
was omitted; it is not included in the successful counts.

Public lifecycle tests additionally exercise live password rotation, manual
client disable/re-enable while another client's TCP/UDP flows continue, quota
reduction to current billed usage, denial after a real core restart, and traffic
reset preserving a separate manual disable. Positive recovery is allowed the
original two-second convergence requirement. Existing admitted flows must close
within 1.25 seconds, and new connection rejection is checked separately. This
quota test checks restriction and restart semantics; it does not replace the
remaining public transfer-until-quota and throughput acceptance tests.

The first lifecycle invocation had a fixture compilation error. Its next run
passed the hot-credential cases but reached a 90-second package timeout during
cleanup after an immediate post-reset admission failed. The official client's
post-dial SOCKS handshake sets its own ten-second read timeout and does not use
the supplied context deadline. Rejection probes now own separate official
clients and stop/join them after 750ms; positive admission retries within the
specified two-second convergence window. Neither the 1.25-second existing-flow
cutoff nor production limits were relaxed. Logs retain each invocation as
`/tmp/3x-ui-mieru-public-lifecycle-{first,2,3}.jsonl`.

That corrected invocation exposed a real TCP-underlay failure: after the core
stopped, an admitted client flow reached the 1.25-second timeout. A native-only
reproduction showed that `Server.Close` aborted the physical TCP connection
before logical close messages could reach the official client. The original
native test started its deadline after `Close` returned and missed time spent
inside shutdown; the corrected test starts both reads before shutdown and
reproduced a TIMEOUT in one of three repetitions. Shutdown now stops admission,
closes and joins managed sessions, then releases the underlying connections.
The existing per-session 100ms blocked-write abort remains. Both underlays
passed ten repetitions in 1.760s total, with the same 1.25-second notification
bound. Logs: `/tmp/3x-ui-mieru-shutdown-{red-2,green}.log`.

After that fix, the public hot-update, quota/reset and core-failure cases all
passed on both underlays. The combined invocation still failed because its
PostgreSQL subtests reused a schema and collided on the fixture username.
Giving each underlay a disposable schema fixed isolation; the separate actual
PostgreSQL TCP/UDP routing and exact billing run passed one top-level test and
two subtests in 4.099s, no skips. Logs:
`/tmp/3x-ui-mieru-public-lifecycle-green.jsonl` (failed combined invocation),
`/tmp/3x-ui-mieru-public-postgres-green.jsonl` (successful isolated PG run).

Native first-use tests verify that unauthenticated requests cannot invoke the
callback, a metadata failure prevents routing and creates no usage, and rotating
a credential cancels an in-progress first-use callback and closes its request.
Both tests passed on both underlays in 1.563s. Temporary source overlays then
proved meaningful RED for removing the callback, ignoring public hot changes,
and ignoring the applied-core protection check. The latter failed on existing
flow cutoff before reaching the later listener-release assertion; the mutation
runner's first expected-message check was too narrow, and the actual failures
were inspected and retained. Production files were never replaced by overlays.
Logs: `/tmp/3x-ui-mieru-first-use-green.log` and
`/tmp/3x-ui-mieru-public-mutations/drop-{first-use,hot-update,core-protection}.log`.

Broader current-source race/static results will be recorded after the running
checks finish; none of these focused checks constitutes public vertical or
whole-task completion.

The subsequent current-source check passed the complete native adapter race
suite (23 top-level tests / 31 subtests, 115.089s), complete local Runtime race
suite (57 / 67, 2.984s), and selected public/shared/SSH/port service race suite
(52 / 54, 98.618s), all with zero skips or race reports. The script itself exited
nonzero because lint then found three formatting issues and one direct error
assertion. Formatting and `errors.As` handling were corrected; affected lint then
reported zero issues, and listener/shutdown/first-use focused race tests passed
4 top-level tests / 8 subtests in 5.091s. Logs use
`/tmp/3x-ui-mieru-public-runtime-{native-race,local-race,service-race}.jsonl`,
`-lint-fixed.log` and `-final-focus.jsonl`. These results precede the presence/API
changes described below and do not replace a final whole-root regression.

Reproduction for those checks (all test processes use disposable databases,
loopback resources and the pinned managed core):

```sh
export XUI_MANAGED_XRAY_E2E_BINARY=/path/to/verified-managed-xray
export XRAY_E2E_BINARY="$XUI_MANAGED_XRAY_E2E_BINARY"
export SSH_E2E_SERVER=/usr/sbin/sshd
export XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable'
go test -p 1 -race ./internal/mieru -count=1 -timeout=4m
go test -p 1 -race ./internal/web/runtime -count=1 -timeout=90s
go test -p 1 -race ./internal/web/service -count=1 -timeout=3m \
  -run '^(TestMieruInbound|TestManagedBridge|TestManagedPolicy|TestSSHInboundRunsThroughProductionXrayLifecycle|TestSSHConfigPreview|TestSSHInboundPreservesCanonical|TestCheckPortConflict|TestForwardedPort|TestAddInbound.*[Ff]orward|Test.*AmneziaWG.*(Conflict|Port))'
```

Additional listener tests caught real alias bypasses: bracketed addresses,
expanded IPv6 wildcards and IPv4-mapped IPv6 addresses were validated but kept
noncanonical strings for conflict checks; unmatched or nested brackets were
also accepted. All 16 new SSH/mieru cases were RED before normalization. Managed
listen addresses now store a canonical IP, accepting a single matched bracket
pair and rejecting malformed input. Related reservation, canonical service,
real SSH/mieru and PostgreSQL paths then passed 39 top-level tests / 53 subtests
in 15.043s, no skips. Logs:
`/tmp/3x-ui-managed-listen-{red.log,green.jsonl}`.

Native presence now counts admitted logical TCP sessions and UDP associations,
not underlying multiplexed transports. It excludes incomplete handshakes,
policy rejection, closing sessions and retired authentication generations, and
reports the actual normalized peer IP. The native presence test was RED with
two working payload flows but zero observations, then related race coverage
passed 8 top-level tests / 15 subtests in 16.166s. Public service projection
preserves canonical membership and rejects stale username attribution; status
reads are owner-scoped, start no runtime, and propagate database errors.
Its real TCP/UDP and existing SSH regressions passed 7 top-level tests / 4
subtests under race in 13.954s, no skips. Logs:
`/tmp/3x-ui-mieru-presence-{red.log,green.jsonl}` and
`/tmp/3x-ui-mieru-public-presence-{red.log,green.jsonl}`.

The new `/panel/api/inbounds/mieru/status` route uses the existing owner/session
and API-token restrictions. Its authorization test first failed with the missing
route's 404. After routing was added, it caught an incorrect inherited JSON
field name (`authenticatedConnections` instead of `authenticatedSessions`);
the tag was corrected and all generated schemas/OpenAPI/reference MDX were
regenerated. These failures remain in
`/tmp/3x-ui-mieru-status-api-{red.log,green.jsonl}`. The corrected status API and route registry checks passed 3 top-level tests / 2
subtests across two packages, no skips (`...-green-fixed.jsonl`).

Cron collection now includes idle native TCP/UDP associations, real source IPs,
last-online timestamps and active inbound tags even when the core online API is
unavailable. Like SSH, mieru observations never trigger a host-wide shared-IP
ban. Tests first failed for missing idle users and an incorrectly created ban;
the integrated race suite passed 4 top-level tests / 2 subtests in 4.580s, no
skips (`/tmp/3x-ui-mieru-collectors-{red.log,green.jsonl}`). This does not establish
per-client IP/device-limit enforcement. Frontend consumption and the rest of
the public vertical remain open.


Frontend integration now exposes native mieru transport selection, canonical
password editing (including a client shared with local SSH), lifecycle-field
preservation, runtime state and logical-session counts on desktop/mobile, and
online client rollups. Clone requests release the source private bridge port.
All new messages are present in all 13 locales. Credentials are validated by
UTF-8 byte length to match the official client/server bound.

Evidence remains incremental: form/clone tests first failed 7 cases, then
passed 17/17 including existing SSH; additional native credential checks failed
3 cases before byte/whitespace validation; client form tests failed 6/7 before
integration, then native forms/client and SSH regressions passed 22/22. Runtime
status UI failed 10/10 before integration, then passed 20/20 across both
protocols. An initial scripted badge refactor produced a duplicate declaration;
that failed run is retained separately. Managed client rollups failed 2/5 before
registration, then rollup plus locale/dead-key checks passed 7/7. Type checking
passed; lint caught one unsafe optional-chain test expression, then passed after
an explicit request guard. Logs use `/tmp/3x-ui-mieru-{form,client-ui,status-ui}*`,
`/tmp/3x-ui-managed-count-ui-*` and `/tmp/3x-ui-mieru-ui-{typecheck,lint}*`.

The first complete `npm test` run, including Chromium Storybook tests, finished
with 186 passing files / 1 failing file and 1851 passing tests / 1 failing test
in 357.13s, no reported skips. The sole failure was the new mieru polling test's
5000ms total test deadline; it waited for a real 3000ms poll alongside rendering,
tooltip timers and further refresh work. This is a failed complete run, not a
full frontend pass (`/tmp/3x-ui-mieru-ui-full-test.log`). A deterministic interval
clock is being checked while retaining actual query polling and stale-state
assertions. Real browser CRUD against the running panel and official mieru
traffic is still required by the public integration plan.


The interval-clock version retained real TanStack polling and all failure/offline
assertions and passed 20/20 SSH/mieru status tests in 12.12s; it does not relax the
5000ms test deadline. A prior edit command used the wrong working directory and
made no changes; the resulting rerun of the original tests is not evidence for
the clock change (`...-poll-timers.log` versus `...-poll-timers-fixed.log`).

Canonical follow-up tests first rejected native-only, mixed SSH/mieru and detached
portable policy restoration, and exposed old Xray listeners surviving conversion
back to mieru over both transports. A separate legacy-import test showed raw
history becoming zero; that RED invocation also accidentally selected one
PostgreSQL subtest without a DSN and skipped it, which is not counted as a pass.
The first fix exposed an additional real recovery failure: inbound editing
removed the last binding and deleted the traffic projection while its owned
ledger survived. A direct boundary assertion reproduced `record not found`
(`/tmp/3x-ui-mieru-transition-accounting-red.log`). The fix preserves owned
projections across detach rather than weakening ledger ownership checks.

The first post-fix race invocation remains failed
(`/tmp/3x-ui-mieru-portable-transition-green.jsonl`). Its detached restoration
assertion also mistakenly required `supported=true` with zero attachments; the
existing API requires an actual supported attachment. The test now preserves
that contract and verifies reattachment enables execution without changing the
restored usage or owner. A fresh combined race run is pending; no complete
backend verification is claimed from these changes.

The corrected combined backend race run passed 8 top-level tests / 25 subtests
in 27.138s, with no failures or skips
(`/tmp/3x-ui-mieru-portable-transition-green-fixed.jsonl`). It exercises actual
TCP/UDP protocol conversion and recovery, retained accounting projections,
native-only/shared/detached/legacy portable clients on SQLite and PostgreSQL,
and existing invalid-import, legacy SSH and traffic-row reuse regressions.
Latest frontend type checking and lint also passed; the complete frontend test
rerun followed by its build is still pending.

### Public mieru export continuation (2026-09-29; uncommitted vertical)

Frontend full rerun after deterministic interval tests: 1848 tests passed and
four existing form tests exceeded the unchanged 5000ms limit (three
`inbound-form-modal` cases and `happ-routing-editor` invalid JSON). Keeping the
same source and deadlines, `npm test -- --maxWorkers=1` passed all 187 files /
1852 tests in 480.71s; `npm run build` then passed in 2.87s. Logs:
`/tmp/3x-ui-mieru-ui-full-test-fixed.log`,
`/tmp/3x-ui-mieru-ui-full-test-serial.log`, `/tmp/3x-ui-mieru-ui-build.log`.
This identifies concurrency sensitivity; these results predate the new export UI.

Native export RED first returned no share link; direct link tests then passed
with the official `appctl.ClientProfileToMultiURLs` encoder. Actual subscription
interop caught the separate SQL protocol allowlist still excluding mieru. After
fixing that query, official clients imported generated subscriptions and sent
TCP and UDP payloads over TCP and UDP underlays, including a Host address override
and deliberately stale settings credentials. The test waits for actual public
listener status before connecting. Race run: 5 top-level / 11 subtests passed,
zero skips, 5.779s (`/tmp/3x-ui-mieru-export-green-ready.jsonl`). Earlier failed
logs remain `/tmp/3x-ui-mieru-export-red-and-interop.log` and
`/tmp/3x-ui-mieru-export-green.jsonl`.

The same RED demonstrated unsupported SSH/mieru/MTProto paths producing
misleading direct-only Xray JSON profiles; those paths now emit no Xray config.
Mihomo mapping uses the same validated native profiles and preserves native
transport, credentials and public endpoint fields. Official v1.19.30 arm64 gzip
was verified against GitHub's published SHA-256:
`58896873736d28628f66de3677c8654fa0f180662523148e136cff4f6e890069`.
Its configuration checks passed, but the first data test raced Mihomo's routing
startup and failed after a successful SOCKS handshake. Pinned upstream startup
opens listeners before `tunnel.OnRunning`; the test now waits for actual echo
readiness, with the original 3s startup deadline. The follow-up result is pending;
no completed Mihomo data-path acceptance is claimed here.

New frontend export tests first reproduced empty links and missing JSON download.
The native URI builder, label parser and shared QR panel now offer official JSON
with SOCKS on loopback, using the exact shared profile's endpoint and credentials.
All 13 locales include download and format guidance. Targeted export/share/QR/
i18n tests passed 78/78 across six files in 14.72s after adding an explicit
accessible button name (`/tmp/3x-ui-mieru-export-ui-green-fixed.log`). Earlier
startup-permission, test-fixture and accessible-name failures are retained under
`/tmp/3x-ui-mieru-export-ui-*`. Full current backend/frontend checks and real
browser download-to-client acceptance remain open.

The corrected full subscription run, `go test -race -shuffle=on -p 1
./internal/sub`, passed 475 top-level and 511 nested tests in 45.291s with the
managed core and official Mihomo v1.19.30 enabled. The two existing scale cases
N10000/N100000 were skipped, not counted as acceptance. Log:
`/tmp/3x-ui-mieru-full-sub-race-fixed.jsonl`. A redundant embedded selector in the
new integration test was then simplified; affected Go lint passed with zero
issues (`/tmp/3x-ui-mieru-export-go-lint-fixed.log`).

Current frontend typecheck and lint passed. The full serial frontend regression
passed all 1,860 tests in 189 files in 483.72s, with unchanged per-test deadlines;
the production build then passed in 2.72s. Logs:
`/tmp/3x-ui-mieru-export-ui-{typecheck-fixed,lint,full-test,build}.log`.
The official mieru v3.38.0 CLI also accepted native JSON produced by the actual
frontend export function, preserving escaped credentials, IPv6 and both transport
bindings (`/tmp/3x-ui-mieru-json-validation/import.log`). This validates the format;
browser download-to-running-client traffic is tracked separately and remains open.

### Native public browser and bulk lifecycle acceptance (2026-09-29)

The real browser fixture now passes all three native configurations: TCP, UDP,
and both. It creates the inbound and password client through the UI, chooses a
custom advertised loopback address, downloads JSON from Client Information's QR
popover, imports that file with official mieru v3.38.0, and runs the actual CLI.
The original export's loopback SOCKS port 1080 is checked; only that local port
is relocated in the fixture to avoid collision. The public server bindings and
credentials remain exactly those downloaded.

Each configuration transfers a 16,384-byte TCP echo plus a 1,024-byte UDP echo
through the native client, managed server and Xray router. Panel raw totals are
17,408 B up and 17,408 B down, and billed usage is exactly 52,224 B at 1.5x.
The editor saves 32,768/65,536 B/s directional rates. This small echo is not the
required sustained rate acceptance. Desktop/mobile runtime badges show two
authenticated logical sessions. Reducing quota through the authenticated client
API to 40,000 B marks the account depleted and closes the existing TCP stream
within the test's unchanged two-second window after the API response. The UI
then disables the listener; canonical APIs delete the owned client and inbound.
No page errors occur. Full quota exhaustion, sustained UDP cutoff, global rates
and the remaining performance/fault matrix stay open.

Command (after the full frontend build and current panel build):

```sh
XUI_E2E_PANEL=/tmp/3x-ui-mieru-browser-panel \
XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_MIERU_E2E_BINARY=/tmp/3x-ui-mieru-v3.38.0 \
node frontend/scripts/mieru-client-e2e.mjs
```

Result: exit 0, three configurations passed;
`/tmp/3x-ui-mieru-browser-e2e-quota-fixed.log`. Official CLI was built with its
own pinned v3.38.0 module dependencies (`go build -mod=readonly -p 1 ./cmd/mieru`);
binary SHA-256 is
`480ea2494f6a0852167654e2f9d0fd389ef2dee0ea3f3460201b0d40570768db`.

Earlier failed fixture attempts remain in `/tmp/3x-ui-mieru-browser-e2e*.log`:
missing environment before startup; incorrect relative password selector and
share selector; an incorrect expectation that the default loopback share host
would stay an IPv4 literal (existing behavior uses `localhost`); exact object
comparison rejected the official CLI's additional derived password hash; the
initial reader spun when a shaped reply arrived in partial chunks, starving the
Node event loop; an initial native admission closed during asynchronous policy
replacement; and the quota test sent the read-model `allowedIPs` string to an
API expecting an array. The fixture now accumulates partial chunks, waits for
actual native admission within two seconds, and sends the update API's input
shape. These changes do not relax data totals, rate bounds or cutoff deadlines,
and do not change production code. The stalled fixture's verified process tree
was stopped and its exact temporary directory removed; unrelated services were
untouched.

`go test -race -p 1 ./internal/web/service -run '^TestMieruBulk' -count=1`
passed 3 tests, no skips, in 5.109s, including SQLite and PostgreSQL. It checks
two clients on two native inbounds, independent stable policy identity, exact
usage/rate retention, denial of unmanaged bulk attachment, manual disable across
quota increases, detach/reattach, idempotency, deletion/recreation with fresh
ownership, and atomic rejection of a batch containing invalid UTF-8 credential
length. Log: `/tmp/3x-ui-mieru-bulk-first.jsonl`.

An isolated Go overlay removing mieru from canonical managed-ownership detection
made the new bulk lifecycle test fail at the unsupported-policy assertion in
0.466s (`/tmp/3x-ui-mieru-bulk-mutation.log`). Production source was unchanged.
The final formatted browser script tightened SOCKS startup to an absolute
two-second deadline and again passed all three configurations
(`/tmp/3x-ui-mieru-browser-e2e-final.log`). Script lint and `git diff --check`
also passed.

The subsequent complete affected backend race/shuffle run did **not** pass:
`go test -race -p 1 -shuffle=on -count=1 -timeout=10m ./internal/mieru
./internal/web/...` returned exit 1. It passed 1,391 top-level and 1,371 nested
tests, failed 7 top-level / 15 nested tests, and skipped 15 top-level / 9 nested
tests. Sixteen test packages passed, the main service package failed (500.228s),
and two packages had no tests. Log:
`/tmp/3x-ui-mieru-public-web-race-full.jsonl`. The chained static check did not
run after that failure.

All failures used the older accounting fixture that claimed a ledger on VLESS,
then called canonical synchronization to add or edit unsupported local/remote
attachments. The new guard correctly refuses those paths. The accounting
fixture now uses valid local mieru credentials; its initial traffic projection
is created before canonical ownership is claimed. The remote reset fanout case
directly seeds a pre-existing snapshot membership, since its subject is reset
delivery, not authorization of a new unsupported attachment. Original usage,
concurrency and cutoff assertions are unchanged; focused and full reruns remain
required. Skips cover opt-in scale, dedicated PostgreSQL failure/serialization
fixtures, the non-Linux update guard and Xray golden fixtures with unavailable
assets; none establish acceptance.

The accounting fixture correction passed the focused race run (23 top-level
cases / 30 subtests, zero skips, 43.711s;
`/tmp/3x-ui-mieru-accounting-fixture-fixed.jsonl`). The original failed full run
above remains failed; a fresh full regression is required.

Port reservation follow-up reproduced missing SSH upstream bridge, routed
MTProto bridge, custom template inbound, and metrics listener conflicts through
actual saves. Reverse `SaveXraySetting` checks also failed all nine ownership
cases before the fix. Both directions now check and persist within one
transaction. Reservations include disabled owners and respect transport,
listen address, and node scope. Core integer-port configuration remains the
existing supported format; a draft range test was removed because the panel's
actual `InboundConfig.Port` is an integer and existing validation rejects ranges.

Extra tests reproduced six socket interpretation errors: IP address aliases and
core dokodemo UDP settings (including legacy/array forms). Those checks now
follow the pinned core's socket semantics. The first concurrent save run then
found a PostgreSQL failure: both template and native inbound could commit when
the traffic writer was inactive. SQLite already serialized these transactions.
A schema-scoped PostgreSQL transaction advisory lock now covers reservation
checks and releases on commit or rollback. The corrected race run passed
52 top-level tests / 100 subtests, zero skips, 23.373s, including 20 concurrent
save rounds on each database. Evidence:
`/tmp/3x-ui-mieru-template-ports-red.log`,
`/tmp/3x-ui-mieru-template-socket-red.log`,
`/tmp/3x-ui-mieru-ports-race-green.jsonl` (failed PostgreSQL run), and
`/tmp/3x-ui-mieru-ports-race-fixed.jsonl` (passed).

The fresh complete affected backend run passed:
`go test -race -p 1 -shuffle=on -count=1 -timeout=10m ./internal/mieru
./internal/web/...` — 1,406 top-level tests and 1,426 subtests, 17 tested
packages passed (main service 508.436s, native mieru 122.964s), two packages
had no tests. Fifteen top-level cases and nine subtests explicitly skipped:
opt-in scale runs, separate PostgreSQL fault-injection fixtures, unavailable
DNS/routing golden assets, and the non-Linux update guard. These are not passes.
Log: `/tmp/3x-ui-mieru-public-web-race-final.jsonl`. The combined command exited
nonzero only because the following static check found one `gofumpt` formatting
issue in the new template reservation file. This full run precedes the final
localhost and own-MTProto-listener boundary corrections described next.

The last two focused RED tests reproduced core `localhost` versus IPv4 loopback
aliasing and an MTProto listener colliding with its own routing bridge.
After fixing both, the expanded port/MTProto/routing-save race run passed
54 top-level tests / 101 subtests, zero skips, 23.849s
(`/tmp/3x-ui-mieru-ports-final.jsonl`). Both failed tests are retained, with RED
logs `/tmp/3x-ui-mieru-localhost-port-red.log` and
`/tmp/3x-ui-mieru-final-ports-red.log`. The subsequent static check required
one further multiline-literal formatting correction; that change has no
runtime effect.

Final affected static analysis passed with zero issues:
`golangci-lint run --timeout 10m ./internal/mieru/... ./internal/web/...
./internal/sub/...` (`/tmp/3x-ui-mieru-public-backend-lint-final.log`).
`git diff --check` also passed. The public increment is ready for its backend
and frontend commits; this does not close Task 6 or the full requirements.

### Public mixed SSH/mieru rate acceptance — follow-up

Backend commit `40145cb9` and frontend commit `7523149c` were pushed to the
approved feature branch; independent `git ls-remote` matched
`7523149c16e0046b198500166af1d0b4d846ad89`. These are scoped integration
milestones; public shared-rate acceptance remained open at that point.

The new production harness attaches two same-IP canonical clients to one SSH
and two mieru listeners, with two simultaneous duplex streams per listener
and client. It uses real OpenSSH and the official mieru client, the actual
policy API and managed core, the existing rate bounds and 2s live-change
criterion, exact 2x ledger arithmetic, and core restart. The first race run
**failed**: SQLite/TCP and PostgreSQL/TCP+UDP passed, but SQLite/UDP delivered
262,143 upload bytes for the unchanged second client over 1.501s, exceeding
its 221,621-byte upper bound at 131,072 B/s during the other client's live
rate increase. Log: `/tmp/3x-ui-public-mixed-rates-first.jsonl` (75.804s).
This is not a complete performance pass. The next diagnostic run compares
ledger admission deltas with receiver deltas to locate scheduling or buffered
burst behavior; rate bounds and measurement durations remain unchanged.

Three SQLite/UDP diagnostic race repetitions passed (56.917s), but ten
further repetitions **failed**: four passed, four failed the unchanged
unlimited-baseline requirement, and two exceeded the initial download bound.
One failure delivered 228,556 bytes while 189,235 bytes were admitted during
the diagnostic interval, exposing previously admitted in-flight data; this
does not by itself establish the complete cause. Logs:
`/tmp/3x-ui-public-mixed-rates-udp-diagnostic.jsonl` and
`/tmp/3x-ui-public-mixed-rates-udp-reproduce.jsonl` (113.814s).

The normal-build comparison (`go test -p 1 ./internal/web/service -run
'^TestClientPolicyProductionSSHAndMieruShareRates' -count=3 -json`, with the
managed-core and PostgreSQL fixture environment) also **failed**, 213.16s:
ten transport/database subtests passed and two failed, no skips. SQLite/UDP
sent 222,822 upload bytes against the 221,566-byte upper bound in 1.500s for
the unchanged client during its peer's live increase. PostgreSQL/TCP once
failed official-client SOCKS5 response negotiation after core restart with
`TIMEOUT`. Log: `/tmp/3x-ui-public-mixed-rates-no-race-comparison.jsonl`.
These failures cannot be dismissed as race instrumentation overhead.

Inspection also found diagnostic timing skew: receiver snapshots straddled
SQL reads while their clock did not, and validation could delay later end
snapshots. The corrected harness collects all diagnostic reads before the
receiver clock and all receiver end counters before validation or SQL. The
rate bounds, baseline, settling periods, measurement duration and 2s live
update requirement are unchanged. This measurement correction is not a
production rate fix; the first failure preceded those diagnostic probes.

The aligned normal-build rerun still **failed** (231.205s): eleven subtests
passed, one PostgreSQL/TCP restart handshake timed out, zero skips. Log:
`/tmp/3x-ui-public-mixed-rates-clock-aligned.jsonl`; failure stack:
`/tmp/3x-ui-mieru-dial-3555727615.stacks`. The stack shows native TCP underlays
waiting to deliver segments to full per-session staging channels. The pinned
official client's default multiplex factor is one; its stream sender does not
use the packet transport's per-session receive-window control. A deterministic
multiplexed-handshake reproduction and an explicit compatibility decision remain
pending; no timeout extension or silent test configuration change was made.
The upstream wire contract is pinned at
[mieru v3.38.0](https://github.com/enfein/mieru/blob/v3.38.0/docs/protocol.md);
the observed TCP behavior is grounded in that version's
[stream sender](https://github.com/enfein/mieru/blob/v3.38.0/pkg/protocol/session.go)
and [default client configuration](https://github.com/enfein/mieru/blob/v3.38.0/pkg/appctl/appctlcommon/client.go).

A separate deterministic regression occupied the real SQLite connection for
250ms while six authenticated flow writers waited for durable admission.
All four stream/datagram and upload/download cases failed: 26,212 bytes reached
the destination within 60ms of database recovery, above the fixed rate-plus-burst
bound of approximately 10,750 bytes. Cause: grants were paced before admission,
then accumulated while admission waited. A second shared direction bucket at
the post-admission write boundary made all four cases pass; the full policyflow
and clientpolicy race suites passed (16.710s and 9.560s). This preserves durable
admission and bounds without serializing blocking writes behind one mutex.
Evidence: `/tmp/3x-ui-rate-trace/admission-four-red.log` and
`/tmp/3x-ui-rate-trace/admission-green.jsonl`.

This scoped correction does **not** close public rate acceptance. Two normal
mixed repetitions after it **failed** (141.191s), six subtests passed, two failed,
zero skipped: one SQLite/UDP live-increase window delivered 157,284 upload bytes,
15 bytes below its unchanged 157,299-byte lower bound; one PostgreSQL/TCP restart
again timed out opening a multiplexed official-client session. Log:
`/tmp/3x-ui-rate-trace/public-delivery-gate.jsonl`. A separate timing-instrumented
UDP race run had three passes and two insufficient-baseline failures, 67.463s
(`/tmp/3x-ui-rate-trace/public-observed.jsonl`); it did not reproduce a burst.

The post-admission correction's complete affected race regression passed:
`go test -race -p 1 ./internal/policyflow ./internal/clientpolicy
./internal/sshtunnel ./internal/mieru -count=1 -json`, with the managed-core
fixture enabled — 64 top-level tests, 89 subtests, zero skips, all four packages
passed (20.403s, 9.560s, 14.735s, 120.992s respectively). This includes four
additional cases changing an unlimited rate while admissions were already
queued, then checking the same recovery burst bound. Log:
`/tmp/3x-ui-rate-trace/shared-controller-regression.jsonl`. This is scoped
controller/adaptor regression evidence; the public failures above remain open.

The fixed-underlay TCP diagnostic subsequently confirmed the mechanism using
the pinned official protocol client: a finite 1 MiB upload at 16 KiB/s kept a
second handshake on the same TCP underlay blocked for 250ms; another TCP underlay
for that same client completed its handshake within 1s. Removing the rate cap
released the first underlay's pending handshake within 2s. Peak native buffering
was 294,912 bytes, inside the existing finite bounds. No server queue was enlarged.
The isolated experiment passed in 4.090s:
`/tmp/3x-ui-rate-trace/tcp-mux-diagnostic.log`; source and overlay are in the same
directory. Export compatibility work remains pending. This experiment establishes
the observed cause and an independent-connection alternative, not support for
bounded handshake latency under arbitrary TCP multiplexing.

The already published public SSH shared-rate tests also passed on SQLite and
PostgreSQL after the controller correction (two top-level tests, zero skips,
20.775s; `/tmp/3x-ui-rate-trace/published-ssh-regression.jsonl`). The panel build
passed (`go build -o /tmp/3x-ui-rate-guard-panel .`), the affected controller/limiter
static analysis reported zero issues, and `git diff --check` passed.

### Explicit TCP scheduling in managed mieru exports

The supported exported TCP configuration now uses `MULTIPLEXING_OFF`; UDP-only
profiles retain the upstream default. Official TCP+UDP profiles apply the choice
at profile scope; Mihomo emits separate transport nodes and applies it only to
TCP. The [compatibility decision](mieru-integration-plan.md#tcp-client-scheduling-compatibility)
records the confirmed TCP backpressure behavior, extra connection cost and
manual-profile limitation. No rate, burst, buffer or handshake limit was widened.
The retained official-protocol characterization passed with race detection in
6.422s (`/tmp/3x-ui-rate-trace/multiplex-boundary-test.log`).

Backend exports first failed four TCP/both cases, then passed two top-level
cases/six subtests, zero skips. Frontend's first attempt could not open Vitest's
loopback listener (`EPERM`) and ran no assertions. The authorized loopback rerun
then failed six tests for the missing URI/JSON scheduling field; after the fix,
all 16 tests in both affected files passed (6.42s), as did TypeScript and lint.
Logs: `/tmp/3x-ui-rate-trace/multiplex-exports-red.log`,
`multiplex-exports-green.log`, `multiplex-ui-red-localhost.jsonl.log`,
`multiplex-ui-green.log`, `multiplex-ui-types.log`, `multiplex-ui-lint.log` in
that same directory. Unsupported or duplicate explicit multiplexing options
are rejected; legacy panel TCP links gain the explicit setting when downloaded.

The complete subscription race suite with the actual managed core and Mihomo
v1.19.30 passed: 475 top-level tests, 511 subtests, 44.559s. Two opt-in scale
subtests skipped and are not passes. Both native transport choices carried real
TCP/UDP payload through official clients and Mihomo after importing the generated
profiles. Log: `/tmp/3x-ui-rate-trace/multiplex-sub-full.jsonl`.
Fresh full frontend/build/browser and public mixed-rate checks are tracked
separately; this export result alone does not close those acceptance items.

Using the explicitly exported TCP scheduling configuration, two complete normal
mixed-rate repetitions passed: four top-level tests/eight database/underlay
subtests, zero skips, 147.198s. Log:
`/tmp/3x-ui-rate-trace/multiplex-public-rates.jsonl`. The independent-client,
multi-listener, live-existing-connection, exact 2x billing and core-restart
assertions all ran with the original limits. The earlier UDP window 15 bytes
below its lower bound has not been attributed to TCP multiplexing; this result
does not erase that unresolved observation or establish the remaining sustained
UDP-payload acceptance.

Fresh production frontend and embedded-panel builds passed (Vite 2.73s).
The complete browser script passed for TCP, UDP and both: actual downloaded
JSON preserved the explicit TCP option through official v3.38.0 CLI import,
carried real TCP/UDP echoes, retained exact 17,408-byte counters in each direction
and 52,224 billed bytes at 1.5x, showed two sessions on desktop/mobile, and closed
existing TCP within 2s of reduced-quota API completion. Disable and deletion
also completed for all three settings. Logs:
`/tmp/3x-ui-rate-trace/multiplex-ui-build.log`, `multiplex-panel-build.log` and
`multiplex-browser.log`. The current full frontend and affected static checks
are separate from this real-browser evidence.

Final checks for this export correction: affected Go lint reported zero issues
(`/tmp/3x-ui-rate-trace/multiplex-backend-lint-final.log`). Its first run found
two import-group formatting issues in new test files; adding the missing blank
separators resolved them without behavioral changes. The fresh complete serial
frontend run passed all 189 files and 1,868 tests in 482.90s, with no skipped
tests reported (`/tmp/3x-ui-rate-trace/multiplex-ui-full-test.log`). TypeScript,
frontend lint, production frontend/panel builds, real browser/CLI and full
subscription checks are recorded above. `git diff --check` passed. The original
whole-task and remaining mieru acceptance requirements remain open.

### Public mieru natural-quota acceptance

The post-admission delivery diagnostic ran ten normal-build SQLite/UDP
repetitions with the original public mixed-rate bounds and windows:
`go test -p 1 -overlay=/tmp/3x-ui-rate-trace/overlay.json ./internal/web/service -run '^TestClientPolicyProductionSSHAndMieruShareRates$/udp$' -count=10 -json`.
All ten top-level and ten underlay cases passed, with no skips, in 164.643s;
log: `/tmp/3x-ui-rate-trace/udp-delivery-diagnostic.jsonl`. The overlay only adds
trace events. This did not reproduce or explain the earlier 15-byte lower-bound
failure; that observation remains open.

`TestMieruInboundNaturalQuota` and its PostgreSQL counterpart exercise the
normal service/Runtime, official mieru v3.38.0 client, native TCP/UDP underlays,
and pinned managed Xray bridge. Each uses two persistent TCP payload flows and
two persistent UDP associations belonging to one canonical client, competing
for the same remaining quota. TCP profiles use the published
`MULTIPLEXING_OFF` export setting. A separate authenticated client on the same
loopback address must continue using its existing flows after exhaustion.

The predeclared workload sends one unacknowledged echo block per flow: 16 KiB
on each TCP connection and 8 KiB on each UDP association. Thus at most
`2 * (2 * 16384 + 2 * 8192) = 98304` raw bytes can be admitted but absent from
the independent upload-target plus download-client observations. This bound
includes hidden buffers by limiting outstanding application data at its source;
it does not establish the general unrestricted-sender buffer bound. Each case
must admit exactly 8 MiB of additional raw payload, with literal quotas of
4/8/12/16 MiB for multipliers 0.5/1/1.5/2. No percentage allowance is used for
billing: billed bytes must equal quota exactly, with no fractional remainder.
Both payload types must independently deliver at least 64 KiB.

A 20ms ledger observer records the last known pre-exhaustion time. Every existing
flow must end without a timeout within 1250ms of that conservative timestamp;
new TCP/UDP admissions must fail. The test then restarts the actual core and
checks that rejection and exact durable usage persist, while the unrelated
client can reconnect. This is a core lifecycle restart, not a panel-process or
host restart. Manual-disable/expiry combinations have separate coverage.

Initial SQLite/TCP, multiplier 2: PASS in 6.804s; raw upload/download
4202496/4186112 bytes, independently observed 4202496/4186112, billed delta
16777216, zero flight difference and cutoff upper bound 16.600006ms. Removing
only the canonical quota limit through a `/tmp` Go overlay made the unchanged
case fail: `natural quota did not close all existing flows within 1250ms`
(6.986s). Production source was never changed by this mutation. Logs:
`natural-quota-first.log` and `natural-quota-mutation-red.log` under
`/tmp/3x-ui-rate-trace/`. The complete dialect/underlay/multiplier race matrix
is recorded below once terminal; no whole Task 6 completion follows from this
bounded workload.

Full natural-quota race matrix:

```sh
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
go test -race -p 1 ./internal/web/service \
  -run '^TestMieruInboundNaturalQuota(_Postgres)?$' -count=1 -json
```

PASS, 157.077s: 2 top-level and 16 subtests, zero skips, no race report.
Every case admitted exactly 8388608 additional raw bytes and reached its
literal billed quota. Independent upload/download counts equaled admission
counts in all sixteen cases: zero observed flight difference and zero billed
overshoot. Conservative observed closure upper bounds ranged from 8.022482ms
to 28.493029ms, below the predeclared 1250ms requirement. Existing unrelated
flows survived each exhaustion; new exhausted-client TCP/UDP connections
failed before and after the real core restart. Log:
`/tmp/3x-ui-rate-trace/natural-quota-matrix.jsonl`.

Additional public restriction and fault checks:

```sh
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
go test -race -p 1 ./internal/web/service \
  -run '^TestMieruInbound(QuotaResetKeepsIndependentRestrictions(_Postgres)?|KilledCoreProtectsAndRecovers)$' \
  -count=1 -json
```

PASS in 53.801s: 3 top-level / 6 subtests, zero skips or races. Both database
engines and native underlays preserve manual disable and expiry across traffic
reset and quota increase; explicitly enabling still-expired clients remains
insufficient to reconnect. Extending expiry permits actual TCP/UDP exchanges.
Automatic renewal resets durable accounting and extends expiry while retaining
manual disable; explicit enable subsequently restores actual traffic.

The Linux fault test identifies only the owned Xray child by its exact binary
and unique temporary config path, sends SIGKILL, and checks its actual OS exit
status. Both native underlays close old TCP/UDP payload flows within 1250ms,
refuse new flows, and release the public listener. Restart recovers traffic
without changing pre-crash raw/billed/remainder values. This expands the earlier
SIGTERM-based `Process.Stop()` coverage; it does not claim panel-process or host
restart validation. Log:
`/tmp/3x-ui-rate-trace/mieru-restrictions-crash-first.jsonl`.

Two additional `/tmp` mutations were rejected by the unchanged tests: disabling
native runtime core-state reconciliation caused an old public TCP flow to time
out instead of closing; copying the legacy reset auto-enable behavior into the
managed reset path changed the canonical manual-disable flag. Both targeted
cases failed as intended (2 top-level / 2 subtest failures, 8.308s), logged in
`/tmp/3x-ui-rate-trace/mieru-state-mutations-red.jsonl`. Neither mutation changed
repository source. Final restored-source public regression is recorded below.

Final restored-source regression:
`go test -race -p 1 ./internal/web/service -run '^TestMieruInbound' -count=1 -json`
with the same managed-core and PostgreSQL environment passed in 261.488s:
15 top-level / 43 subtests, zero skips, no race reports. This includes every
existing public mieru fixture consumer and all new cases. In this run the
sixteen quota cases again had exact independent receive counts, zero flight
difference/overshoot, and closure bounds of 7.175717–32.881171ms. The affected
`golangci-lint run ./internal/web/service/...` then passed with zero issues.
Logs: `/tmp/3x-ui-rate-trace/mieru-public-acceptance-final.jsonl` and
`mieru-quota-lint.log`. This increment changes test utilities, acceptance tests
and evidence documents only; prior frontend/build evidence is unchanged.

### Canonical attachment without a subscription ID

The new public UDP payload workload exposed an existing service defect before
any measurement: `Attach` rejected an already selected canonical client with
an empty subscription ID as a duplicate email. The same public service call
failed for mieru and VLESS on SQLite and PostgreSQL. The new focused regression
also checks repeated attachment, unchanged credentials/policy ID/subscription
ID, and continued duplicate-email rejection by the ordinary add endpoint.

Explicit attachment now carries the selected database owner through the private
add pipeline. It preserves an empty subscription ID and rechecks the selected
record's policy ID, email and subscription ID under its transaction lock before
writing membership or settings. Ordinary create, add and portable import retain
the existing duplicate validation. No model/schema or public API field changes.

Focused empty-ID RED evidence is in
`/tmp/3x-ui-rate-trace/attach-identity-red-isolated.jsonl`: all four dialect/
protocol cases rejected valid attachment. The first PostgreSQL harness reused
one schema across protocol subcases; it was corrected to use a schema per leaf
before recording this evidence. The first identity-replacement fixture attempted
a GORM write to the create-only policy-ID field, which did not modify the row;
its failed assertion is not evidence of an actual replacement race. The corrected
fixture performs the replacement explicitly and checks affected rows and stored
identity. Removing only the transaction identity comparison through a `/tmp`
overlay then caused both SQLite/PostgreSQL replacement tests to accept the stale
operation (meaningful RED, 1.075s). Restored production source passed all four
top-level / four subtests with race checking, no skips, in 8.174s:

```sh
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
go test -race -p 1 ./internal/web/service \
  -run '^TestAttach(CanonicalClientWithoutSubscriptionID|RejectsReplacedCanonicalIdentity)' \
  -count=1 -json
```

Logs: `attach-replacement-mutation-red.jsonl` and
`attach-identity-green-final.jsonl` under `/tmp/3x-ui-rate-trace/`.

The pending UDP payload rate workload then completed cross-inbound attachment,
but its first normal-build SQLite/TCP-underlay unlimited upload baseline was
122791 B/s, below the predeclared 524288 B/s minimum; the run failed in 14.221s
before rate acceptance. No baseline threshold or rate tolerance was changed.
That unfinished workload is retained separately while the canonical attachment
fix receives its own regression/commit. Its baseline capacity must be diagnosed;
this result is not proof of correct sustained UDP shaping.

The same defect was then reproduced at `BulkAttach`: four empty-ID dialect/
protocol cases failed, and two tests with a confirmed policy-identity replacement
incorrectly committed the whole batch. The fix now shares the trusted-owner
pipeline between single and bulk attachment. Each selected record is validated,
then locked in ID order with bounded SQL chunks and rechecked before committing
one payload per inbound. Bulk tests cover two clients, partially populated targets,
repeated target IDs, retries and atomic rejection of a stale identity.

The initial bulk RED run failed all four top-level/four subtests in 2.314s
(`bulk-attach-identity-red.jsonl`). The first combined single/bulk focused race
run passed eight top-level/eight subtests, without skips
(`all-attach-identity-green.jsonl`). Subsequent broader regression results are
recorded below. These logs are under `/tmp/3x-ui-rate-trace/`.

Expanded related-path race checking passed **80 top-level / 90 subtests** in
102.688s, with three explicit skips: the two real-core portable SSH restoration
tests (core environment not set for this focused run) and the opt-in PostgreSQL
bulk scale test. Command: `go test -race -p 1 ./internal/web/service -run
'^Test(Bulk|Attach|Portable|Import)' -count=1 -json` with `XUI_TEST_PG_DSN`.
The service-package `golangci-lint run ./internal/web/service/...` reported zero
issues, and `go build -o /tmp/3x-ui-attach-identity-panel .` passed. Logs:
`attach-related-race.jsonl`, `attach-backend-lint.log`, `attach-panel-build.log`.

A subsequent instrumented UDP baseline again failed all four client/direction
measurements (97266–122863 B/s, required 524288 B/s). Both policy rates were
zero, all eight flows remained alive, payloads were intact, and every receiver
advanced. During the 400ms window, 86 durable-admission calls consumed a combined
738ms including database waiting; destination writes consumed about 3.48ms.
This points to admission throughput, but does not yet separate queueing from
transaction work or commit cost. The diagnostic then stalled in its manual
cleanup: the official session close waited behind a blocked TCP output lock.
A captured goroutine dump identifies the fixture's session-before-client stop
order. The pending fixture now stops the official client/underlay first; that
change still requires execution. No production rate fix or UDP acceptance is
claimed from this diagnostic (`udp-admission-baseline-diagnostic.jsonl`).

Final full-root regression for the single/bulk implementation passed **53 test
packages, 2656 top-level / 4542 subtests**, with **28 top-level / 14 subtest skips**
and seven packages containing no tests. No failures; the service package took
440.36s. Command: `go test -p 1 -shuffle=on -count=1 ./... -json`,
with `XRAY_E2E_BINARY` and `XUI_MANAGED_XRAY_E2E_BINARY` pointing to the pinned
managed core, `XUI_MIHOMO_E2E_BINARY=/tmp/3x-ui-mihomo-v1.19.30`, and the same
`XUI_TEST_PG_DSN`. Log: `/tmp/3x-ui-rate-trace/attach-bulk-full-go.jsonl`.
The pending UDP payload rate fixture is still outside this committed suite.

The skips are retained explicitly (none count as passes):

- `internal/database`: `TestMigrate_Postgres`, `TestHostAutoMigrateCreatesColumns_Postgres`, `TestClientWeeklyRenewMigration_Postgres`.
- `internal/sshoutbound`: `TestConnectorOpenSSHIPv6Target`, `TestBridgeOpenSSHForwardingAndPinFailure`, `TestConnectorOpenSSHForwardingAndStrictHostPin`.
- `internal/sub`: `TestGetSubsScale/N=10000`, `TestGetSubsScale/N=100000`.
- `internal/web/controller`: `TestUpdatePanel_UnsupportedPlatformReturnsNoRunId`.
- `internal/web/job`: `TestCheckClientIpScale/N=10000_single`, `TestCheckClientIpScale/N=10000_spread50`, `TestCheckClientIpScale/N=100000_single`, `TestCheckClientIpScale/N=100000_spread50`.
- `internal/web/service`: `TestDelAllClientsPostgresScale`, `TestSyncInboundPostgresScale`, `TestSetClientLimitHwidIsSerializedWithSyncInbound`, `TestSSHUpstreamPolicyRatesAndRestart`, `TestSSHOutboundRunsThroughProductionXray`, `TestAddTrafficPollScale`, `TestAddInbound_PostgresCommitFailureMakesNoRuntimeCall`, `TestAddDelClientPostgresScale`, `TestGetXrayConfigScale`, `TestWsPayloadScale`, `TestSSHUpstreamPolicyQuotaAndLifecycle`, `TestBulkOpsPostgresScale`, `TestSSHUpstream_Postgres/runtime`, `TestSSHUpstream_Postgres/rates`, `TestSSHUpstream_Postgres/quota`, `TestGetClientTrafficByEmailABScale`, `TestUpdateInbound_PostgresCommitFailureMakesNoRuntimeCall`, `TestGoldenRoutingFixturesBuildInXray/rule/balancer-routed`, `TestGoldenRoutingFixturesBuildInXray/rule/full`, `TestGoldenDNSFixturesBuildInXray/dns/full`, `TestGoldenDNSFixturesBuildInXray/dns-server/full`, `TestGoldenDNSFixturesBuildInXray/dns-server/legacy-expectips`, `TestAllAPIsPostgresScale`, `TestGroupAndListPostgresScale`.
- `internal/web/service/outbound`: `TestSSHProbeResolvesNativeProxyChain`, `TestSSHProbeUsesRequestedPinAndIsolatesInvalidContext`, `TestSSHProbeUsesRealCoreAndOpenSSH`, `TestSSHProbeFailureCleansOwnedResources`, `TestAddTrafficReturnsDeferredCommitFailure`.

## Public UDP payload rates and durable admission batching (2026-09-29)

This continues the previously failed public UDP workload; it does not replace
those recorded failures. Transaction tracing attributed 84 admissions during a
400ms window to 751.7ms cumulative wall time: 370.4ms waiting for the shared
SQLite writer, 94.7ms transaction body, and 286.6ms remaining begin/commit work.
These are overlapping instrumented totals, not independent per-request latency.
Batching without a coalescing interval improved capacity but still failed the
524288 B/s baseline (first direction 366739 B/s). A bounded 1ms collection window
was then added outside locks, with at most 32 requests per durable transaction.

The original fixture stopped unbounded baseline connections before opening new
limited connections. Instrumentation found two old sessions still consuming
16384 bytes of the first client's upload budget during the first limited window;
together with the new flows' 32768 bytes, aggregate delivery matched 32 KiB/s.
Explicitly requiring old public sessions to disappear within 2s failed on all
four database/underlay combinations. Natural client-exit cleanup remains open.
The final workload instead uses the same four associations per client from the
unlimited baseline through all live policy changes. Each direction/association
keeps one 2048-byte packet outstanding until an independent receiver acknowledges
it. Actual payload still crosses the native protocol, private bridge and core.
This bounds the workload from its start and adds existing-flow 0-to-limit
coverage; no baseline threshold, measurement window or rate tolerance was eased.

Official mieru v3.38.0 clients (TCP exports use MULTIPLEXING_OFF) and the pinned
managed core run through production ClientService/Runtime. Both SQLite and
PostgreSQL, both native underlays, two same-IP clients and two inbounds are used.
The baseline is 400ms after 200ms startup, requiring every client/direction above
524288 B/s (8x the larger cap). Caps are 32768/65536 B/s and their inverse; the
first client changes twice while the second remains unchanged. Each 1.5s window
requires at least 80% of the cap and at most 106% plus 100ms burst, one 2048-byte
packet debt and four outstanding packets. Initial and live changes complete
the acceptance window within 2s. Every association must progress without
corruption or termination. Exact 2x durable billing and core restart are checked.

`go test -p 1 ./internal/web/service -run
'^TestClientPolicyProductionMieruUDPPayloadRates' -count=1 -timeout=180s -json`
with the managed-core path and PostgreSQL DSN above passed **2 top-level / 4
subtests, no skips**, in 84.032s. All 16 baseline directions measured
911312–1246780 B/s; all 64 capped direction/windows measured 32745–65536 B/s.
Log: `/tmp/3x-ui-rate-trace/public-udp-bounded-rates-matrix.jsonl`.

Mutation checks use temporary Go overlays and leave production source intact.
Restoring per-request commits made the final fixture fail its baseline at
158437 B/s (12.401s, `public-udp-bounded-no-batch-red.jsonl`). Bypassing both
UDP pacing gates failed the first ceiling: 1447936 bytes versus 65618 allowed
in 1.5s (14.253s, `public-udp-pacing-disabled-red.jsonl`). These are expected
negative controls, not passing rate results.

New actual-SQLite controller tests cover queued duplex admission, canceled
requests, exact 1.5x accounting, a failed final meter write rolling back the
account and cursor, other-client continuity, Close while database access is
blocked, replacement without inherited pending bytes, and near-int64 overflow.
The old implementation required 16/8 cursor writes instead of at most three;
that meaningful RED preceded batching. Another meaningful RED showed aggregate
overflow incorrectly faulting an individually admissible packet before fallback;
the error is now recorded after that fallback. A commit-error-to-success mutation
caused actual payload leakage and failed. Full policyflow race/shuffle passed
**13 top-level / 14 subtests, no skips**, in 20.506s
(`admission-batch-final-policyflow.jsonl`). Broader restored-source regression
and build results are recorded below. The earlier 15-byte stream-window miss,
unrestricted buffer bounds and the complete Task 6 matrix remain open.

Restored-source regression after both negative controls passed:

- Public real SSH and mixed SSH/mieru TCP payload: `go test -p 1
  ./internal/web/service -run '^TestClientPolicyProductionSSH' -count=1
  -timeout=240s -json` — **4 top-level / 4 subtests**, no skips, 96.181s.
- Public natural quota: `go test -p 1 -race ./internal/web/service -run
  '^TestMieruInboundNaturalQuota' -count=1 -timeout=240s -json` — **2 top-level /
  16 subtests**, no skips, 120.358s.
- Shared controller/accounting/adapters: `go test -p 1 -race -shuffle=on
  ./internal/policyflow ./internal/database ./internal/sshtunnel ./internal/mieru
  -count=1 -timeout=300s -json` — **4 packages, 163 top-level / 150 subtests**
  passed. Three top-level tests skipped: `TestMigrate_Postgres`,
  `TestClientWeeklyRenewMigration_Postgres`,
  `TestHostAutoMigrateCreatesColumns_Postgres`; their separate opt-in migration
  environment was not provided. Package durations were 20.437s, 115.625s,
  14.666s and 123.430s respectively. No race failures.

These commands used both pinned managed-core environment variables and the
PostgreSQL DSN above. Logs under `/tmp/3x-ui-rate-trace/`:
`admission-mixed-tcp-regression.jsonl`, `admission-natural-quota-regression.jsonl`,
`admission-shared-backend-race.jsonl`. The isolated negative-control overlays are
absent from these commands.

Final full-root regression with the same managed-core, Mihomo and PostgreSQL
environment passed: `go test -p 1 -shuffle=on -count=1 ./... -json` — **53 test
packages, 2662 top-level / 4548 subtests**, zero failures, **28 top-level / 14
subtest skips**, and seven packages without tests. All 42 skipped test names
match the explicit list in the preceding canonical-attachment full-root record;
none count as passes. The service package took 501.924s. Its new UDP matrix also
passed both databases and underlays (SQLite 41.19s, PostgreSQL 42.33s).
Log: `/tmp/3x-ui-rate-trace/admission-full-go.jsonl`.

`golangci-lint run ./internal/policyflow/... ./internal/web/service/...` reported
**0 issues**. `go build -o /tmp/3x-ui-admission-batch-panel .` completed with exit
zero after the full-root tests. Logs: `admission-backend-lint.log` and
`admission-panel-build.log` in the same directory. No frontend source or assets
changed in this increment. These results establish the scoped batching/UDP
increment, not completion of the remaining native cleanup or full original goal.

## Native session closure and policy wait lifetime (2026-09-29)

The adapter passed only the authenticated credential generation's context into
first-use activation, TCP proxying and UDP policy writers. An individual native
session could finish and release all native buffers while its adapter still
waited for a rate grant or first-use callback. A read-only native `Session.Done`
signal now cancels a child context also owned by the credential generation.
The handler uses that context before first use and for both payload types.
The watcher is tracked with the handler's server workers. No wire format,
client binary, native queue limit, idle timeout or accounting rule changes.

Actual official-client tests cover both native underlays and both TCP/UDP
payloads: establish traffic, set upload to 64 B/s, queue 64 KiB, close the native
session, and require native resources and adapter presence to clear within 2s
after the official Close returns.
The fixture waits for the destination to observe the queued marker before
closing the session. The target drains that marker without replying, while echoing setup
and the independent healthy client's payload. This prevents a failed echo write
from independently canceling the flow and masking the missing lifetime link.
Two additional cases hold actual first-use activation until its context ends.
The meaningful RED failed all **2 top-level / 6 subtests** in 14.884s: native
sessions/buffers had been released but the adapter or callback remained alive
(`native-peer-close-sink-red.jsonl`). The initial echo-target version passed the
four queued-payload cases but failed both first-use cases; it did not isolate
queued cancellation (`native-peer-close-red.jsonl`).

The first fix passed the six cases under race in 4.292s. Broader race checking
then exposed an observation race in one new TCP case: the test read usage as soon
as presence disappeared, while an already-started six-byte admission transaction
was still completing. The documented admission semantics permit such committed
but undelivered charges. The final fixture opens/closes another flow for the same
policy before taking the stable-accounting snapshot. That uses the existing
admission/check mutex as a transaction barrier and also verifies the policy is
still usable. Native resource and presence assertions remain before the barrier;
the subsequent zero-growth assertion and the 2s cleanup bound are unchanged.
The full copied native protocol suite passed during that otherwise failed run;
public regression, lint and build did not run after the failure. Logs are under
`/tmp/3x-ui-rate-trace/`, including `native-peer-close-full-race.jsonl`.

The source patch was regenerated against checksum-verified official v3.38.0
files, preserving its unified-diff format. `python3 tools/managed-mieru/prepare.py
--verify` reproduced all native Go source and the license byte for byte.
Graceful authenticated session-close evidence is separate from silent UDP peer
loss or a TCP close frame behind a full transport queue. The earlier public
client-stop observation and physical-disconnect timeout bounds remain open.

With the final receiver-ready and transaction-barrier fixture, temporarily
restoring the old server context binding failed all **2 top-level / 6 subtests**
in 14.865s (`native-peer-close-observed-red.jsonl`). Restored source passed all
six under race in 4.290s (`native-peer-close-observed-green.jsonl`). The subsequent
complete `go test -p 1 -race -shuffle=on ./internal/mieru/... -count=1
-timeout=300s -json` passed **2 packages, 108 top-level / 1881 subtests**, no skips
or failures (`native-peer-close-final-race.jsonl`). Public Runtime/subscription
regression and static/build results follow below.

Normal-build public regression, with the pinned managed core, official-client
module, Mihomo and PostgreSQL environment above, also passed:
`go test -p 1 -shuffle=on ./internal/web/service ./internal/sub -run
'^Test.*Mieru|^TestManagedPolicy' -count=1 -timeout=600s -json` — **2 packages,
35 top-level / 84 subtests**, no skips or failures. This includes public shared
TCP and UDP rates, quota/restriction combinations, core recovery, canonical
lifecycle, shared-controller ownership and official subscription interoperation.
Service took 370.844s; subscription took 5.028s. The two full native race package
durations were 125.486s and 29.804s. These are affected-path checks after the
peer-close change; the preceding full-root result belongs to the batching commit.

`golangci-lint run ./internal/mieru/...` reported **0 issues**, and
`go build -o /tmp/3x-ui-peer-close-panel .` completed with exit zero. Final logs:
`native-peer-close-public-final.jsonl`, `native-peer-close-lint-final.log`,
`native-peer-close-build-final.log`. Original rate/quotas and unchanged-client
checks remain in force. Complete Task 6 and physical peer-loss detection remain
open; no frontend source changed in this increment.

## Native UDP idle maintenance and real transport loss (2026-09-29)

The native packet parser could starve the outer five-second cleanup ticker:
quiet reads use a process-fixed 60–120s timeout, while invalid packets restart
that read inside the parser. A managed UDP reader now checks the same ticker
inside the parser and caps each read at five seconds. Already-expired managed
sessions close with a timeout error before removal, avoiding the graceful-close
queue wait on a vanished peer. Idle TTL remains 60s; unmanaged/client behavior,
wire format, queue bounds and policy/accounting rules are unchanged.

`TestManagedPacketIdleExpiryDoesNotNeedAnotherValidPacket` uses an actual UDP
socket and authenticated native sessions, but explicitly simulates elapsed idle
time on one session. Quiet and continuous invalid-packet cases must release that
lease within the existing five-second maintenance period plus two seconds;
another established session must then receive actual encrypted payload. The
stronger two-session original-source run failed **1 top-level / 2 subtests** in
14.050s, retaining both leases (`native-idle-healthy-red.jsonl`). A separate
expired-session test queues a reply with remote window zero: cleanup took
1.072446416s, exceeding its predeclared 500ms limit
(`native-idle-stalled-red.jsonl`, **1 top-level failure**, package 1.100s).
After both fixes, the combined focused race run passed **2 top-level / 2
subtests**, no skips, in 11.049s (`native-idle-focused-green.jsonl`).

`TestOfficialUDPTransportLossExpiresWithoutAuthenticatedTraffic` supplies the
official v3.38.0 client's PacketDialer with actual UDP sockets. It exchanges TCP
and UDP payload, checks exact 22-byte upload / 22-byte download / 88-byte billed
totals at 2x, then closes those physical sockets **before** Client.Stop. Both
sessions must remain online after five seconds, proving the test did not merely
deliver a graceful session-close message. With production clocks and timeouts,
native leases/buffers and adapter presence must disappear within **67s from
physical loss**: 60s idle TTL + 5s maintenance + 2s scheduling margin. It then
checks unchanged usage and fresh TCP/UDP traffic by another user through the
same listener. No healthy official client sends heartbeats during the wait,
because they could wake the old parser and mask the quiet-socket bug.

An overlay restoring the prior native packet source, with continuous invalid
packets, failed after 67.001s with two leases, 25 buffered bytes and two online
records: **1 top-level / 1 subtest failure**, package 68.735s
(`native-idle-wallclock-red.jsonl`). The restored-source race run passed **1
top-level / 2 subtests**, no skips, in 128.036s. Actual cleanup was
59.995933639s for quiet and 64.988051908s for invalid packets
(`native-idle-wallclock-green.jsonl`). No bound was relaxed after failure.

Commands for these focused results:

```sh
go test -p 1 -race ./internal/mieru/native \
  -run '^TestManaged(PacketIdleExpiry|ExpiredSession)' -count=1 -timeout=45s -json
go test -p 1 -race ./internal/mieru \
  -run '^TestOfficialUDPTransportLossExpiresWithoutAuthenticatedTraffic$' \
  -count=1 -timeout=160s -json
python3 tools/managed-mieru/prepare.py --verify
```

The negative control used `-overlay` to replace only `underlay_packet.go` with
its source at a0470c2e, selected `/invalid-packets$`, and used a 100s test timeout.
All diagnostic logs are under `/tmp/3x-ui-rate-trace/`. The maintained patch
again reproduces checksum-verified v3.38.0 source, authored tests and license
byte for byte. This increment verifies silent UDP expiry, not TCP FIN hidden
behind full native queues, unrestricted public sender buffers, or the whole
Task 6 acceptance matrix. The earlier unrestricted public Client.Stop cleanup
failure remains recorded. Full affected-path regression follows below.

The complete native and adapter race regression passed:
`go test -p 1 -race -shuffle=on ./internal/mieru/... -count=1 -timeout=450s
-json` — **2 packages, 111 top-level / 1885 subtests**, no skips or failures.
Adapter took 258.861s; native protocol took 40.200s
(`native-idle-final-race.jsonl`). The package timeout accommodates the two new
real one-minute waits; each test's original cleanup bound remains unchanged.

With the same pinned managed core, Mihomo and owned PostgreSQL environment as
the preceding milestone, public normal-build regression also passed:
`go test -p 1 -shuffle=on ./internal/web/service ./internal/sub -run
'^Test.*Mieru|^TestManagedPolicy' -count=1 -timeout=600s -json` — **2 packages,
35 top-level / 84 subtests**, no skips or failures. Service took 369.165s and
subscription took 3.993s (`native-idle-public-final.jsonl`). This rechecks public
shared stream/datagram rates, natural quota and restriction combinations,
core recovery, management lifecycle and official client subscription paths.

`golangci-lint run ./internal/mieru/...` reported **0 issues** and
`go build -o /tmp/3x-ui-idle-panel .` exited zero. Logs are
`native-idle-lint-final.log` and `native-idle-build-final.log`.
No frontend source changed, and this increment did not rerun the entire root
Go suite: the earlier full-root evidence belongs to the admission batching
milestone. The complete original task remains open.

## Public mieru IPv6 acceptance (2026-09-29)

`TestMieruIPv6RoutingPreservesSourceAndPolicy` and its PostgreSQL counterpart
exercise both native underlays with official v3.38.0 clients, actual `::1`
listeners and TCP/UDP `::1` targets. The fixture creates the inbound through
existing public services, changes its listener through `UpdateInbound`, saves
conjunctive routing rules through `SaveXraySetting` and applies them through
`RestartXray`. Rules require the public tag, an allowed canonical email, original
IPv6 source, domain, network and port; unmatched traffic goes to blackhole.
The private bridge remains IPv4 loopback. Public online IP and the actual UDP
reply peer must retain IPv6.

The first user's exact totals are **53 up / 53 down / 159 billed** at 1.5x.
The independent 1x user has **44 / 44 / 88**, then **88 / 88 / 176** after
continuing while the first user is disabled, and **132 / 132 / 264** after core
restart and fresh traffic. First-user TCP/UDP flows close within the existing
1250ms bound; fresh authentication stays denied across core restart and its
durable counters remain unchanged. Both users share `::1`.

A narrow Go overlay changed only the runtime bridge callback's `dest.Source`
to `127.0.0.1:23456`. Both native transports then timed out at the first real
payload exchange: **1 top-level / 2 subtest failures**, 25.165s
(`mieru-ipv6-source-red.jsonl`). Restoring the real source, without any production
code change, passed **2 top-level / 4 subtests** under race in 39.223s, no skips
or failures (`mieru-ipv6-source-green.jsonl`):

```sh
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
go test -p 1 -race ./internal/web/service \
  -run '^TestMieruIPv6RoutingPreservesSourceAndPolicy' -count=1 -timeout=180s -json
```

Only acceptance tests and their helpers changed: official test clients now use
the supplied address instead of hardcoded IPv4, and echo fixtures can bind an
explicit address while existing callers retain `127.0.0.1:0`. This is local
dual-stack proxy evidence, not Internet IPv6, NAT64, IPv6 configuration export,
or the complete routing/rate/fault acceptance matrix. Separate user-specific
outlet selection, priority, balancers and preview/rollback remain required.

Affected IPv4 regression also passed under race/shuffle: **5 top-level / 10
subtests**, no skips, 43.798s (`mieru-ipv6-ipv4-regression.jsonl`). The selection
was `^TestMieru(InboundRunsThroughProductionXrayLifecycle|InboundHotCredentialsAndEnablePreserveOtherClient|InboundQuotaSurvivesCoreRestartAndManualDisable|PresenceAndStatusFollowAuthenticatedFlows)`.
It includes SQLite/PostgreSQL lifecycle, credential/enable isolation, quota
restart persistence and real public presence. `golangci-lint run
./internal/web/service` reported **0 issues** (`mieru-ipv6-lint.log`).
No runtime source changed, so production build/full-suite evidence remains the
preceding UDP-idle milestone; neither was rerun for this test-only increment.

## Public mieru preview and preflight rejection (2026-09-29)

`TestMieruPreviewAndRejectedConfigKeepWorkingFlows` and its PostgreSQL counterpart
exercise actual official TCP/UDP native clients through the production Runtime
and managed core. Existing TCP and UDP payload flows continue during repeated
public `GetXrayConfig` previews of a saved logging change for 1.1s, spanning more
than four native reconciliation ticks. An invalid routing CIDR is then saved;
`RestartXray(false)` must reject it in preflight while both existing flows and
fresh official-client flows still use the previous valid configuration. Restoring
the original template preserves those flows and the running core. Independent
target receive counts must equal raw upload/download totals and 1.5x billing
must equal three times each direction's observed bytes.

The initial preview-side-effect mutation applied the managed runtime plan from
`GetXrayConfig`; both native transports failed with closed-pipe errors during
existing payload exchange (**1 top-level / 2 subtests**, 5.240s,
`mieru-preview-red.jsonl`). The first restored run failed all four database /
transport cases because the test expected the invalid CIDR text in the error.
The actual preflight contract deliberately withholds core diagnostics and wraps
`exec.ExitError` with exit code 23. The final test asserts that typed exit status
and the validation-stage error prefix. No production behavior, data-path
assertion or timing bound changed (`mieru-preview-green.jsonl`, 15.765s).

A separate mutation skipped preflight validation. The actual core then failed
startup and restored its previous configuration; the test rejected this later
failure stage (**1 top-level / 2 subtests**, 9.317s,
`mieru-preview-validation-red.jsonl`). With the final fixture, the preview-apply
mutation again failed both native transports in 5.199s
(`mieru-preview-final-red.jsonl`). These controls use Go overlays only; repository
runtime source remains unchanged.

The next restored run exposed another fixture issue after the original template
was restored: all four cases timed out on an old TCP flow
(`mieru-preview-final-green.jsonl`, 65.061s); lint did not execute. The first
diagnostic showed the restore replaced the core even though its resulting
configuration was identical. The fixture's inbound creation had left the
pending-restart flag set. Unlike the panel's timer, its direct initial
`RestartXray(true)` did not consume that notification. Before establishing the
baseline flows, the final fixture calls the same `ApplyPendingRestart` path as
`internal/web/web.go`. It does not write the flag directly. A diagnostic then
observed pending=true at creation, false before restoration, a 6.421ms restore,
the same core/configuration, and all four old/fresh TCP/UDP flows continuing
(`mieru-preview-pending-diagnostic.jsonl`). No production fix or relaxed
continuity assertion was needed.

With that final baseline, both negative controls still failed as intended:
preview application **1 top-level / 2 subtests** in 5.199s, omitted validation
**1 top-level / 2 subtests** in 9.287s (`mieru-preview-settled-red.jsonl` and
`mieru-preview-settled-validation-red.jsonl`). Restored source passed **2
top-level / 4 subtests**, no skips or failures, under race/shuffle in 21.930s:

```sh
go test -p 1 -race -shuffle=on ./internal/web/service \
  -run '^TestMieruPreviewAndRejectedConfigKeepWorkingFlows' \
  -count=1 -timeout=120s -json
golangci-lint run ./internal/web/service
```

The managed core and PostgreSQL environment are the same as the IPv6 increment.
Lint reported **0 issues**. Logs: `mieru-preview-settled-green.jsonl` and
`mieru-preview-settled-lint.log`. Only a new acceptance test and documentation
changed; no production code or existing test helper changed, and no additional
build/full-suite rerun is claimed. This covers preview and preflight rejection,
not continuity after an already-stopped core or a complete panel-process restart.

## Public mieru routing with observed exits (2026-09-29)

`TestMieruPublicRoutesChooseObservedExit` and its PostgreSQL counterpart use
canonical public clients, `SaveXraySetting`, `RestartXray(false)`, actual official
v3.38.0 clients and the pinned managed core. Each combination of database, native
underlay and payload network runs ten cases: first user, second same-IP user,
domain, literal IP, original source, combined public inbound/network/port,
wrong-port block, wrong-network block, first-deny priority and round-robin.
That is **80 leaf route cases**; Go also reports 12 grouping subtests.

Two freedom exits redirect to an owned target using actual socket source
addresses `127.0.0.2` / `127.0.0.3`. Each probe carries a unique eight-byte marker.
The target records its source and marker independently, then echoes the payload.
The client verifies the echo; UDP also verifies its returned target peer.
Round-robin must produce A/B/A across three fresh probes. Blocked probes must
produce no target observation during the declared 200ms window, followed by a
successful second-user control through B on the same running service. No host
interface, firewall or global routing changes are made for these loopback exits.

The initial user-matcher-removal overlay failed at core validation because a
user-only rule became empty (**1 top-level / 4 grouping/leaf subtest failures**,
3.444s, `mieru-public-routing-red.jsonl`). That was not route-selection evidence.
The fixture now explicitly includes its tested network in user rules, so
removing only the user condition creates a valid but overly broad rule. The
meaningful negative control passed the first-user case and failed the second:
marker `0000000000000002` arrived from **127.0.0.2 instead of 127.0.0.3**
(`mieru-public-routing-user-red.jsonl`, 4.546s). The reported failure includes
the parent groups; only one leaf failed and one leaf passed.

Restored source passed under race/shuffle: **2 top-level / 92 subtests**, no
skips or failures, in 28.574s (`mieru-public-routing-green.jsonl`):

```sh
go test -p 1 -race -shuffle=on ./internal/web/service \
  -run '^TestMieruPublicRoutesChooseObservedExit' -count=1 -timeout=240s -json
```

The managed core and PostgreSQL environment are the same as the preceding
acceptance increments. Production source remains unchanged; the overlay strips
only generated routing `user` matchers and is never applied to the repository.
This evidence concerns fresh-flow route choices after public configuration
application. It does not prove established-flow route migration, every outbound
protocol, global node routing, or complete Task 6 acceptance. Blocked payload can
still incur its documented durable-admission charge; this test does not assert
that blocked or undelivered bytes are uncharged.

The final readable negative-control log records
`source=127.0.0.2 payload=0000000000000002 want=127.0.0.3/0000000000000002`
and the first-user pass (4.531s, `mieru-public-routing-final-red.jsonl`). Initial
lint found one gofumpt layout issue in the round-robin test literal; formatting
was corrected without changing assertions. `golangci-lint run
./internal/web/service` then reported **0 issues**
(`mieru-public-routing-lint-final.log`). Only the new test and documentation
changed; production build/full-suite evidence remains the preceding milestones.
## Actual panel exit and persistent mieru enforcement (2026-09-29)

`mieru_panel_process_linux_test.go` starts the actual panel executable, initializes
its administrator with the CLI, and uses cookie/CSRF-authenticated HTTP APIs to
create the inbound and update client policies. Only the isolated routing template
and disabled subscription listeners are seeded before startup. Official mieru
v3.38.0 API clients send TCP/UDP payload through the pinned managed core to actual
echo targets. The matrix covers SQLite/PostgreSQL, native TCP/UDP, and
`SIGTERM`/`SIGKILL` (eight leaf cases).

The original panel failed the SQLite/TCP/SIGKILL case: its core API port remained
bound beyond the predeclared two seconds after panel exit. That run failed in
5.078s, before any production change (`mieru-panel-process-red.jsonl`). Linux had
no child-lifetime attachment. The fix keeps `Start` and `Wait` on a dedicated,
locked OS thread and configures the child's parent-death signal as `SIGKILL`.
Other process attributes and the Windows job-object attachment are retained.

This thread lifetime is necessary because the Linux signal follows the creating
thread, not the final thread in the parent process; see the
[Go issue](https://github.com/golang/go/issues/27505),
[Linux manual](https://man7.org/linux/man-pages/man2/PR_SET_PDEATHSIG.2const.html),
and [Go thread-lock contract](https://pkg.go.dev/runtime#LockOSThread).
An additional real echo-child regression starts the child from a locked caller,
waits for its listener, terminates that caller's OS thread, then exchanges data.
A temporary Pdeathsig-only overlay failed with connection refused in 0.084s
(`xray-naive-pdeathsig-ready-red.jsonl`). Initial fixture attempts used Go's
initial thread, which the runtime parks instead of terminating; these were
fixture failures, corrected by keeping that thread occupied and using another
worker. The final test verifies that worker really disappears from `/proc`.

The actual-panel test declares exit within five seconds, both listener ports
reusable within two seconds after exit, HTTP readiness within 15 seconds, and
native runtime recovery within five seconds of the authenticated HTTP check.
The depleted user retains **44 up / 44 down / 176 billed** at 2x. The healthy
user's original TCP/UDP flows still work after the other user's depletion;
its pre-exit snapshot is **88 / 88 / 264** at 1.5x. After restarting the same
database, both policies and usage snapshots compare exactly, the depleted
credential remains denied, and new healthy flows bring its counters to
**132 / 132 / 396**. Stored 64/32 KiB/s rates also survive; this is persistence
evidence, not a new throughput measurement. UDP client-side silent-death
detection is not assigned the server listener-release bound.

```sh
go build -p 1 -o /tmp/3x-ui-child-lifetime-panel .
XUI_E2E_PANEL=/tmp/3x-ui-child-lifetime-panel \
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
go test -p 1 -race -shuffle=on -count=1 -json \
  -run '^TestMieruPanelProcessRestart' ./internal/web/service
```

The strengthened matrix passed **2 top-level / 12 subtests**, zero skips, in
56.367s; 12 includes four underlay parent groups and eight leaf cases. The panel
binary is a normal build; the test harness uses race instrumentation. Measured
SIGKILL-to-panel-exit time was 5.720–8.304ms (four cases), graceful exit
8.851ms–3.711s (12 stops including the recovered instances), and listener release
107µs–10.777ms (16 stops). Native status checks reached running in
258.092ms–1.294s. The initial, less strict matrix also passed in 47.702s before
adding the explicit pre-exit healthy-flow exchange.

Complete `internal/xray` race/shuffle regression passed **102 top-level / 81
subtests**, zero skips, in 5.076s with `XRAY_E2E_BINARY` set to the pinned managed
core. Logs under `/tmp/3x-ui-rate-trace/`: `mieru-panel-process-final.jsonl`,
`xray-child-lifetime-green.jsonl` (six focused lifecycle tests, 2.790s), and
`xray-child-lifetime-all-race.jsonl`. This is a scoped process-recovery fix;
remaining native buffering, deployment, node and full-protocol requirements
remain open.

The affected Go lint command reported **0 issues**. Both cross-compilation
commands completed successfully; they establish compilation, not runtime
behavior on those operating systems:

```sh
golangci-lint run ./internal/xray/... ./internal/web/service/...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/xray \
  -o /tmp/3x-ui-rate-trace/xray-lifetime-windows.test.exe
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go test -c ./internal/xray \
  -o /tmp/3x-ui-rate-trace/xray-lifetime-darwin.test
```

The final full-root backend regression completed with exit zero: **53 test
packages, 2677 top-level / 4670 subtests passed**, **28 top-level / 14 subtest
skips**, and seven packages without tests. There were no failures. All 42
skipped names exactly match `admission-full-go.jsonl`; no skip was added or
removed, and none counts as a pass. The service package took 612.085s and
included both actual-panel database matrices. Command:

```sh
XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_MIHOMO_E2E_BINARY=/tmp/3x-ui-mihomo-v1.19.30 \
XUI_E2E_PANEL=/tmp/3x-ui-child-lifetime-panel \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
go test -p 1 -shuffle=on -count=1 -timeout=25m ./... -json
```

Log: `/tmp/3x-ui-rate-trace/xray-child-lifetime-full-go.jsonl`. No frontend
source or assets changed in this increment, so frontend tests/build were not
repeated. The separate Snell audit documentation committed during this run
changed no Go source under test.

## 2026-09-29: Linux TCP FIN/RST behind full native queues

`TestOfficialTCPTransportLossReclaimsFullQueues` uses the unchanged official
v3.38.0 client, actual loopback TCP transport and TCP/UDP payload targets, a
temporary SQLite ledger and two independent same-IP users. The first uploads a
finite 512 KiB workload at 64 B/s until native queues hold at least 240 KiB.
Closing its captured physical socket precedes `Client.Stop`, preventing a native
session-close message from explaining server cleanup. FIN and `SetLinger(0)` RST
are separate cases. Within the predeclared **two seconds from physical close**,
only the healthy user's native lease/presence may remain and native retained
payload must be zero. Its already-open flow still echoes; the lost account's
settled counters stop growing even after removing shaping, and a fresh official
connection succeeds. The lossless paused-admission and connection-scoped
backpressure tests supply healthy saturated controls.

Initial 1 MiB and 512 KiB FIN fixtures retained kernel `ESTABLISHED`: FIN had
not entered the full TCP receive window, so these failures did **not** establish
a native cleanup defect. A smaller 320 KiB TCP workload passed existing code
with FIN/RST cleanup around 1.08s. The UDP fixture cleaned up promptly but its
fresh-connection check incorrectly ignored already-settled whole-packet debt:
a 2048-byte admitted datagram at 64 B/s can leave about 32s of debt. The final
fixture explicitly removes the rate after cleanup and the accounting snapshot;
it does not refund admitted bytes or relax the cleanup deadline.

Only the final fixture's owned TCP receive buffers are set to 2 MiB, allowing
the finite workload's FIN to arrive without changing production socket options
or native queue caps. The original implementation then fails with kernel
`CLOSE_WAIT`, **two sessions / 262144 retained bytes / two online users** beyond
two seconds (`mieru-tcp-loss-window-sized-red.jsonl`, package 2.516s). RST also
fails with retained resources (`mieru-tcp-loss-rst-red.jsonl`, 3.586s including
cleanup). Both are actual socket-loss failures, not synthetic session closure.

The fix checks Linux kernel shutdown without consuming wire bytes while the
managed receive path is blocked. `RawConn.Control` protects the descriptor,
zero-timeout `poll` requests `POLLRDHUP`, and HUP/ERR also end that underlay.
The check is throttled to 100ms within a blocked delivery. No additional worker,
unbounded buffering or changed native idle timer is involved. Other platforms
retain ordinary EOF detection; this does not bound network delivery of FIN.
The maintained native patch reproduces the pinned source and license exactly.

```sh
go test -p 1 -count=1 -json \
  -run '^(TestOfficialTCPTransportLossReclaimsFullQueues|TestNativeTCPMultiplexingBackpressureIsConnectionScoped)$' \
  ./internal/mieru
python3 tools/managed-mieru/prepare.py --verify
```

The first fixed focused run passed **2 top-level / 4 subtests**, zero skips, in
7.236s. Measured FIN TCP/UDP cleanup was 1.092s / 2.813ms and RST TCP/UDP was
1.080s / 100.305ms, including the official client's stop call. Settled raw up /
down / billed counters were 17 / 5 / 44 for TCP and 2053 / 5 / 4116 for UDP at
2x. Logs live under `/tmp/3x-ui-rate-trace/`; complete affected regression and
full-root verification are recorded below when terminal results are available.

The complete affected race/shuffle run finished with exit zero: **111 top-level /
1887 subtests passed**, no failures, and **one top-level skip** because that
invocation omitted `XUI_MANAGED_XRAY_E2E_BINARY`. The skipped
`TestOfficialClientsThroughManagedCoreBillOnceAndRevoke` was then run separately
with the pinned actual core: **1 top-level / 2 subtests passed**, zero skips,
4.830s. The first skip remains recorded and is not counted as a pass. Native
protocol and adapter package times were 40.256s and 252.717s. These runs include
the lossless 1.8 MiB paused-admission control on both transports and the new four
physical TCP-loss cases. Affected-package lint reported **0 issues**.

```sh
go test -p 1 -race -shuffle=on -count=1 -timeout=15m -json ./internal/mieru/...
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
  go test -p 1 -race -count=1 -json \
  -run '^TestOfficialClientsThroughManagedCoreBillOnceAndRevoke$' ./internal/mieru
golangci-lint run ./internal/mieru/...
```

An attempted Windows adapter test cross-build with `CGO_ENABLED=0` failed in
the existing database backup dependency: `sqlite3.SQLiteConn.Backup` is absent
from the no-CGo stub (`internal/database/db.go:2975`). The repository requires
CGo for SQLite, and this host has no Windows/macOS C cross-toolchain. This is
not a successful adapter/platform build; native-protocol-only compilation is
checked separately below. Logs: `mieru-tcp-loss-full-race.jsonl`,
`mieru-tcp-loss-bridge-race.jsonl`, `mieru-tcp-loss-lint.log`, and
`mieru-tcp-loss-windows-build.log`.

Native-protocol test binaries cross-compiled successfully for Windows amd64 and
macOS arm64. These exclude the adapter's database dependency and establish only
compilation, not platform runtime acceptance. The actual Linux panel also built
successfully; its executable is used by the subsequent full-root regression.

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/mieru/native \
  -o /tmp/3x-ui-rate-trace/mieru-tcp-loss-native-windows.test.exe
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go test -c ./internal/mieru/native \
  -o /tmp/3x-ui-rate-trace/mieru-tcp-loss-native-darwin.test
go build -p 1 -o /tmp/3x-ui-tcp-loss-panel .
```

Final full-root backend regression completed with exit zero: **53 test packages,
2678 top-level / 4674 subtests passed**, no failures, **28 top-level / 14 subtest
skips**, and seven packages without tests. The 42 skipped test names exactly
match the preceding `xray-child-lifetime-full-go.jsonl`; none was added or
removed and none counts as a pass. This includes the four new physical TCP-loss
cases, the actual Linux panel process matrix and real core/client tests.

```sh
XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_MIHOMO_E2E_BINARY=/tmp/3x-ui-mihomo-v1.19.30 \
XUI_E2E_PANEL=/tmp/3x-ui-tcp-loss-panel \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
XUI_E2E_PG_DSN='postgresql://nobody@127.0.0.1:55432/postgres?sslmode=disable' \
go test -p 1 -shuffle=on -count=1 -timeout=25m ./... -json
```

Log: `/tmp/3x-ui-rate-trace/mieru-tcp-loss-full-go.jsonl`. No Go source changed
during the run. The separately committed isolated-network probe and audit
documentation changed no tested Go package. Frontend source/assets were unchanged
and frontend verification was not repeated. This completes this scoped Linux
TCP-loss fix; full Task 6 and the original multi-backend goal remain incomplete.

## AWG forwarded-port reservations and concurrent public mutations

Scope and RED cases are in [awg-port-reservations.md](awg-port-reservations.md).
This covers fixed/template listeners, full inbound edit/enable, client
add/edit/import and bulk enable; it does not implement first-class forwarding,
peer-versus-peer ownership or AWG forwarded-payload policy.

With isolated SQLite/PostgreSQL fixtures and the actual managed core paths:

```sh
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
go test -p 1 -race -shuffle=on -count=1 -timeout=25m -json \
  -run 'Test.*(Port|AWG|Amnezia|Template|Portable|Import|Bulk.*Enable|Bulk.*Reenable|SetInboundEnable)' \
  ./internal/web/service
```

Result: **180 top-level tests / 210 subtests passed**, no failures or skips,
169.250s. Log: `/tmp/3x-ui-rate-trace/awg-reservations-final-related-race.jsonl`.
The six new PostgreSQL concurrency cases observe the actual advisory wait,
then reject the committed reservation. Import preserves retained accounting;
bulk enable preserves all three disabled projections and succeeds after the
reservation is removed.

Staticcheck initially rejected the test dispatch's `if` chain with `QF1003`.
It was changed to an equivalent tagged `switch`, then the final focused race
rerun passed **3 top-level tests / 66 subtests**, no failures/skips, 67.944s:

```sh
go test -p 1 -race -count=1 -timeout=3m -json \
  -run '^TestAWGClient(ForwardsRespectAdditionalReservations|ForwardMutationWaitsForReservation)' \
  ./internal/web/service
golangci-lint run ./internal/web/service/...
go build -p 1 -o /tmp/3x-ui-awg-reservations-panel .
```

The same PostgreSQL environment was present. Final lint reported zero issues;
the current normal panel binary built successfully.

Final full-root regression exited zero: **53 test packages, 2681 top-level tests
and 4740 subtests passed**, no failures, **28 top-level / 14 subtest skips**,
seven packages without tests. All 42 skipped names exactly match the preceding
`mieru-tcp-loss-full-go.jsonl`; none was added, removed or counted as a pass.
The service package completed in 639.104s, including the new AWG matrix and
the actual panel/core/mieru data paths.

```sh
XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_MIHOMO_E2E_BINARY=/tmp/3x-ui-mihomo-v1.19.30 \
XUI_E2E_PANEL=/tmp/3x-ui-awg-reservations-panel \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
XUI_E2E_PG_DSN='postgresql://nobody@127.0.0.1:55432/postgres?sslmode=disable' \
go test -p 1 -shuffle=on -count=1 -timeout=25m ./... -json
```

Log: `/tmp/3x-ui-rate-trace/awg-reservations-full-go.jsonl`. No Go source or
embedded frontend assets changed during this run. The separately committed
network prerequisite probe and documentation changed no tested Go package.
Frontend verification was not repeated because its source/assets were unchanged.
This verifies the scoped reservation fix, not all Task 8 requirements.

## AWG peer forward ownership and candidate state

This follow-up is scoped to save-time resource ownership and lifecycle state;
[awg-port-reservations.md](awg-port-reservations.md) describes the behavior.
AWG forwarded payload still bypasses the unified policy meter. These checks do
not complete Task 8 or establish per-client forwarding throughput or quota.

Observed RED evidence before the relevant changes:

- Ten conflicting creation/addition/edit cases accepted duplicate peer
  claims, while ten disjoint-port controls passed. Two complete-edit cases
  accepted moving the public listener onto an owned forward.
- Moving a public listener away and using its released port failed on both
  databases because the preflight still examined the old inbound row.
- Temporarily removing only the addition's post-rebase validation accepted
  both same-inbound and cross-inbound PostgreSQL conflicts after an observed
  advisory wait and the other transaction's commit. The original early lock
  remained in place. The source was restored before subsequent runs.
- Whole-row validation initially blocked disabling one of three conflicting
  legacy peers, and disabling an inbound through its full edit endpoint.
  Both failures were observed on SQLite and PostgreSQL. The corresponding
  disable paths now avoid acquiring new forward claims.
- Existing fixed-reservation validation also prevented client disable in both
  databases. The regression test removes the claim successfully and verifies
  that re-enable remains rejected by the same named reservation.

Focused restored concurrency race result: **1 top-level test / 2 subtests
passed**, no skips, 4.714s. The expanded focused ownership matrix passed
**10 top-level tests / 56 subtests**, no skips, 18.333s. Subsequent lifecycle
coverage, including full-edit disable and re-enable, passed **2 top-level tests
/ 20 subtests**, no skips, 6.713s. The latter run follows the final production
change. Earlier enable controls passed 1 top-level / 6 subtests in 2.340s;
these are overlapping checks, not additive acceptance totals.

Final affected race checks passed with actual local PostgreSQL and Xray:

```sh
go test -p 1 -race -count=1 -timeout=5m -json ./internal/amneziawg ./internal/amneziawgnet
go test -p 1 -race -count=1 -timeout=8m -json \
  -run 'Test.*(Port|AWG|Amnezia|Template|Portable|Import|Bulk.*Enable|Bulk.*Reenable|SetInboundEnable)' \
  ./internal/web/service
golangci-lint run --timeout=5m ./...
go build -p 1 -o /tmp/3x-ui-awg-peer-ownership-panel .
```

Native packages: **148 top-level / 76 subtests passed**, zero skips; 1.269s
and 23.006s. Service regression: **191 top-level / 276 subtests passed**, zero
skips, 233.069s, including all 11 new ownership tests / 66 subtests. Lint
reported **0 issues** and the normal panel build succeeded. Logs are
`awg-peer-native-race.jsonl`, `awg-peer-related-race.jsonl`, `awg-peer-lint.log`
and `awg-peer-build.log` under `/tmp/3x-ui-rate-trace/`.

The final full-root regression exited zero: **53 test packages, 2692 top-level
tests and 4806 subtests passed**, no failures. **28 top-level and 14 subtests
skipped**; all 42 names exactly match `awg-reservations-full-go.jsonl`. Seven
packages have no tests. The service package completed in 658.088s.

```sh
XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_MIHOMO_E2E_BINARY=/tmp/3x-ui-mihomo-v1.19.30 \
XUI_E2E_PANEL=/tmp/3x-ui-awg-peer-ownership-panel \
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
XUI_E2E_PG_DSN='postgresql://nobody@127.0.0.1:55432/postgres?sslmode=disable' \
go test -p 1 -shuffle=on -count=1 -timeout=25m -json ./...
```

Log: `/tmp/3x-ui-rate-trace/awg-peer-full-go.jsonl`. No Go source or embedded
frontend assets changed during the run. Frontend checks were not repeated for
this backend-only change. The full task remains incomplete; this evidence
covers the resource-ownership follow-up, not AWG payload policy enforcement.

## AWG forward revocation and close races

This lifecycle follow-up retains the peer/device while removing one of its
forwarding rules. Before the fix, the real encrypted TCP path delivered another
byte after removal, a pending tunnel dial left its external socket open past
the two-second read deadline, and an observed UDP target-resolution race
published one session after close. The focused RED run failed all three
selected top-level tests; the unrelated UDP and restored-TCP controls passed.
Log: `/tmp/3x-ui-rate-trace/awg-forward-lifecycle-native-red.jsonl`, 5.062s.

After the listener ownership/cancellation and UDP publication guards, focused
race validation passed **3 top-level tests / 3 subtests**, no skips, 6.396s.
The pending-dial case completed in 0.02s. The encrypted rule-removal subtest
completed in under the test logger's 0.01s resolution; UDP continuity and fresh
TCP restoration also passed. These observations concern local fixture cleanup,
not a universal network latency or payload quota bound. Log:
`/tmp/3x-ui-rate-trace/awg-forward-lifecycle-native-first-green.jsonl`.

Explicit set closure and repeated-close controls were added after that run.
Complete affected race checks passed: **207 top-level / 146 subtests** across
`amneziawg`, `amneziawgnet` and `web/runtime` (1.263s, 24.520s, 2.781s), and
**86 top-level / 151 subtests** in the AWG service regression (136.875s). No
tests skipped. Logs: `awg-forward-close-native-race.jsonl` and
`awg-forward-close-service-race.jsonl` in `/tmp/3x-ui-rate-trace/`.

The first lint run stopped the pipeline on three `errorlint` findings in test
error assertions; the build/full-root run had not started. The assertions now
use `errors.As`, and their absolute two-second deadline starts before
`Close`/`Reconcile`, including the operation itself. The old file header's
misleading enforcement claim was also corrected; runtime logic was unchanged
by these follow-ups.

Fresh focused race validation passed **3 top-level / 3 subtests**, no skips,
6.108s. Final lint reported **0 issues** and the normal panel build succeeded:

```sh
go test -p 1 -race -count=1 -timeout=45s -json \
  -run '^TestPortForward(RoundTripTCPAndUDP|UDPCloseDuringTargetResolution|TCPCloseCancelsPendingTunnelDial)$' \
  ./internal/amneziawgnet
golangci-lint run --timeout=5m ./...
go build -p 1 -o /tmp/3x-ui-awg-forward-close-panel .
```

Logs: `awg-forward-close-final-focused-race.jsonl`,
`awg-forward-close-final-lint.log`, `awg-forward-close-build.log` in the same
directory. Full-root validation with that panel exited zero: **53 test packages,
2694 top-level tests and 4809 subtests passed**, no failures. **28 top-level and
14 subtests skipped**; all 42 names exactly match `awg-peer-full-go.jsonl`.
Seven packages have no tests. The service package completed in 657.608s.

The command and local dependencies match the preceding ownership regression,
with `XUI_E2E_PANEL=/tmp/3x-ui-awg-forward-close-panel`. Full log:
`/tmp/3x-ui-rate-trace/awg-forward-close-full-go.jsonl`. No Go source or embedded
frontend assets changed during the run. Frontend checks were not repeated for
this backend-only change. This evidence covers connection revocation and
closure; AWG payload accounting, shaping and quota admission remain open.


## Release archive staging component

The new staging helper is documented in [update-staging.md](update-staging.md).
It is not yet wired into the installer or updater and does not establish
fork provenance, managed-core compatibility or transactional upgrade rollback.
No host installation or service was changed during this validation.

The package and CLI initially failed to compile because their new entry points
did not exist. After implementation, a subprocess test exposed blocking on a
FIFO before regular-file validation (2.008s, exit 1). Linux input now uses
nonblocking open and refuses a final-component symlink. Non-Linux command builds
return an explicit unsupported-platform error.

Affected race/shuffle checks passed **18 top-level tests / 73 subtests**, no
skips, across two packages (1.213s and 2.045s). These Go totals include the fuzz
seed cases and the subprocess helper entry. Full repository lint reported
**0 issues**. The Linux arm64 command built as a static binary with CGo disabled;
Windows amd64 and Darwin arm64 cross-builds also passed. Those two checks are
compile-only evidence, not non-Linux runtime acceptance.

Mutation controls used Go overlays without modifying the worktree. Removing
member validation accepted 19 unsafe cases while both ordinary GNU/USTAR
controls continued to pass. Removing only the final compressed SHA256 comparison
accepted an incorrect, well-formed digest; the valid archive controls passed.

Initial 30-second fuzzing exited successfully but reported only 4 executions.
A 100-execution diagnostic completed in 0.123s. Explicitly bounding each
minimization to one second then completed **6861 executions in 30.495s**, with
31 new interesting inputs and no failure. This records the exercised scope;
the initial low-throughput run is not presented as broad parser coverage.

The built command staged an archive containing the actual 99,131,648-byte panel
and 46,374,476-byte managed Xray binary. Both extracted files matched their
source SHA256s; their version commands returned panel **3.8.5** and
**Xray 26.9.9 / 3x-ui-managed-1**. Building the 72,211,900-byte compressed fixture
and staging/verifying it took 4.134s in this local run. Mutating its gzip trailer
and recomputing the outer SHA256 produced exit 2 with `gzip: invalid checksum`,
removed the partial stage and preserved the old-install sentinel. This checks
real artifact extraction and version execution, not an upgrade or database
migration.

Logs under `/tmp/3x-ui-rate-trace/`: `update-stage-final-race.jsonl`,
`update-stage-full-lint.log`, `update-stage-fifo-red.jsonl`,
`update-stage-member-mutation-red.jsonl`,
`update-stage-checksum-mutation-red.jsonl`,
`update-stage-restored-race.jsonl`, `update-stage-fuzz-bounded-minimize.log` and `update-stage-real-bundle.json`.
After the overlays, the unmodified source again passed **18 top-level / 73
subtests** under race/shuffle (1.213s and 2.046s), with no skips.
The existing installer defect remains open until integration and rollback tests
pass. Full-root Go regression will be repeated after those existing application
paths change; this independent package/CLI change used affected race tests and
full repository lint.

## Release source identity and file inventory

`release-manifest` now generates the bounded source/file manifest described in
[update-staging.md](update-staging.md). Explicit release flags make `update-stage`
verify it before publishing a stage path. Installer, menu, web updater and
release/Docker integration remain open; these tools alone do not fix those paths.
The declared ABIs and caller-supplied source commit still need verification
against the actual panel and managed-core capability before activation.

New API tests initially failed to compile; the release-flag test failed because
the command had no such flags. Follow-up RED runs exposed four accepted ambiguous
JSON forms (case aliases at three levels and an omitted nonexecutable flag),
then a fifth case where `null` silently decoded as `false`. Exact required keys
and non-null scalar fields now reject those inputs.

Affected race/shuffle validation passed **30 top-level tests / 135 subtests**,
no skips, across `internal/updatebundle`, `tools/update-stage` and
`tools/release-manifest` (1.397s, 2.083s, 1.027s). This includes the preceding
staging tests. Both Linux arm64 tools built with CGo disabled. Full repository
lint and Windows amd64/Darwin arm64 staging-command cross-builds also passed;
those are build checks, not runtime platform acceptance.

Go overlay negative controls removed one check at a time. Omitting the identity
comparison accepted mismatched commit/tag fixtures; omitting the per-file
comparison accepted six changed inventories. Ordinary arm64, armv7 and amd64
fixtures continued to pass in each mutation run. No worktree source was changed
for these controls.

A real-binary fixture assembled the prior normal panel and managed Xray, the
new staging helper and repository scripts/units (ten files plus the manifest).
Its declared source was deliberately a fixture identity, **not proof of the
binaries' source revision**, and no runtime capability probe or installer was
executed. Results from the built CLI commands:

| Case | Result | Local staging/verification duration |
| --- | --- | --- |
| Matching fixture identity and file inventory | Exit 0; every staged file hash matched | 1.519s |
| Different selected full commit | Exit 2; identity mismatch | 1.269s |
| Menu changed after manifest creation, outer archive SHA256 recomputed | Exit 2; file differs from manifest | 1.373s |

Every case preserved the old-install sentinel. Failed cases emitted no stage
path and removed their temporary directory; the successful stage was removed
by the fixture after inspection. Logs are `release-manifest-real-bundle.json`,
`release-manifest-final-race.jsonl`, `release-manifest-json-alias-red.jsonl`,
`release-manifest-null-red.jsonl`, `release-manifest-identity-mutation-red.jsonl`
and `release-manifest-file-hash-mutation-red.jsonl` under `/tmp/3x-ui-rate-trace/`.

The restored source was checked again under race/shuffle after the overlays;
see `release-manifest-restored-race.jsonl` and `release-manifest-full-lint.log`.
No frontend or existing application delivery code changed in this increment.

## Candidate release runtime preflight

The standalone panel commands in [update-staging.md](update-staging.md) now
check compiled metadata, the complete staged manifest and an authenticated
managed-core handshake before any business database initialization. This is a
preflight component; existing installer/menu/web updater integration and safe
activation/rollback remain unfinished.

The previous normal panel failed the new metadata CLI tests (unknown command,
non-JSON output and incorrect success exit for invalid arguments). It also
failed both real-core preflight cases because the command did not exist. New
metadata API/modified-source tests initially failed to compile. The first full
lint found a context-free listener; using `ListenConfig.Listen(ctx, ...)` fixed
that finding, and the final full repository lint reported **0 issues**.

A normal Linux arm64 candidate was built with an explicit `f` repeated 40 times
source stamp and `-buildvcs=false`. This is a declared **test fixture identity**,
not source provenance attestation. It accepted the actual managed Xray
26.9.9 / 3x-ui-managed-1 and rejected the stock core built from the same pinned
upstream source. Eight invalid bundle/CLI cases failed before launching a
marker core: commit, tag, platform, ABI, modified file, foreign panel executable,
extra argument and missing commit. Database sentinels and unrelated directories
remained unchanged; probe temporary files were removed.

A second actual build kept VCS metadata and the same explicit stamp. Its
`modified` flag was true; a fully inventoried fixture was rejected with exit 2
before the marker core ran, leaving its temporary directory empty. Thus the
explicit source stamp does not hide a recorded dirty checkout.

Cancellation testing waits until an owned core helper receives authentication
bytes on the actual probe listener, withholds the reply and sends SIGTERM to
the candidate. The candidate must fail within 2 seconds, reap the observed
child, release its exact listening address and remove its temporary files. A
Go overlay that discarded only signal-context propagation failed at this
unchanged deadline (3.851s overall); restoring the implementation passed. The
helper exercises process/handshake cleanup, not stock-core interoperability.

Final affected race/shuffle checks passed **9 top-level tests / 22 subtests**,
no skips, across the root package and `internal/config` (6.317s and 1.019s):

```sh
XUI_E2E_PANEL=/tmp/3x-ui-release-preflight-panel-final \
XUI_E2E_RELEASE_COMMIT=ffffffffffffffffffffffffffffffffffffffff \
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/3x-ui-xray-managed-final-1 \
XUI_STOCK_XRAY_E2E_BINARY=/tmp/3x-ui-xray-pinned \
go test -p 1 -race -shuffle=on -count=1 -timeout=2m \
  -run '^Test(ReleaseInfoCLI|VerifyReleaseCLI|ReleaseInfoSource|ReleaseInfoRejects|ReleaseInfoDoesNot|ReleaseInfoReports)' \
  -json . ./internal/config
```

Logs under `/tmp/3x-ui-rate-trace/`: `release-info-cli-red.jsonl`,
`release-runtime-first-green.jsonl`, `release-runtime-reject-green.jsonl`,
`release-runtime-cancel-mutant.jsonl`, `release-runtime-final-race.jsonl` and
`release-runtime-lint-final.log`. The final full-root command
`go test -p 1 -shuffle=on -count=1 -timeout=25m -json ./...`, with the normal
candidate, both managed-core variables, the separate stock-core variable,
Mihomo and both isolated PostgreSQL DSNs enabled, passed **56 test packages /
2734 top-level tests / 4966 subtests**, with no failures. The service package
took 664.626s. **28 top-level and 14 subtests were skipped**; their exact 42
names matched the preceding AWG forwarding regression, with none added or
removed. Seven additional packages had no tests. See
`release-runtime-full-go.jsonl`. Go source and embedded assets were unchanged
throughout the run. No frontend behavior changed in this increment.

No Docker image, release publication or host installation was executed. The
Dockerfile's scoped change builds the whole root package so the new CLI file is
linked; managed-core image/release assembly remains a separate delivery step.

## Managed release assembly and source export

The shared assembler and its distribution wiring are described in
[tools/managed-release](../../tools/managed-release/README.md). The Linux release
and Docker paths now build the managed core from its verified pin/patches and
assemble the scripts, static staging helper, units, source archive, license and
manifest. The candidate runs `verify-release` before archive/image production.
The Windows workflow builds the managed executable and includes a real bridge
test gate, while retaining upstream support files. Workflow wiring does not
constitute an executed Windows, foreign-architecture or Docker acceptance run.

The native probe first failed because the assembly command did not exist. Its
first actual build then failed the repeated-source-archive comparison. Diagnostic
reproduction found **zero source-content differences** and **1229 metadata
differences**, including directory timestamps five seconds apart. The core
binaries already matched. Sorted GNU tar entries, fixed times/numeric owners,
normalized modes and `gzip -n` corrected the source export without weakening
the byte-equality assertion.

```sh
GOFLAGS=-p=1 python3 tools/managed-release/probe_linux.py \
  --panel /tmp/3x-ui-release-preflight-panel-final
```

The final Linux arm64 fixture passed: **12 inventoried files**, compressed
archive **71,910,795 bytes**, byte-identical repeated core/source builds,
mandatory archive SHA256 and selected manifest verification, followed by the
staged panel's actual authenticated managed-core probe. The previous-install
sentinel remained intact and probe temporary files were removed. Its panel
source stamp was the explicitly declared `f` repeated 40 times fixture identity,
not an actual source revision. No installer, service manager or business
database was exercised by this assembly probe. Five invalid-input/existing-
manifest checks exited 2 without changing their existing fixture files.

A separately retained core built by the same command was a static Linux arm64
ELF. Its complete `internal/routedbridge` race/shuffle suite passed **15 top-level
tests / 26 subtests**, no skips, in **5.372s**, using the actual stock core for
rejection tests. This covers authentication, actual UDP peers and datagram
boundaries, route decisions, hot additions, cancellation and core-exit cleanup.

- Core SHA256: `e8209436effac620d60a3790cffd4ddbfb4ba18e279fcccf2d6ba172258b2f1f`.
- Exported source SHA256: `57b6472d2f1efdb19df1461fade46dc3040e490d492f018dcee5cef6e7e91ccc`.

Both modified workflows passed **actionlint 1.7.12**; shell syntax, Python AST
and YAML parsing also passed. Official **Compose 5.5.1** evaluated the actual
compose file without starting a daemon: missing `XUI_SOURCE_COMMIT` exited 1;
providing the full commit produced the expected source argument and default
`local` tag with exit 0. Both standalone validation tools were downloaded from
their official releases and checked against the published SHA256s.

Logs under `/tmp/3x-ui-rate-trace/`: `release-packaging-red.log`,
`release-packaging-first-build.log`, `release-packaging-source-diagnostic.log`,
`release-packaging-native-green.log`, `release-packaging-core-race.jsonl` and
`release-packaging-actionlint.log`. No application Go or embedded asset changed
in this packaging increment, so the preceding complete Go regression remains
the application-source check; the fresh core used the focused data-path suite.

No Docker engine or Windows runtime was available here, and the release/image
workflows were not dispatched. Optional geodata/MTProto and toolchain/image
selection still contain dynamic upstream inputs; full artifact reproducibility
is not established by the managed-core/source comparison. Existing installer,
menu and web updater activation/rollback remain unfinished. No release or image
was published, and no host installation or service was changed.

## Fork download and actual updater preflight (2026-09-29)

The production download/prepare path now selects only the managed fork, resolves
full tag commits, downloads numeric asset IDs with mandatory sidecar and API
SHA256 checks, verifies the complete manifest, and optionally executes the
candidate's existing managed-core preflight. `update.sh` uses it before any
package-manager or service-stop action and installs bundled scripts/units.
It no longer downloads upstream update assets or tolerates missing checksums.

Focused race/shuffle validation: **37 top-level and 165 subtests passed, no
skips**, across `internal/updatebundle` and `tools/update-stage`. Cases include
latest/explicit/prerelease and annotated-tag selection, redirects, moved tags,
missing/duplicate/pending/oversized assets, exact response bounds, truncated and
corrupt transfers, recursive tag limits, checksum filename/digest disagreement,
proxy-credential error privacy, CLI identity arguments and stage cleanup.
The HTTP fixtures use actual loopback exchanges with the fixed repository path;
they do not constitute acceptance of a published GitHub release.

The first stalled-body test canceled too early and passed without reaching the
suspected read path. A transport read-entry barrier then reproduced both lost
cancellation causes (metadata and archive). Preserving the context error fixed
those failures without changing the expected behavior. CLI preflight tests
first failed on the missing flag, then exercised actual owned executable
fixtures and checked that failed candidates never publish a stage.

The reproducible whole-script probe is
[`tools/managed-release/probe_update.py`](../../tools/managed-release/probe_update.py):

```sh
CGO_ENABLED=0 go build -p 1 -trimpath -o /trusted/tools/update-stage ./tools/update-stage
sudo python3 tools/managed-release/probe_update.py \
  --helper /trusted/tools/update-stage --panel /trusted/clean-source-panel \
  --managed-core /trusted/managed-xray --stock-core /trusted/stock-xray
```

It requires Linux, namespace/mount privileges, Python, `ip`, and a static
`/usr/bin/busybox`. Before any network or mount action, each child checks PID 1,
private proc/net/mount identities and an owned temporary root marker. It enables
only that namespace's loopback and chroots before running the actual updater.
Package-manager, curl and service-manager fixtures cannot affect the host.

**Nine cases passed**: missing/wrong bootstrap checksum, bad archive checksum,
corrupt gzip, missing panel, missing required unit, wrong compiled source,
actual stock-core rejection, and actual managed-core success followed by a
refused service stop. Old program/core/unit/menu/DB hashes were preserved in
all cases, failure status was recorded, and temporary update directories were
removed. Invalid release cases performed no package-manager, stop or global
process-kill action. The valid candidate reached the stop request only after
its real managed HMAC preflight; the refused stop preserved program files.
The probe initially omitted private-loopback setup, causing the valid managed
candidate to time out; enabling loopback after the isolation guards fixed the
fixture. A checksum assertion was also corrected to the actual SHA256 error
text; the invalid hash remained unchanged.

The panel fixture was the clean-source Linux arm64 build of `faaa4098`; its
compiled declaration was checked. The test bundles combine this candidate with
current helper/scripts and an explicitly constructed fixture manifest. They
are interoperability/ordering fixtures, not source attestations or published
release acceptance. The managed core is the separately rebuilt packaged core;
the stock core is the separately pinned upstream binary. Business DB files are
sentinels here, not schema-migration fixtures.

Full repository lint and the final affected-package lint passed with zero issues.
The final native helper passed all nine isolated script cases; Windows amd64 and
macOS arm64 helper builds passed (compile checks only). Logs under
`/tmp/3x-ui-rate-trace/`: `release-download-api-red.log`,
`release-download-cancel-read-red.jsonl`, `release-prepare-cli-red.jsonl`,
`release-prepare-final-race.jsonl`, `release-download-lint.log`,
`updater-managed-preflight-red.jsonl` and `updater-managed-preflight-final-green.jsonl`.

This closes the demonstrated pre-extraction deletion defect for validation
failures in the standalone updater. Copy/start failure after a successful stop,
program/DB transactional rollback, crash recovery, successful migration/health
activation, first installer, menu/web update entry points, Docker/native foreign
platform execution, and the separate Xray updater remain open. There was no
host installation, service operation, release publication or image deployment.

## Verified installed updater entry points (2026-09-29)

The regular stable/dev menu updates and authenticated web update now prepare a
verified local copy of the installed release's `update.sh`. They no longer fetch
an upstream `main` updater. Preparation matches the compiled unmodified source
and platform, checks the strict manifest and bounded script bytes/mode, and
places the copy outside the installation. Runtime-generated files are allowed;
fresh release verification still checks the full exact inventory.

Version lookup now reuses the download selector's fixed-fork/full-tag resolver.
Development availability compares full commits, even when their first eight
characters are identical. The obsolete release-body/short-prefix parsing helpers
and their tests were replaced by actual HTTP resolver tests and full-identity
comparison tests; those old helper semantics are no longer used in production.

The affected package race/shuffle run passed **76 top-level and 209 subtests**
across the updater library/tools and panel service, with **zero skips**. The
normal-binary root CLI and controller run passed **10 top-level and 13 subtests**;
its **one skip** is the existing `TestUpdatePanel_UnsupportedPlatformReturnsNoRunId`
non-Linux test running on Linux. Real managed/stock cores, standalone metadata,
preparation without database side effects and cancellation cleanup were exercised.
No skipped test is counted as a pass.

The shared manifest parser retained all existing strict-inventory tests. New
installed-file tests cover runtime files/symlinks, preserved runtime content,
installation rename after preparation, source/platform/tag mismatch, missing or
changed scripts, executable mode, oversized scripts, symlinked inputs, canceled
contexts and in-installation staging parents (including aliases). API/CLI tests
first failed before the new functions/command existed. The Docker guard first
failed because the implementation looked for host tools before rejecting the
container; it now rejects before that lookup.

The actual-process probe has additional modes:

```sh
# Use a normal native panel, not a race/test binary.
sudo python3 tools/managed-release/probe_update.py --menu \
  --helper /trusted/tools/update-stage --panel /trusted/clean-source-panel \
  --managed-core /trusted/managed-xray --stock-core /trusted/stock-xray
sudo python3 tools/managed-release/probe_update.py --web \
  --helper /trusted/tools/update-stage --panel /trusted/clean-source-panel \
  --managed-core /trusted/managed-xray --stock-core /trusted/stock-xray
```

**Three menu cases passed**: stable and dev requests reached only the correct
fork helper channel, reported invalid-download failure with nonzero exit status,
and removed the private script copy; a changed installed updater was refused
before any download. The old menu's real failure reproduction showed an upstream
raw-script request and exit status zero despite the failed download. The first
chroot fixture lacked `/dev/fd`; after adding its private-proc symlink, the
reproduction exercised the intended upstream request rather than that fixture
omission. Old file hashes and DB sentinels remained intact.

**Four authenticated web cases passed** using the actual native panel, an owned
SQLite database, a synthetic administrator API token and real loopback HTTP
inside each guarded chroot. Stable/dev requests launched the actual detached
updater, and polling returned the same run ID with the expected failed download.
The live panel stayed running, old program/core/unit/menu hashes were unchanged,
and an existing database setting survived. Changed-script and Docker requests
were rejected before launch, with no download or status-file creation. English,
Chinese and existing English fallback responses were checked for the container
message. A separate actually modified-source build, despite its explicit full
commit stamp, was also rejected through the authenticated API before launch.

The web fixture initially lacked Python's lazily loaded IDNA codec inside the
chroot; codecs are now loaded before entering the guarded root. All HTTP,
package-manager, service and download operations remain confined to the owned
fixture; no host deployment or network rules are involved. A localization test
first reproduced a Chinese failure title with an untranslated new detail; the
new error messages now have explicit English/Chinese entries and use the
repository's normal fallback. Failed starts also use the existing failure title.

Normal pre-commit fixtures use `-buildvcs=false` and an explicit `f` repeated 40
times source declaration; they are execution fixtures, not source attestation.
The modified-source probe uses `-buildvcs=true` against the actually dirty tree.
Building the full panel with `CGO_ENABLED=0` failed at the existing SQLite backup
API; the normal CGo build succeeded. The independent update helper remains a
static build. Actual published-release acceptance and foreign-platform runtime
execution are not established by these probes.

Logs under `/tmp/3x-ui-rate-trace/`: `installed-updater-api-red.log`,
`installed-updater-cli-red.jsonl`, `installed-updater-container-red.jsonl`,
`installed-updater-menu-source-red.log`, `installed-updater-resolver-red.log`,
`installed-updater-full-commit-red.log`, `installed-updater-final-focused.jsonl`,
`installed-updater-final-root-controller.jsonl`,
`installed-updater-menu-green.jsonl`, `installed-updater-web-localized-green.jsonl`,
`installed-updater-web-dirty-localized-green.jsonl`, `installed-updater-localization-red.jsonl`
and `installed-updater-final-lint.log`.

Final repository-wide Go lint reported zero issues. The independent helper built
with CGo disabled for native Linux arm64, Windows amd64 and Darwin arm64; the
foreign binaries were compiled only. Shell syntax, Python syntax and both changed
translation JSON files also passed validation. Prettier was not available in the
frontend toolchain; its attempted check did not run. The repository's frontend
formatter is oxfmt, with existing checks scoped to frontend sources/tools.
The rebuilt final helper also passed all nine actual isolated archive/bootstrap/
preflight cases, including managed-core acceptance followed by a failed service
stop with old files preserved (`installed-updater-final-archive-probe.jsonl`).

This validates preparation and failure handling through the regular menu/web
entry points. First installation, menu refresh/legacy paths, independent core
updates, successful upgrade activation, consistent SQLite/PostgreSQL backup,
crash recovery and transactional program/database rollback remain open. The
other outstanding protocol/policy acceptance items are unchanged.

## Standalone installer validation and first SQLite installation (2026-09-29)

The actual old installer failed the new isolated acceptance probe: before any
managed-release verification it invoked the package manager and requested the
upstream latest release. The new standalone installer uses the same bounded
fixed-fork bootstrap, complete release validation and actual managed-core
preflight as `update.sh`. The duplicated bootstrap functions keep both scripts
standalone; the process probes exercise both paths. Upstream raw-script/unit
fallbacks, optional missing checksums, deletion of the old installation tree and
global process-name kills were removed from the install path.

**Nine existing-install and nine fresh-install negative cases passed.** The cases
cover missing/wrong helper checksums, wrong archive checksum, corrupt archive,
missing panel/unit, wrong compiled source and a real stock core. A valid managed
candidate reaches the explicit service-stop refusal in the existing case, or
package-manager refusal in the fresh case. Old file hashes and the database
sentinel survive; invalid fresh candidates do not create an installation, menu
or unit. Owned staging trees are removed.

The success probe performs an actual first SQLite installation using the native
panel and managed core, checks every installed release file against the manifest,
checks the installed menu/unit and mode-0600 credential file, then starts the
installed panel and performs authenticated loopback HTTP. The database is created
by the installer; the HTTP fixture does not recreate it or change its selected
port/base path. It adds an unrelated setting and verifies startup preserves it.
The final first-install case passed with no early service restart or migration
error. Synthetic credentials are confined to the owned fixture.

That success test exposed two distinct issues. Its initial chroot lacked a root
account, so real `chown root:root` failed; adding fixture passwd/group entries
resolved that environment omission. The subsequent real run exposed premature
service restart during configuration and the existing `empty slice found`
migration error. Panel service operations are now deferred during configuration,
including renewal commands registered by this installer. The migration avoids
saving an empty legacy-proxy slice. New tests first reproduced the error on both
SQLite and isolated PostgreSQL, for fresh and SSH-only databases, then verified
repeatable migration and unchanged SSH configuration. The migration race/shuffle
run passed **7 top-level and 8 subtests, zero skips**.

Reproduce with trusted native binaries and owned namespace/mount privileges:

```sh
python3 tools/managed-release/probe_update.py --install \
  --helper /trusted/update-stage --panel /trusted/native-panel \
  --managed-core /trusted/managed-xray --stock-core /trusted/stock-xray
# Add --fresh-install for negative first-install cases, and also
# --install-success to run the positive first SQLite installation case.
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
  go test -p 1 -race -shuffle=on -count=1 -run '^TestMigration' ./internal/web/service
```

The pre-commit native panel uses an explicit full fixture source declaration
with VCS recording disabled; it is not source attestation. Package installation,
public network calls and service management are substitutes inside each guarded
chroot. A marked executable stands in for the optional TUIC asset, so this test
does not exercise TUIC. Actual systemd/OpenRC execution, ACME issuance/renewal,
PostgreSQL installation, foreign platforms and custom service paths remain
unverified. These tests do not establish transactional program/database rollback,
safe downgrade, crash recovery or other outstanding protocol/policy acceptance.

Logs under `/tmp/3x-ui-rate-trace/`: `installer-entrypoint-red.log`,
`installer-first-success-initial.jsonl`, `installer-first-success-red.jsonl`,
`installer-empty-migration-red.jsonl`, `installer-migration-green.jsonl`,
`installer-first-success-green.jsonl`, `installer-existing-final.jsonl` and
`installer-fresh-final.jsonl`.

Shell/Python syntax and `git diff --check` passed. The first full Go lint run
reported one formatting issue in the new test; after formatting correction,
the final repository-wide run reported zero issues
(`installer-final-lint-corrected.log`).

## Verified menu maintenance and installer bootstrap (2026-09-29)

Menu refresh now restores a verified `x-ui.sh` from the running panel's installed
release, using `prepare-menu` before an atomic same-directory replacement of the
control menu. Mode 0755 is set before replacement; refresh neither downloads nor
restarts the panel. First menu installation downloads only the fork's bounded
standalone installer and mandatory checksum. The existing `legacy` command now
validates an exact fork tag and invokes the installed verified updater without
`eval`. The Linux amd64 packaging job exports the portable installer/checksum
once; the workflow has not been published or executed remotely.

The old actual menu reproduced three failures inside guarded chroots: refresh
fell back to upstream main; installer download failure still requested service
start and returned zero; a shell expression in the legacy tag created a harmless
owned `/fixture/injected` marker through `eval`. The new code rejects that same
input before any download, and the marker is absent. A malformed checksum already
failed closed but initially emitted no explanation; its additional diagnostic
assertion failed first and now receives an explicit invalid-checksum message.

The final **10 maintenance cases passed**: matching refresh, changed menu, missing
manifest, selected managed tag, injected tag, missing checksum, wrong checksum,
wrong sidecar filename, oversized installer and successful menu installation.
The final case goes through both script bootstraps, real candidate/core preflight,
actual first SQLite initialization and authenticated HTTP from the installed
panel. All installed release files/menu/unit match the expected hashes and
temporary files are removed. The oversized response is stopped by the bounded
stream, including the expected fixture broken-pipe error; it is never executed.
The existing **3 regular menu and 4 actual HTTP update cases also passed** after
the shared verifier/probe changes. No skipped case is counted as a pass.

The shared library/tool race/shuffle run passed **42 top-level and 182 subtests,
zero skips**. The normal-binary preparation regression passed **1 top-level and
3 subtests, zero skips**. Repository-wide Go lint reported zero issues; shell
syntax, Python syntax, whitespace checks and official actionlint 1.7.12 checks
for the changed release workflow passed.

```sh
python3 tools/managed-release/probe_update.py --menu-maintenance \
  --helper /trusted/update-stage --panel /trusted/native-panel \
  --managed-core /trusted/managed-xray --stock-core /trusted/stock-xray
# Add --case menu-legacy-invalid to reproduce only the tag rejection.
go test -p 1 -race -shuffle=on -count=1 ./internal/updatebundle ./tools/update-stage
```

These are owned Linux arm64 fixtures with actual binaries, SQLite and loopback
HTTP. Network downloads and service/package managers remain isolated substitutes.
Pre-commit binaries use a full declared fixture identity, not provenance
attestation. No release asset has been published, and actual service-manager,
container and foreign-platform runtime acceptance is unchanged. Independent
core updates, transactional activation, database downgrade/rollback and the
outstanding original protocol/policy requirements remain open.

Logs under `/tmp/3x-ui-rate-trace/`: `menu-prepare-red.log`,
`menu-prepare-library-green.jsonl`, `menu-refresh-red.log`,
`menu-legacy-marker-red.log`, `menu-install-red.log`,
`menu-checksum-diagnostic-red.log`, `menu-maintenance-final.jsonl`,
`menu-regular-regression.jsonl`, `menu-web-regression.jsonl`,
`menu-root-cli-regression.jsonl` and `menu-final-lint.log`.


## 2026-09-29 — managed fork core updates and error recovery

The independent core updater now selects this fork's complete Linux release,
verifies its full tag commit, asset/checksum/inventory and actual managed runtime,
then changes the core under the existing lifecycle mutex. The old executable and
working configuration are restored after activation failure. No business database
snapshot is restored. The UI separates package tags/prereleases from the running
(or last reported) Xray version; the old string-list endpoint remains a tag-list
compatibility representation. Container and unsupported-platform guards fail before download.

Observed failures and corrections:

- New catalog, subprocess preflight and file transaction APIs initially failed
  their contract tests because those APIs were absent. The old service downloaded
  stock XTLS ZIP files after stopping the live process; that path was removed.
- The first actual process test invocation was denied loopback sockets by the
  sandbox. The authorized isolated-socket rerun passed; the denied run is not
  counted as a product failure or as successful coverage.
- Removing the obsolete core digest helper exposed geodata's shared digest-limit
  constant at compile time. Geodata now retains its own unchanged 64 KiB limit;
  its checksum/update regression tests pass.
- The frontend's new error handling exposed a synchronous-effect lint error.
  The catalog request now updates state through its promise callbacks, discards
  closed-dialog responses, and clears choices on a new dialog opening.
- The Ukrainian API test exposed an empty failure title when a new translation
  was absent. Core update titles/errors now explicitly fall back to English.
- A running native core could otherwise be reported as successfully replaced
  immediately after exec. The new native SOCKS test injects exit 29 after real
  configuration/version validation and verifies restored payload routing through
  the old configuration despite a changed, valid desired route.
- The HTTP server has a 30-second write deadline, too short for a whole release.
  Only the authenticated core-install response receives a nine-minute deadline
  for its eight-minute operation plus recovery. A real HTTP/gzip test outlives a
  short normal server deadline and still receives the complete response.

Current focused evidence (race, shuffle and count=1 for Go):

| Run | Result |
| --- | --- |
| Complete updatebundle + update-stage tests | 49 top-level, 208 subtests passed; 2 packages, zero skips |
| Core, ordinary restart, bind, SSH/mieru, geodata and update-controller regression | 33 top-level, 67 subtests passed; 2 packages; two skips recorded below |
| PostgreSQL core activation, actual OpenSSH outbound, localized API and HTTP/gzip deadline | 4 top-level, 14 subtests passed; 3 packages, zero skips |
| Additional plain native recovery and locale contract run | 2 top-level, 6 subtests passed; zero skips |
| Frontend managed release picker | 2 component tests passed; typecheck, lint and production build passed |

The wider focused run skipped `TestSSHOutboundRunsThroughProductionXray` because
`SSH_E2E_SERVER` had not been supplied; it was then rerun successfully with the
owned OpenSSH fixture using `/usr/sbin/sshd`. The other skip is the existing
`TestUpdatePanel_UnsupportedPlatformReturnsNoRunId`, which deliberately exercises
only non-Linux behavior. These are not new successful runtime cases.

Actual SQLite download/preflight tests cover incorrect checksum, mismatched full
source commit, stock-core rejection and managed-core activation. They exchange
mieru TCP and UDP payloads while download is in progress and afterward: exact
raw counters are 132 bytes in each direction, billed once at 1.5x for 396 bytes.
SQLite and isolated PostgreSQL activation matrices cover success, startup exit,
API readiness timeout, cancellation during startup, invalid desired configuration,
missing API, pre-canceled requests and deliberate manual stop. Each additional
successful echo round adds exactly 44 bytes per direction and 132 billed bytes;
manual-stop updates add none. An ordinary reconciliation queued during canceled
startup waits until executable/process recovery releases the lifecycle mutex.

The download tests use current Go service code, fixed-fork HTTP transport fixtures,
an actual clean panel candidate from commit 61efa40f, a real managed core and a
real stock core. They are not a published-release acceptance test. Placeholder
nonexecuted service/script assets satisfy the test bundle inventory only.
Crash-durable journals/boot recovery, shell-versus-panel cross-process coordination,
transactional panel/SQLite/PostgreSQL activation with admission barriers, and actual
container/foreign-platform execution remain open, along with the earlier original
protocol/policy requirements. The previous full-repository suite is historical;
these results must not be described as a new full-repository test pass.

Logs under `/tmp/3x-ui-rate-trace/`: `managed-core-replacement-red.log`,
`managed-core-lifecycle-red.log`, `managed-core-service-api-red.log`,
`managed-core-ui-red.log`, `managed-core-http-deadline-red.log`,
`managed-core-library-final.jsonl`, `managed-core-regression-final.jsonl`,
`managed-core-pg-deadline-ssh.jsonl`, `managed-core-native-controller-green.jsonl`,
`managed-core-download-runtime.jsonl` and `managed-core-frontend-build.log`.

Full-repository `golangci-lint run --timeout=5m` completed with zero issues.
The production frontend build generated 188 OpenAPI paths / 200 operations and
completed successfully; the generated OpenAPI changes are included.

The refactored staging helper also builds as a static Linux arm64 binary and
as Windows amd64 / Darwin arm64 command binaries. The foreign builds are compile
evidence only; those platforms explicitly reject runtime activation preflight.

The rebuilt native static helper passed all nine existing isolated process
bootstrap/archive/source/core-preflight/service-stop-failure cases. Old program,
unit and database fixture bytes remained intact on rejection. The real managed
core passed preflight before the fixture intentionally refused service stop.
This shell probe does not test successful panel activation or database rollback
(`managed-core-helper-process.jsonl`).
