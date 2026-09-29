# Verification record

Only executed evidence counts. Unverified, unimplemented, and justified not applicable are not passing tests. Full required tests are in requirements §15 A–H and the implementation plan.

Environment: Linux arm64, 2 CPUs, isolated checkout from fork main `17d7dd46`; Go 1.27.1; checksum-verified Node 26.10.0/npm 11.19.1. Tests use loopback/temp directories and ephemeral endpoints. No production listeners, firewall or host management SSH changes.

## Baseline and build checks — 2026-09-28

| Check | Command / input | Actual |
| --- | --- | --- |
| Branch origin | `git rev-parse HEAD main` after creation | both `17d7dd46b512d0a9c22921a6094f30c672e436c9`; clean |
| Source import | compare every indexed upstream blob at `23f7c784` | all 1039 match pinned upstream; original notices retained |
| Git push | explicit feature ref, then `git ls-remote` | source commits pushed; remote feature SHA `23f7c784ad3d04e39a66af69d1b3bb229b8a139c` |
| Go dependencies | `go mod download` | passed with network permission |
| Panel with managed core | `make test-go` | passed; initial restricted-socket failures rerun with authorization; nested-module AST guard corrected after reproduced failure |
| Frontend dependencies/build | Node 26 `npm ci`, `npm run build` | passed; npm audit reported 0 vulnerabilities |
| Frontend full tests | `npm test` | initial concurrent run failed with 16 timeouts and 2 teardown errors; isolated `npm test -- --maxWorkers=1` passed all 174 files / 1742 tests in 426.18 s, unchanged timeouts |
| Panel build | `go build -o build/x-ui .` | passed |
| Custom core build | `bash tools/build-custom-core.sh` | passed; distinctive Custom Xray-core 26.9.9-custom.1 version and source stamp, SHA256 output |
| Core full suite | `go test -shuffle=on -count=1 ./...` | failed in testing/scenarios: WireGuard nil MemoryStreamConfig panic, then timeout; same nil MemoryStreamConfig panic independently reproduced at immutable upstream `52a412d9e2f5`, with an isolated core.New reproducer; fix/full rerun pending |
| Targeted static checks | `go vet ./app/clientpolicy ./app/dispatcher ./proxy/dokodemo` | passed |

No full-suite pass, PostgreSQL validation, clean-clone build, platform matrix or absence of skipped upstream tests is claimed.

## Runtime and actual Tunnel traffic

Implementation under test: working tree based on `23f7c784`; these results describe the following committed source changes, not the earlier imported binary. In-memory enforcement only; durable recovery remains unfinished.

`GOTOOLCHAIN=go1.27.1 go test -race -count=1 ./app/clientpolicy ./app/dispatcher ./common/protocol ./infra/conf ./proxy/dokodemo ./testing/policy` passed. New arithmetic and engine behavior was introduced after observed failing tests; a mutation test verified natural channel release must not kill a shared transport.

| Test/input | Observed result | Scope limit |
| --- | --- | --- |
| Multiplier 0.5/1/1.5/2/10, fractions, batch split, overflow | exact fixed-point results, preserved remainder; invalid values rejected | fuzz seeds run; sustained fuzzing pending |
| 16 sessions, shared 1536-byte quota, multiplier 1.5 | exactly 1024 raw bytes admitted; all sessions closed | engine concurrency, not 16 real sockets |
| Hot multiplier 1→2 after 10 GiB, then 5 GiB | 20 GiB billed; history unchanged | synthetic engine admission |
| Expiry/disable/quota/revocation updates | close idle sessions; reasons compose; stale version rejected | panel lifecycle pending |
| TCP through two owned Tunnel aliases plus UDP | upload 2048, download 2048, billed 8192 at multiplier 2; 3 sessions share one ID | fixed loopback target |
| Disable above live client | existing TCP sockets close; UDP no longer returns payload; active count 0 | listener reassignment/ACL pending |
| Selected SOCKS outbound; default block | exact 4096 bytes each direction, billed 12288 at 1.5; denied route never dials echo target | test upstream is another listener in same core |
| Managed identity without policy | target receives no connection | explicit trusted listener ID |
| 100 MiB quota, multiplier 2, real TCP echo | admitted upload 26,214,400 + download 26,214,400; billed 104,857,600; active count 0; reconnect denied | quota-triggered UDP termination still pending |

In the 100 MiB test, the sender wrote 26,214,400 bytes and receiver read 26,206,208. The 8192-byte difference is admitted data discarded by immediate quota termination. It falls within the test's predeclared 64 KiB endpoint/admission uncertainty. This measures admission, not acknowledged delivery, and does not prove a universal 64 KiB bound across protocols.

## Independent binary rate measurements

`python3 tools/test-custom-tunnel-rates.py --binary build/custom-xray` passed all six cases. Each starts a separate actual core process and two TCP connections sharing one client. Upload and download are measured separately at the receiving endpoint; exact machine-readable observations are [tunnel-rates.jsonl](evidence/tunnel-rates.jsonl).

| Direction | Aggregate configured rate | Measured bytes/s | Measurement window |
| --- | --- | --- | --- |
| Upload | unlimited | 1,378,987,438 | 2.0001 s |
| Upload | 262,144 B/s | 261,425 | 10.0001 s |
| Upload | 1,048,576 B/s | 1,047,134 | 10.0001 s |
| Download | unlimited | 773,859,437 | 2.0015 s |
| Download | 262,144 B/s | 262,347 | 10.0001 s |
| Download | 1,048,576 B/s | 1,048,671 | 10.0001 s |

Limited cases warm up for 2 s and have a 65,536-byte shared directional burst. Assertions were fixed before the successful run: upper bound `R × T × 1.01 + B`, lower bound `R × T × 0.85`, and the same startup-inclusive upper bound. Unlimited controls exceed 4 MiB/s. Running the same test against the pre-policy binary failed at the first limit (15,784,326,142 bytes in ~10 s at configured 262,144 B/s). No limits were relaxed after failure.

These are local TCP Tunnel measurements. They do not establish UDP, every protocol, many-client fairness, cross-node rates, persistence overhead, or hot updates on real sockets. Those acceptance tests remain open.

Keep credentials out of logs and reports. Restricted socket failures require authorized reruns, never skipped tests or weakened expectations. A baseline failure is not classified as pre-existing until isolated upstream comparison proves it.

## Durable local execution state — 2026-09-28

The next implementation requires an explicitly initialized private state file and matching instance ID whenever `clientPolicy` is configured. The in-memory engine constructor remains for isolated primitive tests; configured core traffic uses durable state. `policy-init` refuses existing files. Missing, corrupt, wrong-identity or concurrently locked files cause startup failure.

New tests cover exact graceful restart (raw directions, fractional multiplier history, versions, revocation), a child process calling `os.Exit(23)` without shutdown, before/after-commit injected failure, a 131,073-operation one-byte stream using three reservation commits, rejected batch atomicity, and malformed state. Targeted race results are recorded with the commit checkpoint below. These fault injections are distinct from a physical power-loss/filesystem durability test, which has not run.

The abrupt-exit input consumes 65,544 raw bytes at multiplier 2. Recovery retains 65,536 confirmed raw upload bytes / 131,072 billed bytes, and freezes a further 131,072 uncertain billed bytes. It does not fabricate raw counters for the last 8 bytes. The remaining budget can be spent once; reconnect fails when confirmed plus frozen usage reaches quota. Normal checkpoint/shutdown preserves exact admitted usage and releases unused reservation, with no uncertainty charge.

Real Tunnel restarts: three full core instances sequentially reopen the same store; each echoes 1024 bytes in both directions at multiplier 2. Totals progress 4096 → 8192 → 12288 billed bytes; reopening again retains 12288 and epoch 4, with no uncertain usage. Rejected configuration testing first reproduced a leaked file lock; cleanup and deferred initial policy application now preserve the existing policy and release the lock.

The same six independent-process rate cases were repeated with durable reservations. [tunnel-rates-persistent.jsonl](evidence/tunnel-rates-persistent.jsonl) contains the raw observations. Upload rates measured 263,780 / 1,037,099 B/s; download 262,347 / 1,048,157 B/s. All original rate/burst/healthy-throughput assertions passed. Unlimited controls were 29,070,308 B/s upload and 34,310,353 B/s download. This is a substantial local throughput cost versus the earlier in-memory controls; no general high-throughput acceptance claim is made.

Remaining durability work: panel DB idempotent settlement, restore/rollback fencing, multi-node budget leases, full configuration-start rollback, disk/power-loss tests and further throughput work. Runtime Snapshot combines current live usage with a durable sequence marker; it is **not** an atomic committed ledger event and must not be used as one by the panel.

Additional checks found two further baseline issues: adding the upstream `core` package to race testing failed in `testing/servers/udp/udp.go` (shared `Server.accepting` flag assignment/close); broader `go vet ./infra/conf` reports unreachable legacy reverse configuration code after its removal error. These are failures, not skipped/passed checks. Targeted `app/clientpolicy` and `testing/policy` tests in that run passed; baseline fixes and exact reruns remain separate work.

Persistence checkpoint validation: focused race tests across clientpolicy/dispatcher/protocol/conf/Tunnel passed; `go vet` of clientpolicy/dispatcher/core/testing-policy passed; full panel `make test-go` with the persistence dependency passed. These do not erase the separately recorded full-core/scenario/race/vet baseline failures.

Baseline repair checkpoint: the missing WireGuard stream-settings regression first panicked, then passed after applying empty optional settings; the actual UDP core tests now pass under race after removing the racy helper flag (socket close terminates its loop). `go vet ./infra/conf` passes after removing code that was already unreachable after the existing legacy-reverse removal error. No removed feature was re-enabled or disabled by that cleanup. Full core/scenario rerun is still pending at this checkpoint.


## Protected control service and panel adapter

At baseline-repair commit `1d3f5525`, the complete `go test -shuffle=on -count=1 ./...` managed-core suite passed (scenario package 352.548 s). This was the fixed source snapshot compiled before the following API work; it does not claim every upstream test contains data-transfer assertions or that skipped paths were validated.

API v1 tests first received gRPC Unimplemented, then exposed the generic Unix listener's synthetic TCP peer address. The protected API now uses an actual Unix listener while retaining the original listener path for other API services. `go test -race -count=1 ./app/clientpolicy/... ./app/commander ./testing/policy ./infra/conf` passed. Tests verify private transport requirements even for direct protobuf config, rejection of TCP/no peer, socket permissions, live RPC rate/multiplier changes, atomic committed counters, cursor replay/pagination, ahead-of-store cursor rejection, and closing an existing TCP socket. Exact hot-update accounting and method semantics are in [control-api.md](control-api.md).

Panel `go test -race ./internal/xray -run TestClientPolicyAdapter -count=1` passed against actual local gRPC transports. The fixtures test capability negotiation only: absent service, incompatible version, wrong instance and missing enforcement capabilities fail explicitly; a fully matching service succeeds. These are not additional protocol interoperability tests. Runtime/DB/UI integration remains unfinished.

API checkpoint: final focused core/API race checks, focused vet, rebuilt Custom Xray-core and full panel `make test-go` passed. Final lifecycle regression also confirms a closed engine no longer advertises readiness. The new adapter has not yet been wired into production panel Runtime.

## Panel identity, transactional receipts and legacy seed — 2026-09-29

SQLite identity migration tests first failed on missing stable-ID support, then passed. A separate partial-migration test reproduced duplicate empty IDs blocking unique-index creation; moving the backfill before index creation fixed it. PostgreSQL 16.15 was downloaded as Ubuntu packages and unpacked into a private temporary test directory, run as `nobody` with TCP disabled; no system service was installed or changed. Tests use isolated schemas and close their pools. Both missing-column and partially nullable/empty-column PostgreSQL migrations passed under race, preserving credentials, quotas and old `111/222` directional counters.

Ledger tests cover seed preservation, multipliers already applied in core, duplicate/out-of-order pages, invalid/regressing counters, signed overflow, wrong source/epoch, cursor gaps, whole-page rollback, 20 concurrent retries, and a deleted client's final receipt remaining on its original UUID. SQLite and PostgreSQL focused race runs passed. PostgreSQL additionally executes a real deferred foreign-key violation at transaction commit: totals, per-client receipt and source cursor all roll back; replay after removing the injected violation commits exactly once.

Core initialization tests first failed on missing methods; the private RPC test first returned `Unimplemented`. Implementation now passes core policy/API/Tunnel scoped race tests. A 100-byte quota seeded with 30 billed bytes admits only the remaining budget. Identical initialization retries after traffic, policy changes and restart do not reset counters; conflicting seeds, existing unseeded identities and revoked identities are rejected. Fault tests cover an error before the initialization write and an error returned after that write committed.

`TestClientPolicyLedgerRealTunnelAndRestart` passes with SQLite and PostgreSQL under race. The account begins with 100 upload / 200 download / 300 billed historical bytes. Each full core run echoes an independently observed 1024 bytes each way at multiplier 2. Panel totals become `1124/1224/4396`, then after restarting the same core store `2148/2248/8492`, with zero uncertainty. Each real gRPC ledger page is submitted twice. The test explicitly exercises the panel adapter, real Tunnel sockets and SQL settlement; it is not a full browser/production Runtime acceptance test.

`TestClientPolicyCrossDatabaseMigration` verifies IDs, policy sources, cumulative totals, fractional remainder and receipts survive SQLite→PostgreSQL copying. An old source without the new identity column/tables initially failed the unique index; migration now generates identities while copying without modifying the source, accepts all-three policy tables absent as a legacy schema, and rejects an incomplete policy-table set. This migration test passes under race. Portable per-client export/restore fencing remain unfinished.

Reproduction (Go 1.27.1; set `XUI_DB_TYPE=postgres` and `XUI_DB_DSN` to an isolated test database for PostgreSQL-only cases):

```sh
go test -race ./internal/database ./internal/web/service -run 'TestClientStableIdentity|TestClientPolicyLedger|TestClientPolicyCrossDatabaseMigration' -count=1
go test -race ./internal/xray -run TestClientPolicyAdapter -count=1
(cd core/xray && go test -race ./app/clientpolicy/... ./testing/policy -count=1)
```

When PostgreSQL is not configured its explicitly gated tests are skipped and do not count as PostgreSQL evidence. Here the PostgreSQL commands were separately run against the real private instance. Frontend generation/build passed after adding the response identity field; full frontend and full panel verification results will be recorded at the commit gate.

Commit-gate checks: generated schemas/OpenAPI (also copied to the docs site), frontend build and TypeScript checks passed. Full frontend `npm test -- --maxWorkers=1` passed 174 files / 1742 tests in 447.99 seconds without changing timeouts or assertions. Final focused core policy/API/Tunnel race, core/panel focused vet, panel adapter race, PostgreSQL identity/ledger/Tunnel/migration race and custom-core binary build passed.

The first full panel `make test-go` run failed in the unchanged Discord test `TestGatewayRequestedHeartbeatDoesNotRaceTicker`; its 10 ms fixture heartbeat missed an ACK and closed the socket under concurrent package load. The test then passed 30 consecutive isolated runs. No Discord code, expected result or timing threshold was changed. A full `GOFLAGS=-p=1 make test-go` rerun is recorded separately below; the failed concurrent run is not a passing result.

The full panel rerun `GOFLAGS=-p=1 GOTOOLCHAIN=go1.27.1 make test-go` passed. It includes the original Discord test unchanged. The isolated 30/30 pass plus the serial suite support scheduling contention as the explanation for the earlier parallel failure; no claim is made that the 10 ms fixture is load-independent.

## Runtime private control transport — 2026-09-29

`TestLocalRuntimeUsesPrivateControlForHandlersRoutingAndStats` first failed on the missing Runtime endpoint methods. It now runs a real managed core with only a private Unix control listener, adds an owned Tunnel through Local Runtime, echoes 256 bytes each way, reads exactly those inbound counters through StatsService, hot-applies a blocking route, and removes the listening resource. Fixture corrections supplied the required policy burst and enabled the router feature before the final successful run; the earlier fixture failures are not passing results.

Safety tests reject non-loopback TCP, invalid ports, abstract Unix addresses, malformed explicit configuration, public socket/directory permissions and socket symlinks. Temporarily removing address/socket validation makes the tests fail; restoring validation passes. The tests also ensure an explicit invalid endpoint never falls back to a legacy API-tag inbound.

```sh
XRAY_E2E_BINARY="$PWD/build/custom-xray" go test -race ./internal/xray ./internal/web/runtime \
  -run 'TestExplicitControlEndpoint|TestPrivateControl|TestLocalRuntimeUsesPrivateControl|TestGetTraffic|TestXrayAPI_E2E|TestClientPolicyAdapter' -count=1
```

This command passed (xray 3.047 s, runtime 1.296 s), including the actual binary's pre-existing TCP handler/routing integration. The binary was built from the managed source including the usage-seed RPC. Full panel `GOFLAGS=-p=1 GOTOOLCHAIN=go1.27.1 make test-go` and focused `go vet ./internal/xray ./internal/web/runtime ./internal/web/service` passed. These tests do not claim automatic policy activation, node-wide budgets or browser acceptance.

## Core startup rollback — 2026-09-29

`TestFailedCoreStartReleasesListenersAndKeepsCommittedPolicy` first reproduced a leaked TCP listener, a held durable-state lock and a falsely running instance after both a real Unix control-address conflict and an injected final-feature failure. Closing all features repaired the resource leaks but the final-feature case still persisted rejected version 2. Deferring the policy commit until the remaining features succeed repaired that second failure. Recovery preserves version 1, enabled state, multiplier 1 and the original 7 raw-upload / 7 billed bytes. The conflicting Unix socket owned by the test remains usable; the failed core's own socket is removed.

The extra multiple-committer case fails when the single-barrier guard is temporarily removed. Restored code passes the focused race regression. The core/policy/control scoped race suite and focused vet passed. The full `go test -p=1 -shuffle=on -count=1 ./...` managed-core suite passed, including the scenarios package in 337.015 seconds. No test thresholds were changed.

## Negotiated panel process startup — 2026-09-29

`TestManagedProcessNegotiatesAndSeedsBeforeOpeningListeners` first failed because `StartManaged` did not exist. The implemented path rejects ordinary startup of a managed configuration and keeps the business listener closed during usage preparation. A real child starts with 100 historical download/billed bytes; a 256-byte echo yields exactly 256 upload / 356 download / 612 billed bytes. A preparation error and a second-listener bind conflict both stop the child and release the first listener. Runtime readiness stays false until successful activation.

`TestManagedProcessRejectsUnmodifiedCoreBeforeBusinessTraffic` runs the actual unmodified upstream binary built from module version `v1.260327.1-0.20260908222543-52a412d9e2f5` without changing its source. Managed control negotiation fails within the test deadline, the child stops, and the managed business port remains closed. This is a real unsupported-core rejection test, separate from the gRPC capability fixtures.

```sh
XRAY_E2E_BINARY="$PWD/build/custom-xray" XRAY_UPSTREAM_E2E_BINARY=/path/to/upstream-xray \
  go test -race ./internal/xray ./internal/web/runtime \
  -run 'TestManagedProcess|TestLocalRuntimeUsesPrivateControl' -count=1 -v
```

With the rebuilt custom binary at core-source commit `26ccc460`, this passed (xray 2.819 s, Runtime 1.460 s). The broader Runtime/API race command including the existing real TCP API regression also passed. Full panel `GOFLAGS=-p=1 GOTOOLCHAIN=go1.27.1 make test-go`, focused vet and the custom-core build passed. CI now builds the pinned upstream binary and requires explicit PASS lines for these process tests; an unset binary environment variable and its resulting skip do not count as evidence.

Production DB cutover, automatic configuration generation, policy reconciliation, durable polling/statistics projection and UI activation remain open. These process tests exercise the real executable and preparation boundary, not that unfinished whole-panel flow.

## Runtime database bootstrap — 2026-09-29

State provisioning tests cover private file permissions, preservation of existing usage, concurrent retry after failed creation, and refusal to recreate missing activated state. A real child-process test seeds 100 upload / 200 download / 300 billed bytes, then exchanges 1024 bytes each way at multiplier 2 across each of two core lifetimes. Totals are 1124/1224/4396 and 2148/2248/8492, with duplicate settlement and no reset. Moving the panel cursor ahead of the core rejects startup and leaves the TCP listener closed. Removing the cursor check makes that test fail.

The same test reproduced a Runtime mutex held across database preparation; a concurrent Runtime operation could not finish until preparation timed out. Narrowing the lock to core RPCs repaired the inversion. Final SQLite and real PostgreSQL 16.15 race runs passed in 3.855 s and 6.612 s, respectively. The sandbox-only attempt could not open a local test socket and is not counted as a passing run.

```sh
XRAY_E2E_BINARY="$PWD/build/custom-xray" go test -race -p=1 ./internal/web/service \
  -run 'TestClientPolicy(StateProvisioning|RuntimeBootstrap)' -count=1
# Repeat with XUI_DB_TYPE=postgres and XUI_DB_DSN pointing to an isolated test database.
```

The CI jobs now build the custom binary and require the child-process bootstrap test to pass explicitly in both databases. Their GitHub execution is not claimed as local evidence. Full production activation and coordinated backup rollback fencing remain unfinished.

The full panel `GOFLAGS=-p=1 GOTOOLCHAIN=go1.27.1 make test-go` passed after this change (service package 50.934 s). Focused `go vet -p=1 ./internal/web/runtime ./internal/web/service` also passed.

## Database policy settings and live client edits — 2026-09-29

The settings tests first demonstrated discarded JSON fields and accepted multiplier zero. Persistence then exposed the separate update-column list for clients without inbounds; that path now preserves an omitted policy and permits explicit unlimited rates/multiplier 1. An attached-client test independently reproduced settings loss during a legacy metadata edit; both the canonical record and inbound settings retain the values after the repair. Invalid rates and multipliers leave no created client row.

Desired-policy tests start from an old-style client with unlimited rates/multiplier 1. Renaming/credential rotation preserves version 1; twelve concurrent preparations of a rate/multiplier change all return version 2. The real engine bills 10 bytes at multiplier 1 plus 20 bytes at multiplier 2 as exactly 50 bytes. Disable closes the existing session, and a subsequent quota increase preserves manual disable. A rejected two-client batch rolls back all desired-version changes. Removing the create-only ORM protections reproduces an ordinary account save rewinding the version to zero; the protections were restored.

The child-process bootstrap test now includes a policy-edit subtest: after the prior 8492 billed bytes, a 65,536-byte echo at multiplier 2 contributes 262,144 billed bytes. Upload at 1 B/s holds the next 1024-byte echo; changing the existing client's rate to unlimited and multiplier to 0.5 releases it within two seconds and contributes exactly 1024 billed bytes. Final totals are 68,708 raw upload / 68,808 raw download / 271,660 billed. Disable closes that actual TCP connection; increasing quota leaves it disabled at version 5.

Direct DB→Runtime→core tests passed under race on SQLite and PostgreSQL (4.218 s and 11.525 s). Replacing the test's manual Runtime call with the ordinary client-edit service first failed because the core retained its old quota. The service hook repaired it; the SQLite race run passed in 4.361 s. The updated full PostgreSQL/service regression is recorded at the commit gate below. SQLite→PostgreSQL migration of policy fields/version and the old-schema variant passed under race in 7.166 s, together with the PostgreSQL identity migration.

```sh
XRAY_E2E_BINARY="$PWD/build/custom-xray" go test -race -p=1 ./internal/web/service \
  -run 'TestClientPolicy(Options|Omission|Desired|RuntimeBootstrap)' -count=1
# Repeat with the isolated PostgreSQL environment for that database's evidence.
```

This proves the normal edit-service path for a client already activated in a managed process. Automatic initial activation, UI, all bulk and lifecycle entry points, period resets, first-use expiry, global budgets and backup rollback fencing are not established by this test.

Additional regressions reproduced an omitted policy overwriting a concurrently committed edit and stale inbound settings overwriting the canonical policy. Policy omission now reads the locked authoritative row inside the serialized inbound transaction; detached-client edits update policy columns only when supplied. The SQLite race regression passed in 3.831 s before the final combined checks. The live edit/disable deadlines include time spent in the ordinary edit service. Restarting from the acknowledged configuration preserves disabled version 5 and 271,660 billed bytes; removing the snapshot update makes restart fail with a stale-policy error.

Final commit gate: the combined adapter/Runtime/SQLite race checks passed (2.412 s / 1.290 s / 5.700 s), and the PostgreSQL settings/concurrency/real-child suite passed in 27.059 s. The full serial panel suite passed after the omission repair (service package 55.001 s). `make lint-go` with golangci-lint v2.14.0 reported zero issues. Frontend code generation, typecheck, lint and production build passed. The full frontend suite (`npm test -- --maxWorkers=1`) passed 174 files / 1743 tests in 427.86 s, including headless Chromium.

## Scheduled collection and idle checkpoints — 2026-09-29

`TestCheckpointCommitsOnlyChangedUsageAndReservations` first failed because two unchanged clients generated new ledger sequences on every checkpoint. The repair avoids idle writes while retaining exact commits after a fully consumed 65,536-byte reservation, a multiplier change and a fractional remainder. Recovery preserves 65,536 raw upload / 6 raw download / 65,541 billed bytes plus a 500,000-millionth remainder, with zero crash uncertainty. Core policy/API race checks passed (4.850 s / 1.038 s); focused vet passed. The full core regression is recorded below once complete.

`TestClientPolicyPollingRetriesCommittedTraffic` calls the production traffic collector against an actual child process. Its first version failed because polling never attempted ledger settlement. A database failure injected while saving the source cursor now rolls back the entire page. Retrying preserves the historical 100/200-byte seed and accounts for a 1024-byte echo at multiplier 2 as 1124 upload / 1224 download / 4396 billed bytes. A fresh service instance polling again does not duplicate usage.

The expanded test uses 1001 clients to require multiple pages. Removing pagination made the active client's bill remain at the original 300 bytes; restoring it passed under race in 16.035 s. A separate cursor regression then reproduced checkpointing concealing a database cursor one sequence ahead of the core. The cursor must be checked before checkpointing. This is collection evidence for an already managed process; automatic activation and legacy statistics/lifecycle cutover are still pending.

A subsequent run alongside core regression/lint failed during startup: a ten-second negotiation context also capped all 1001-client restoration despite a longer caller deadline. An independent slow-preparation test reproduced cancellation at 10.07 s with a twenty-second caller deadline. Activation now honors the caller deadline while negotiation remains separately bounded. Neither that failed run nor the intentional RED/mutation runs count as passing evidence.

Final targeted race checks passed: process/adapter 13.488 s, Runtime 1.291 s, SQLite collection 17.417 s, and PostgreSQL collection 46.545 s (the 1001-client test itself 44.68 s). The cursor regression also verifies successful retry after restoring the valid cursor, yielding 2148 upload / 2248 download / 8492 billed bytes after two echoes. Final panel lint reported zero issues. The full managed-core `go test -p=1 -shuffle=on -count=1 ./...` passed, including scenarios in 368.028 s. The full panel `GOFLAGS=-p=1 GOTOOLCHAIN=go1.27.1 make test-go` also passed. CI now explicitly requires the real polling test to run in both databases; CI execution itself is not claimed as local evidence.

```sh
XRAY_E2E_BINARY="$PWD/build/custom-xray" XRAY_UPSTREAM_E2E_BINARY=/path/to/pinned/upstream-xray \
  go test -race -p=1 ./internal/xray ./internal/web/runtime ./internal/web/service \
  -run '^TestManagedProcess|^TestLocalRuntimeUsesPrivateControl|^TestClientPolicyPolling' -count=1
# Repeat TestClientPolicyPolling with the isolated PostgreSQL environment.
```

## Listener removal and reassignment — 2026-09-29

`TestTunnelRemovalDrainsOldTCPAndUDPSessionsBeforeReassignment` first failed because removing an inbound left its established TCP stream alive. The repair also drains UDP sessions: with only UDP cleanup removed, the regression fails with two remaining sessions instead of the single sibling listener. Restoring cleanup preserves that sibling and the previous owner's 312 upload / 312 download / 624 billed bytes; the replacement on the same port independently records 77 upload / 77 download / 154 billed bytes for the new owner.

`TestLegacyTunnelRemovalClosesTCPAndUnixConnections` reproduced retained connections in both unmanaged transports before the repair. The combined real-socket removal tests passed twenty consecutive race runs (1.997 s). Focused dispatcher/policy race checks passed (1.027 s / 3.654 s), and focused vet passed. Runtime's existing private-control test now also asserts that deletion closes its established connection, not just the listening port. The complete core and Runtime regression results are recorded at the commit gate below.

```sh
(cd core/xray && go test -race -p=1 ./app/proxyman/inbound ./app/dispatcher ./testing/policy -count=1)
go test -race -p=1 ./internal/web/runtime -run '^TestLocalRuntimeUsesPrivateControl' -count=1
```

These are core/Runtime removal checks, not evidence of completed panel ownership assignment or final legacy traffic handoff.

Final removal gate: the full core suite passed, including protocol scenarios in 368.015 s. Private-control/real-child race checks passed for process (13.567 s), Runtime (1.297 s) and service (18.231 s), using a binary built from the removal patch; they include restart recovery and the 1001-client polling case. The complete serial panel suite passed (service 62.303 s), and panel lint reported zero issues. No frontend or database schema changed in this increment.

## Authenticated accounts sharing Tunnel policy — 2026-09-29

The real VLESS/VMess/Trojan/classic Shadowsocks tests first failed because only the Tunnel's 17 upload / 17 download / 51 billed bytes reached the engine. Preserving each authenticated account's configured `clientId` made all four TCP cases pass, then all eight plain/Mux cases. Adding UDP exposed a race between the shared UDP dispatcher's timer termination and its next packet reading `closed`; this flag is now atomic. The TCP/UDP/Mux cases passed three consecutive race runs (18.964 s) after that repair.

Each expanded case first records 318 upload / 318 download / 954 billed bytes at multiplier 1.5 across two authenticated TCP streams, UDP and an owned Tunnel. Setting upload to 1 B/s and consuming the 65,536-byte burst through an authenticated stream makes the Tunnel wait. Changing the shared rate to unlimited and multiplier to 0.5 releases its queued 13-byte echo within two seconds. Historical billing is preserved: final counters are 65,867 upload / 65,867 download / 197,575 billed bytes. Disable terminates existing TCP streams and prevents the UDP session delivering another payload. This expanded race run passed in 8.737 s.

The Handler API tests independently reproduced discarded identity fields, mutations accepted by a service without policy support, and managed accounts silently accepted by a core lacking the relevant protocol capability. Rejections now precede handler mutations, and legacy requests without managed identity still work. Config tests preserve email/level and the VLESS/VMess legacy identity spelling while rejecting conflicting aliases. They also verify that unsupported managed Shadowsocks 2022 is rejected without rejecting the unchanged legacy configuration.

Runtime's private-control test now adds a real VLESS account, transfers 32 bytes each way, rotates credential/email, rejects the old credential and transfers another 17 bytes each way under the same client ID. Its existing usage increases by exactly 98 billed bytes while policy version remains 1; disable ends the new connection. Runtime/adapter race checks passed in 1.310 s / 1.195 s. The peer uses the core's existing wire encoder; this is real protocol traffic, not an independent-client interoperability claim.

```sh
(cd core/xray && go test -race -p=1 ./app/clientpolicy/... ./app/dispatcher ./infra/conf ./transport/internet/udp ./testing/policy -count=1)
go test -race -p=1 ./internal/xray ./internal/web/runtime \
  -run '^TestManagedMutation|^TestClientPolicyAdapter|^TestLocalRuntimeUsesPrivateControl' -count=1
```

The final scoped core race run passed (policy 4.911 s, API 1.039 s, dispatcher 1.028 s, config 1.238 s, UDP 2.027 s, real policy traffic 11.148 s). The final adapter/Runtime race checks passed in 1.208 s / 1.342 s. Focused core vet and panel lint passed with zero lint issues. The complete serial, shuffled core suite passed, including scenarios in 353.533 s. The complete panel suite passed with the current custom binary and unmodified upstream binary enabled (service 67.008 s, Xray 14.660 s). No frontend or SQL schema changed in this increment.

Ordinary panel identity compilation/activation, Vision, remaining account adapters and complete account lifecycle are not established by these tests.

## Tunnel ownership through existing client services — 2026-09-29

The original service tests failed in three ways: attaching a credential-free Tunnel owner returned `empty client ID`; full sync, delta and add accepted a second owner; two concurrent attachment requests both succeeded. The checks now lock the Tunnel row and validate the resulting client_inbounds membership in the same transaction as the mutation. Disabled clients still own their listeners. A failed request preserves the previous settings, client record and accounting rows.

A further failing test reproduced the same credential assumption during owner rename. Rename now retains the record ID and stable client ID across two listeners. Detaching one listener preserves its sibling. The create/replacement regression also fails when the Tunnel credential exemption is removed, and verifies explicit reassignment retains the old account while assigning a different stable identity.

The restored SQLite race checks passed in 3.770 s; real PostgreSQL checks passed in 11.290 s, with separate schemas per test. Panel lint passed with zero issues. The complete shuffled, serial panel regression passed with the custom and unmodified core binaries enabled (service 58.594 s, Xray 14.647 s). These tests establish service/database ownership; they do not establish live policy activation, migration of unowned legacy listeners or the final UI flow.

## Managed candidates from stored client ownership — 2026-09-29

The real-child compiler test creates two Tunnel rules through existing services, generates the managed configuration, starts the core through Runtime and settles normal traffic polling. TCP sends 55 plus 17 bytes and UDP sends 13 bytes in each direction. Starting from 100 upload / 200 download / 300 billed historical bytes, the final ledger is exactly 185 upload / 285 download / 640 billed bytes at multiplier 2. The test passes with SQLite and PostgreSQL.

Regressions first reproduced lost trusted identity, silently discarded business listeners using the API tag, and removed credentials for restricted accounts. Restoring the fixes passes: only an explicitly configured legacy Tunnel control listener is replaced, stored identities override raw settings, and disabled/depleted accounts retain credentials behind core enforcement. Invalid owners/adapters/remote budget sharing fail before desired policy versions are written. Saved inbound settings and the caller's state options are preserved.

The 1001-client regression compiles two candidates and changes the final client's multiplier on the second pass. Every identity is emitted, only that policy advances to version 2, and truncating preparation to the first batch makes the test fail with 1000 policies. SQLite race passed in 5.078 s (two compilations 2.130 s); PostgreSQL race passed in 7.790 s (two compilations 3.708 s). These are candidate compilation measurements, not 100000-client process activation or maximum gRPC message evidence.

Final compiler plus existing configuration race checks passed in 6.458 s. The PostgreSQL compiler/real-child checks passed in 31.713 s, followed by the additional batch test above. Lint reported zero issues. The complete shuffled, serial panel Go suite passed with actual custom and unmodified core binaries enabled (service 58.520 s, Xray 14.688 s). The existing opt-in legacy scale test was skipped and is not counted as validated. No core, frontend or database schema changed in this increment.

CI includes the candidate tests in both database jobs and checks explicit PASS records for the real-child and 1001-client cases. This is a separately callable candidate path; automatic production startup, legacy accounting/reset/expiry cutover and live account/listener reconciliation remain open.

## Exact quota-window baselines — 2026-09-29

The core tests first failed because reset still compared against lifetime usage, the quota restriction remained set, and a future baseline was accepted. The implementation now subtracts an exact billed baseline, including the millionth-byte fraction. Three upload bytes at multiplier 0.5 create 1.5 billed bytes; resetting the baseline to 1.5 allows exactly two further raw download bytes under a one-byte quota. After restart the lifetime ledger remains 3 upload / 2 download / 2.5 billed, and the new window stays exhausted. A retry of the same version does not grant another window.

Separate regressions verify disable/expiry/revocation remain effective, a malformed/future baseline rejects the whole batch, and a valid near-uint64 lifetime value does not overflow by adding quota to an absolute ceiling. Before-commit failure retains the old confirmed seed and nine frozen uncertain bytes; after-commit failure restores the new baseline with exact counters and no unused reservation. An explicit subsequent reset can credit the old window's uncertainty without deleting it from lifetime history.

Two compatibility mutations fail as intended: emitting a new zero-valued JSON field breaks a legacy initialization digest, and removing recovery validation accepts a future baseline in the stored record. Both protections were restored before final checks.

The real Tunnel test starts from 201 historical upload bytes / 100.5 billed, loads a baseline through JSON/core startup, exchanges one byte each way, resets through private RPC, and exchanges another byte each way. The final lifetime counters are 203 upload / 2 download / 102.5 billed with a 101.5 baseline. It first failed in configuration conversion and then in RPC conversion before both were fixed. The panel child-process test separately caught a startup conversion that dropped the baseline; it now retains the baseline/fraction while forwarding 256 bytes each way.

Scoped race checks passed: engine 5.180 s, command API 1.044 s, dispatcher 1.030 s, configuration 1.251 s and real policy sockets 11.280 s. Panel adapter/child-process quota-window checks passed in 1.271 s. Nonzero baselines require the explicit `quota-window-baseline-v1` capability. Full panel reset/renew/scheduler flows and period statistics are not yet implemented.

The first complete core regression encountered the unchanged `TestQUICNameServerWithIPv6Override` failing at its two-second external DNS-over-QUIC query deadline. Isolated `-count=3` runs reproduced a successful query followed by the timeout in both the modified tree and the pinned, unmodified upstream module. No DNS test was skipped, timeout relaxed or source changed; this external regression limitation is separate from the passing local quota evidence.

The complete run also exposed an existing Tunnel scenario fixture that chose one free port but attempted to listen on six consecutive TCP ports. The log shows later ports already occupied across all startup retries. The fixture now probes the complete TCP/UDP range and reaps a failed child before retrying. Both real forwarding tests pass ten repetitions after the repair (20 cases, 3.508 s). The original isolated TCP test also passed three runs, confirming the original failure depended on concurrent port occupancy. The initial full-core run remains recorded as failed (scenario package 363.667 s); it is not counted as a full PASS.

The complete panel suite passed with the new custom and unmodified binaries enabled (service 67.344 s, Xray 14.813 s); lint reported zero issues and scoped core vet passed.

After the port fixture repair, the complete shuffled core suite passed with package parallelism 2, including DNS (56.529 s) and scenarios (340.128 s). This later successful run does not erase the external DNS timing failure recorded above. No other heavy check ran during the rerun. The six independent-process rate cases also passed their original limits: upload measured 258046 / 1047134 B/s and download 262347 / 1048466 B/s at configured 256 KiB/s / 1 MiB/s. Unlimited controls measured 17178163 / 32156957 B/s; these observations are not a controlled claim of unchanged throughput versus prior runs. See [raw observations](evidence/tunnel-rates-quota-windows.jsonl).

## Durable SQL reset requests — 2026-09-29

The reset preparation tests use real committed engine receipts. Three upload bytes at multiplier 0.5 produce 1.5 billed bytes; the first request captures that boundary. Four later download bytes increase lifetime billing to 3.5. A second request captures 3.5, and a delayed retry of the first request retains that newer window without granting further credit. Sixteen concurrent identical requests create one reset record and policy version while preserving manual disable and expiry. Frozen uncertainty is credited explicitly; outstanding reservations are excluded.

Insert-time failure rolls back both desired version and reset record. A real PostgreSQL deferred-constraint failure verifies the same behavior at commit time and returns no policy that could be applied to the core. Separate regressions reject unsettled, revoked, shared remote, multiple-source and inconsistent-version resets. The migration test preserves every boundary field from SQLite to PostgreSQL, accepts older schemas without reset history, and rejects a reset table without a complete ledger.

The initial service tests failed before implementation. A delayed-retry mutation failed by returning the old boundary with an extra version; a migration-copy omission failed with a missing reset record. Both were restored before final checks. SQLite reset race checks passed in 4.481 s. Final PostgreSQL migration/desired/reset race checks passed in 4.938 s / 20.726 s. The complete panel suite passed with custom and unmodified core binaries enabled (service 58.748 s, Xray 14.726 s), and lint reported zero issues.

```sh
go test -race ./internal/web/service -run '^TestClientPolicy(Reset|Desired)' -count=1
# With the isolated PostgreSQL environment:
go test -race ./internal/database ./internal/web/service \
  -run '^TestClientPolicy(CrossDatabaseMigration|Reset|Desired)' -count=1
```

This increment establishes SQL preparation and migration. Runtime application/retry, public reset/bulk/scheduled flows, period statistics and automatic activation remain open. No core, frontend or public HTTP schema changed, so the preceding core and frontend evidence was not rerun for this SQL-only increment.

## Runtime reset and automatic retry — 2026-09-29

The real-child reset test first reproduced a committed reset whose core remained at policy version 1 after normal polling. The repair checkpoints and settles before capture, commits the request before Runtime application, and retries pending resets from ordinary traffic polling. Losing the private control socket after SQL commit leaves a 32-byte boundary pending. Another 16 billed bytes arrive before recovery; polling applies version 2 with the original 32-byte boundary and 48 lifetime billed bytes.

Further traffic and delayed requests preserve the latest window: request B captures 80 billed bytes at version 3; a later request A retry leaves B's boundary and all 92 billed bytes intact. A third reset carries manual disable and expiry into version 4, closes the actual TCP connection, and retains 23 upload / 23 download / 92 billed bytes across a child restart. Attempting a reset while stopped creates no request. An injected SQL insert failure returns its database error without an applicable reset; a concurrent Runtime ledger read inside the transaction verifies the RPC mutex was already released.

Both explicit SQL retries and polling originally accepted a new remote attachment made after request creation. Failing regressions now verify rejection until that attachment is removed. Another regression reproduced an unrelated revoked client blocking a pending reset; only clients with reset records are inspected, and completed resets do not block another client's retry when their owner is later revoked.

The 1001-client polling test additionally prepares a reset for the last configured client. Its next normal poll applies version 2 and the stored disable. Omitting the pre-reset checkpoint makes the real reset test fail on an unsettled receipt. Truncating reconciliation to the first 1000 clients leaves the last client at version 1 and fails the batch regression. Both mutations were restored before final checks.

Final SQLite reset/desired/polling race checks passed in 20.456 s. PostgreSQL reset/desired/polling checks passed in 42.153 s before the scan-scope repair; final PostgreSQL reset/desired checks, including that repair and the real-child lifecycle, passed in 27.652 s. CI requires the real reset test's explicit PASS record in both database jobs. Full panel/lint results are recorded at the commit gate below. This increment changes service orchestration; public reset requests, period statistics and automatic managed activation remain open.

The final complete panel suite passed with the current custom and unmodified core binaries enabled (service 60.446 s, Xray 14.729 s). Panel lint reported zero issues. Workflow YAML parsing and all shell-block syntax checks passed. No core, frontend or SQL schema changed in this runtime increment.

## Exact acknowledged accounting views — 2026-09-29

Read-service tests cover single-client statistics, client lists and pagination, inbound slim/detail responses, and full/active snapshots. Pending reset requests retain the previous window until the committed receipt acknowledges the corresponding policy version. Fraction borrowing, frozen uncertainty, duplicate receipt replay and near-int64 JSON precision are checked with literal amounts: lifetime billed `9223372036854775799.999999`, period billed `9223372036854775798.499999`, and remaining `8.500001`. The initial large-number fixture had an off-by-one expected subtraction; an independent decimal calculation corrected that expectation before acceptance.

An unattached client rename exposed traffic left under the previous email. A failing SQL-update injection now verifies that record and traffic rename roll back together. A successful retry preserves the stable ledger; a replacement using the old email receives no prior accounting. Inconsistent totals, multiple receipt sources, remote attachments without coordinated budgets and future reset versions return ledger errors. A 1001-client snapshot checks every stable identity and fraction. These batch timings are diagnostic, not production throughput claims.

The real Runtime reset test additionally reads the existing statistics service after control loss and acknowledgement: pending period/lifetime 32/32, then 16/48 after application, 12/92 after the next window, and 0/92 after restart. SQLite accounting/reset races passed in 5.804 s; PostgreSQL passed in 49.061 s while other checks shared the two-CPU host.

Frontend regressions first failed on raw-quota rendering and missing accounting-only websocket updates. The real client cell and inbound modal now use billed periods; the popover retains lifetime values after reset. TanStack notifications are asynchronous, so the hook test waits for the actual state change. Removing its accounting merge still fails that assertion; the implementation was restored. Eighteen focused tests pass. The initial complete frontend run had 12 five-second timeouts (eight Happ editor cases and four inbound modal cases) while Go checks competed for this two-CPU host, plus teardown errors. With one worker the assertions passed, but asynchronous React callbacks still accessed `window` after jsdom teardown. Direct console output isolated the additional pending-log RPC error but did not solve those React callbacks. The shared cleanup now awaits React `act` instead of guessing three timer ticks. The exact affected files plus accounting UI passed 38 tests with normal diagnostics. Final complete frontend verification passed all 175 files / 1754 tests in 439.37 s, with one worker, normal console interception and unchanged deadlines. Typecheck, lint, production Vite build and Storybook build also passed. No tests were skipped or diagnostics suppressed to obtain this result.

A final modal regression also preserves the configured quota for legacy clients whose statistics lag. A separate SQL scope regression rejects a local accounting view after a new remote attachment, rather than presenting local usage as a global total. Removing accounting consistency guards makes all four corruption regressions fail; removing the global/node traffic rename makes the identity regression fail. All mutations were restored before final checks.

Final backend gate for the accounting view: SQLite race checks passed in 5.993 s and PostgreSQL race checks in 21.409 s, including the remote-scope regression and global/node rename preservation. The 1001-client snapshots took 280 ms / 432 ms in those runs. Go lint reported zero issues. The complete shuffled panel suite passed with custom and unmodified core binaries enabled (service 60.422 s, Xray 14.745 s). Workflow YAML and all 11 shell blocks passed syntax checks; frontend and documentation OpenAPI files match. No core source changed in this increment, so the previously recorded full core suite was not repeated.

## Manual single-client reset entrypoints — 2026-09-29

The first regression reproduced legacy zeroing from all three public service entrypoints when the core was stopped, both after acknowledgement and with only a prepared bootstrap seed. A second regression queued initialization before the reset's SQL write and reproduced loss of the captured seed. Those writes now recheck under the client row lock. An identity-guard mutation also reproduced a queued reset switching to a new client that reused the email; restoring the guard preserves both identities' counters. The fixture explicitly disables ClientRecord after insertion because its GORM default otherwise enables it.

The real-core reset lifecycle now calls the public client service, including explicit identity-bound request IDs. Removing its SQL receipts and total originally caused a successful legacy reset; checking the current managed process configuration now rejects that inconsistent state without zeroing counters or enabling the client. It covers checkpoint-before-capture, SQL/control failures, delayed duplicate requests, the latest period boundary, manual disable/expiry, actual stream closure and restart. HTTP regressions reject malformed JSON, invalid keys and stale/missing identities before writes; an empty body retains legacy compatibility. Browser regressions reuse a key after `success:false`, transport failure, remount and rename, release it after success, and isolate a replacement identity at the old email.

The first full frontend run passed 176 files / 1757 assertions but failed with two teardown errors from the existing Happ tests; it is not a successful gate. Static Ant Design messages create their own React root and animation-frame timer outside Testing Library cleanup. Test cleanup now destroys these messages, completes their CSS exit animation and waits for removal. JSDOM selects the WebKit animation-end event but never emits it, so the cleanup supplies both standard and prefixed completion events. Destroy-only cleanup reproduced the stuck exit animation; removing destroy after the final repair also fails deterministically. The complete four-file regression passes 37 tests with zero teardown errors. No product behavior, assertion, timeout or error reporting was disabled.

The first PostgreSQL run passed the policy tests but exposed shared-schema collisions in old legacy reset fixtures. Those tests now use the same per-case database isolation as the policy tests; the final run keeps the original test selection. Final complete gate results follow below. Core source and dependency versions are unchanged in this increment.

Final backend checks pass after those repairs: SQLite service/controller race checks 9.039 s / 3.648 s; PostgreSQL reset and legacy compatibility race checks 48.651 s; Go lint reports zero issues. The complete shuffled panel suite passes with the actual custom and unmodified core binaries enabled (service 60.962 s, Xray 14.743 s). Production frontend build, Storybook build, API generation, docs typecheck/lint, and workflow YAML/all 11 shell-block syntax checks pass. Final frontend typecheck and lint pass; the full serial suite after static-message cleanup passes all 176 files / 1757 tests in 440.76 s, with normal console interception and zero teardown errors.

## Batched manual resets — 2026-09-29

New SQL tests verify whole-batch rollback and a delayed batch request preserving an independently newer single-client window. The real 1001-client polling fixture now also executes batched resets: an unknown member is rejected before checkpointing; failure on reset insert 1001 leaves no partial batch or Runtime application; a failure on Runtime batch two leaves all SQL boundaries durable, and normal polling recovers the pending versions. Every client's quota and disabled state is read back from the actual core. Each explicit operation checkpoints once.

The public mixed-batch regression initially exposed a GORM builder retaining the first chunk's `IN` condition under row locking, returning 999 managed members instead of 1001. A cloned session per query scope repairs pagination. It then exposed legacy enable work being lost after another member's Runtime failure. Fault injection at the SQL completion boundary and an actual Runtime interleave also reproduced a crash gap and an old enable overwriting a newer disable/quota edit. Legacy client/inbound enable and node dirty flags now commit with the counters; post-commit Runtime work reads current desired state and never rewrites SQL from the old reset payload. A duplicate request preserves subsequent legacy usage and manual disable. A stopped-core request captures membership, then recovery after rename and email reuse resets only the original stable identity.

Service/HTTP regressions cover reset-all membership freeze, original affected counts, inbound selection from authoritative links, unchanged replay timestamps, and stopped/prepared clients. Both HTTP endpoints initially cleared newly accrued traffic on retry before request-key wiring. The browser test initially sent no all-client key; another regression reproduced two panels at different base paths sharing that key. All-client retry storage now follows the actual HTTP base path. Focused browser tests pass with normal diagnostics.

Cross-database migration preserves immutable batch membership and completion, supports older schemas without this table, and rejects incomplete reset-history schemas. Omitting batch copy makes the new round-trip assertion fail; the restored migration race check passes (database 5.142 s). Replacing inbound membership with the stale traffic-row inbound ID also fails its regression. A real HTTP node receives current inbound fields and the exact quota 9007199254740993; removing post-commit Runtime dispatch makes that check fail. Source is restored before final checks. No core source or dependency changed in this increment.

Final SQLite and PostgreSQL reset race checks pass after the atomic-enable repair (service 39.573 s / 107.856 s; PostgreSQL real 1001-client lifecycle 34.50 s). Go lint reports zero issues. Frontend typecheck, lint, format check, MSW-worker check, production build and Storybook build pass; the full serial frontend suite passes 176 files / 1759 tests in 444.57 s with zero teardown errors. API generation, docs typecheck/lint and workflow YAML/all 11 shell-block syntax checks pass. Frontend and documentation OpenAPI files match. The complete shuffled, serial panel suite passes with actual custom/unmodified core binaries enabled (service 64.741 s, Xray 14.748 s). The staged generated files pass `make gen-check`, and `go build ./...` passes against the current production frontend bundle.


## Scheduled reset integration — 2026-09-29

The existing job first reproduced a second run clearing new usage and including a client created after the first run. It now uses one captured calendar operation across inbound and client cycles. Existing node gate tests still require concurrent HTTP reset calls; all seven original monthly day cases now run through the real service/database path instead of an isolated date helper. Calendar integration cases cover repeated/long/short daylight-saving periods, Sunday boundaries and Kathmandu's fractional UTC offset. Replacing hour boundaries with UTC truncation fails the fractional-zone case; removing month-end clamping fails the short/leap-month cases.

Separate regressions reproduced a delayed older period clearing a newer allowance, a pending task overriding a later manual reset, and a pending legacy cycle re-enabling a later operator disable. All now pass in scoped SQLite runs. The actual 1001-client core fixture queues two periods during core unavailability, recovers only the newer window, preserves manual disable, and settles without granting the old window. It also places nine unconfigured-client operations ahead of valid ones: persisted attempt order allows the valid work to progress on the second bounded poll, while the failed intents remain pending and ordinary traffic collection succeeds.

Cross-database tests preserve calendar scope/time, original inbound IDs, target flags, completion and per-client ordering. Skipping only the reset-time copy produces a missing-row failure. The older-column case also exposed an empty JSON value produced by map-based migration; older manual batches now receive an empty inbound list while retaining their original membership. Older schemas without the reset-time table remain supported. Final reset races pass on SQLite (service 46.040 s, job 4.482 s) and PostgreSQL (service 139.459 s); the real 1001-client lifecycle passes in 30.06 s / 37.51 s. The migration race check passes in 6.964 s. Go lint reports zero issues, and the complete shuffled panel suite passes with actual custom/unmodified core binaries (service 69.806 s). Workflow YAML and all 11 shell blocks pass syntax checks. No frontend or core source changed in this increment.

The final scheduled-reset tree also passes `go build ./...`, `make gen-check`, and `git diff --check`.

## Managed renewal integration — 2026-09-29

Four new regressions first reproduced legacy maintenance disabling managed clients from raw quota/expiry, clearing renewal usage and operator disable, and converting first-use expiry outside the managed ledger. Excluding prepared/active identities fixes those mutations while preserving raw statistics accumulation. The initial compatibility run also passes the existing automatic-renewal tests.

A real private-core polling fixture starts with four identities: renewable, manually disabled, catch-up truncated by a prepaid cap, and an already exhausted allowance. Its initial failure showed no managed renewal. Normal polling now preserves lifetime 333 bytes and opens only the two current windows; the other periods retain 333 bytes. It checks the actual core restrictions and versions, SQL counts, exact quota 9007199254740993 and attached JSON. SQL failure leaves all expiry/count/version changes uncommitted. Control loss first exposed the expiry-only version missing from reset-specific retries; general desired-version reconciliation repairs that case. All three modes and the existing 1001-client polling/reset recovery pass the focused race run (service 36.379 s). Full database/backend gates are recorded after completion.

The first expanded PostgreSQL run exposed collisions in legacy automatic-renewal fixtures that reused the package schema; the shared fresh-database fixture now gives each case its own schema. The same test selection is retained. A mixed-inbound regression then reproduced all three legacy lifecycle paths rounding a managed sibling's quota 9007199254740993 through float64 JSON decoding. Those edits now preserve integer tokens; the regression also requires the legacy client's renewal/activation/disable action to occur, so simply skipping maintenance cannot pass.

Final renewal checks pass: expanded SQLite races 60.960 s, PostgreSQL races 229.563 s, and the final mixed-inbound/legacy lifecycle PostgreSQL race run 70.051 s. The latter covers the subsequent integer-decoding repair; its SQLite compatibility run passes in 6.917 s. Go lint reports zero issues. The complete shuffled panel suite with actual custom/unmodified core binaries passes (230.69 s wall time), followed by `go build ./...` and `make gen-check`. Workflow YAML/all 11 shell blocks and `git diff --check` pass. This increment changes no frontend or core implementation.

## Durable first-use expiry — 2026-09-29

Negative expiry remains a versioned duration until a first nonzero admitted
payload persists its timestamp with the durable reservation. Idle/zero-byte
admissions do not start it. Core tests cover autonomous disconnect, unrelated
policy edits, graceful restart and an abruptly exited child. Omitting the
timestamp specifically from reservation saves makes the abrupt-exit test fail
with an unexpired client and 65,536 uncertain bytes; the implementation was
restored before the passing runs.

The actual Tunnel/private API test first failed because the ledger lacked its
first-use field. The adapter regression first reached an unimplemented Apply RPC
instead of rejecting the absent capability. Both now pass. SQL tests first
failed when the compiler rejected a negative duration; settlement now atomically
updates the receipt, client, legacy traffic row, attached JSON and desired
version, retaining manual disable and the exact quota 9007199254740993. Rollback,
replay and a newer operator edit are exercised. Removing receipt timestamp
guards makes the invalid-page tests fail; clearing the timestamp during database
copy makes the migration's exact receipt comparison fail. Both mutations were
restored. An old receipt schema without the column migrates with a zero default.

The real panel-generated Tunnel fixture uses StartManagedProcess and ordinary
GetXrayTraffic polling: four bytes each direction yield eight billed bytes, the
negative duration becomes an acknowledged absolute expiry, and a child restart
retains the original deadline and usage. This is not the ordinary RestartXray
activation path. The initial runtime fixture omitted its required subscription
identity and failed during attachment; that fixture was corrected before the
passing run. A restricted-sandbox socket failure was rerun with local socket
access; that failed invocation is not counted as a pass.

Final scoped races passed with the newly built custom binary: SQLite service
44.870 s and adapter 1.182 s; PostgreSQL migration 8.597 s and service 94.392 s.
Both database logs explicitly contain PASS for settlement and the real child
restart case. The PostgreSQL commit-failure case ran in its PostgreSQL job; its
SQLite skip is not PostgreSQL evidence. Core policy/control/dispatcher/config/
real-traffic race checks also passed. CI now requires both new panel tests' PASS
records in both database jobs. Full regression and build results follow below.

The initial complete core run failed in the unchanged
`TestQUICNameServerWithIPv6Override`: its two-second public AdGuard QUIC request
returned `record not found`. No DNS source, timeout or assertion was changed.
Three isolated repetitions then passed (0.15 s, 0.43 s, 0.12 s), consistent with
an intermittent external-query failure rather than evidence of a repaired DNS
bug. The initial full run remains failed; the final full rerun is recorded
separately below.

The final complete core rerun passed without DNS changes (607.67 s wall time;
DNS package 57.994 s, scenarios 335.253 s). The original failed run remains in
the evidence log. No frontend source changed in this increment.

Final panel lint reports zero issues. The complete shuffled panel suite with
both real custom/upstream binaries passes (235.74 s wall time; service 71.562 s,
Xray 14.710 s), followed by `go build ./...` and `make gen-check`. Workflow YAML,
all 11 shell blocks and whitespace checks pass. This final panel run also
includes the separately committed exhausted-renewal selection regression.
