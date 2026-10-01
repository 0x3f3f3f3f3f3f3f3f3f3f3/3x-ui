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

## Exhausted renewal selection — 2026-09-29

The normal real-core renewal test now removes its control socket after all
remaining expired clients have exhausted their allowed renewals. It first
failed because polling still tried to checkpoint those clients. Due selection
now joins the canonical allowance with the traffic row's used count before any
RPC; a missing traffic row remains eligible for the existing validation error.
All normal, SQL-failure and control-loss renewal cases pass under race on SQLite
(4.459 s) and PostgreSQL (8.412 s). The complete panel/lint/build/generation gate
above includes this fix. No quota, renewal count or lifetime counter is reset
by the selection change.

## Closing sessions for one inbound — 2026-09-29

The control API accepts an optional `ClientRequest.inbound_tag` (protobuf field
3) for `CloseConnections`. A supplied tag limits the operation to that client
on that inbound; omission preserves the existing client-wide behavior. The
core advertises `inbound-scoped-session-close-v1`. The panel checks this
capability before sending a scoped request, because an older protobuf server
would otherwise ignore the added field and close every connection.

`TestCloseConnectionsRestrictsItsInboundScope` first closed two sessions instead
of one. It now verifies admission stops for the selected session, admission
continues on the sibling inbound, repeated/unknown scopes close zero sessions,
client-wide close still works, and the admitted 11 bytes remain billed once.
Core engine/control race checks pass (5.901 s / 1.042 s). Removing the panel
capability guard with a temporary Go overlay makes the adapter regression fail
with an unexpected RPC, proving the guard is exercised.

During the still-uncommitted activation work, a real VLESS + Tunnel test also
reproduced a same-client sibling flow being disconnected by partial detach.
With the scoped API and Runtime consumer, the sibling stream survives and
lifetime totals remain 112 upload / 212 download / 348 billed bytes after
three 4-byte bidirectional exchanges on top of the 100/200 historical seed.
This is evidence for the API consumer under development, not a claim that
ordinary managed startup, legacy migration or all lifecycle paths are complete.

The nine-file scoped-close increment was also checked independently in a
detached worktree at `f3465945`, without the activation changes. The adapter
race checks, Go lint (zero issues), full shuffled panel suite (218.12 s wall),
build (7.77 s) and full shuffled core suite (643.14 s) pass. Initial full runs
failed in unchanged tests: the public AdGuard QUIC DNS query timed out, and an
AmneziaWG test found its fixed TCP port 58912 occupied. An isolated DNS repeat
passed once and then timed out. The final panel and core suites ran sequentially;
no test assertion, timeout or source was changed to obtain the passing runs.

## Revoking authenticated credentials — 2026-09-29

Managed sessions now remain tied to the actual authenticated `MemoryUser`.
VLESS, VMess, Trojan and Shadowsocks validator removal permanently revokes that
object, closes its registered sessions and rejects requests that finish
authentication or enter dispatch later. A replacement credential gets its own
object; sibling credentials keep the same client ledger and remain usable.
The private API advertises `authenticated-credential-revocation-v1`.

Twelve validator/dispatcher cases first admitted removed credentials or stale
payload. A real VLESS header decoder also reproduced removal between UUID
validation and the remaining header reads. The dispatcher now fences session
registration against revocation; a concurrent registration/removal case checks
the resulting admitted bytes and absence of leaked sessions. The focused race
checks pass. An overlay that removes dispatcher registration fails again.

The existing four-protocol TCP/UDP scenarios now remove credentials through the
handler API operation in both plain and mux modes. All eight cases initially
retained old streams under the omission overlay, and pass with the fix. The
sibling Tunnel still forwards and the final ledger remains exactly 65875 bytes
in each direction / 197583 billed bytes after the rate and multiplier changes.

The first complete core run exposed VLESS/VMess comparison tests traversing the
new private runtime state. Their roundtrip checks now require the decoder to
return the same authenticated user object, while still comparing the remaining
header fields. Both codec race suites pass; copying the authenticated user under
an overlay makes all four serialization/mux checks fail. No runtime field is
serialized into the protocol or accepted from a client.

This increment was verified independently at `0496f115`, with thirteen core
source/test files and without the panel activation work. Core race checks,
the core build, Go lint (zero issues), the complete shuffled panel suite
(239.65 s wall) and panel build pass. The final complete shuffled core rerun
also passes, including the repaired codecs and scenarios (343.209 s). The
initial comparison-test failures remain recorded separately.

## Coherent managed configuration reads — 2026-09-29

Managed compilation reads settings, listeners, normalized credentials, fallback
targets, subscription outbounds, node egress and trusted identity bindings from
one database snapshot. PostgreSQL uses a read-only repeatable-read transaction.
SQLite pins a connection and starts a deferred read transaction, avoiding the
DSN's immediate write transaction mode. Policy preparation still locks current
client rows and rejects credentials that changed after compilation.

The concurrent owner/target regression initially produced the new owner with
the old Tunnel target. It now accepts a coherent snapshot and requires the next
compilation to observe the committed owner and target together. Omitting the
SQLite read transaction or downgrading PostgreSQL to read committed reproduces
the mixed configuration. A separate credential rotation regression checks the
typed stale-candidate error and a successful fresh compilation.

This seven-file compiler increment was verified independently at `56d0f622`,
without ordinary activation changes. SQLite compiler/builder race checks
(10.354 s), PostgreSQL compiler/desired-policy race checks (32.982 s), Go lint
(zero issues), the complete shuffled panel suite (244.61 s wall) and panel
build pass. Real-core checks use the credential-revocation build. A coherent
candidate is not a complete database-to-runtime revision fence; concurrent
mutation completion and cross-node enforcement remain separate work.

## Negotiating authenticated revocation before activation — 2026-09-29

Managed VLESS, VMess, Trojan and Shadowsocks users now require the protocol's
trusted-identity capability, `authenticated-credential-revocation-v1` and
`inbound-scoped-session-close-v1`. Both user and listener additions reject a
missing capability before changing handlers. Process startup collects the same
requirements from the compiled listeners and checks them before usage
preparation. Generic ledger reads and unmanaged handler mutations retain their
existing capability requirements.

Eight negative adapter cases initially changed handlers without one of the two
revocation capabilities. They now reject without a handler call; four matching
positive cases preserve the authenticated client identity. A real older custom
core initially entered the startup preparation callback. Startup now rejects it
before preparation and leaves no business listener. The current core reaches
preparation, and existing Tunnel seed, restart and control tests still pass.

CI builds an additional test core using a Go overlay that removes only the
revocation capability advertisement. `XRAY_PRE_REVOCATION_E2E_BINARY` selects
this negative fixture; its source is not installed or shipped. The workflow's
actual fixture build and shell syntax checks pass.

This increment was checked independently at `c1fbc8a6`, without ordinary
activation changes: adapter/process and Runtime race checks pass (31.13 s wall),
Go lint reports zero issues, the complete shuffled panel suite passes
(256.34 s wall), and the panel builds. The production core source is unchanged.

## Atomic legacy traffic settlement — 2026-09-29

The production job now reads cumulative counters without resetting them and
commits inbound, client and outbound changes with one per-child receipt.
Real VLESS echo tests inject failures in each traffic table and receipt writes,
including the first poll and a newly constructed job. Failed transactions keep
all totals unchanged; retry adds each delta once. Another wrapper commits the
real SQL transaction and then returns a simulated lost acknowledgement. Before
the fix, retry doubled a four-byte echo (client totals 108/208 instead of
104/204). A retained batch and SQL receipt now acknowledge it without adding
usage again, and subsequent traffic remains collectible in the next batch.

First-use tests inject failures at the inbound settings write, normalized client
write and expiry update. All changes roll back together. Zero-byte reports keep
idle delayed-start clients inactive, including mixed active/idle polls. Process
tests use real helper children and gRPC counters to verify concurrent polls,
shutdown waiting for settlement, fresh cursors after child replacement and
separate inbound/outbound counters sharing a tag.

The collector was tested independently of ordinary managed activation at
`8100a5a4`. SQLite race checks passed in 58.88 s wall and PostgreSQL checks in
47.60 s wall. The expanded receipt cases passed under race in 4.938 s. Review
caught a missing migration registration; the full regression also failed its
existing model-parity check. Registering the model alone then reproduced failure
to migrate an older database without this table. With both fixes, model parity,
copy checks and real SQLite→PostgreSQL→SQLite migration/legacy compatibility
passed under race in 12.093 s. CI explicitly requires the migration test's PASS
record and the real collector test in both database jobs.

Commands: `go test -race ./internal/web/job ./internal/web/service ./internal/xray
-run '^Test(XrayTrafficJobRetriesUncommittedCountersAtomically|FirstUseTrafficSettlement|ProcessTrafficSettlement)'
-count=1`; set `XRAY_E2E_BINARY` to the custom build. The PostgreSQL run sets
`XUI_DB_TYPE=postgres` and a dedicated `XUI_DB_DSN`; migration tests use their own
schema. Select `TestLegacyTrafficReceiptCrossDatabaseMigration` for receipt
round-trip and older-schema coverage. After the migration correction, lint
reported zero issues, the complete shuffled panel suite passed in 245.49 s wall,
and the panel build passed in 10.78 s.

This increment retains the existing lifecycle Runtime ordering. Moving those
calls outside the serial writer, stale-plan revalidation, ordinary activation,
legacy crash recovery and a verified final drain are separate unfinished work.
No production core or frontend source changes in this increment.


### Local reservation and remote attachment fence

A client with a prepared local policy cannot acquire a remote binding until
coordinated budgets are available. Conversely, a remote binding prevents local
policy preparation. Both operations lock the same stable client rows in sorted
chunks and refresh records after the lock; a stale pre-lock read cannot bypass
the reservation check. This is a topology fence, not a global budget allocator.

The regressions first reproduced both invalid orderings and a PostgreSQL
interleave that paused attachment after its initial read while preparation
committed. The isolated increment then passed the broader SQLite race selection
in 20.481 s package time and the PostgreSQL race selection in 609.62 s wall time,
including the original 5,000/20,000/50,000/100,000-client bulk scale cases.
The first broad PostgreSQL run exceeded its 300 s suite deadline; the unchanged
selection passed with a 1,800 s deadline. A lint-only embedded selector issue
was corrected afterward. Final lint reported zero issues, the complete shuffled
panel suite passed in 245.55 s wall time, and the build passed in 9.88 s.

CI explicitly requires `TestClientPolicyLocalReservationRejectsRemoteMembership`
in both database jobs and `TestClientPolicyLocalReservationSerializesAgainstRemoteLink`
in PostgreSQL. Existing conflicting topologies restored through a whole-database
backup still require validation; this increment does not implement global leases,
remote budget coordination or automatic managed activation.


### Ordinary managed startup, mutations and traffic writer boundaries

The normal restart entrypoint now supports an explicit managed template. Real
child-process fixtures cover VLESS creation/rotation, shared Tunnel ownership,
sibling-flow preservation, listener changes, stopped-core queuing and exact
ledger recovery. Removal precedes policy grants; RPC barriers first reproduced
an obsolete binding becoming active during a grant. Core protocol identity,
authenticated revocation and scoped-close capabilities are checked before hot
bootstrap. Missing each capability first reached preparation incorrectly; the
fixed path rejects it with zero preparation calls.

Review regressions reproduced and repaired live legacy refusal after reserving
a desired version, post-commit bind-conflict leaving old access active, reset
reentering its SQL writer, Reverse clearing after configuration application, and
BulkDelete hiding a failed application when canonical links and Settings had
drifted. A real HandlerService read verifies Reverse removal. Reverse-tagged
inbounds retain the existing full-restart rule. A further cross-protocol RED
showed an ordinary Trojan edit clearing the shared VLESS Reverse; explicit early
clearing is now limited to VLESS.

Traffic tests delay an old renewal across disable, inbound disable/delete and
credential rotation. They also block Runtime application while concurrent inbound
mutations attempt to proceed. Removing the shared lock fails all four overlap
cases. A 1001-client batch initially made 2002 reads and 1001 managed reconciles;
reads are now bounded and the batch reconciles once. WireGuard peers retain their
own inbound addresses/PSKs. Config export leaves pending lifecycle maintenance
for polling; restart performs maintenance before taking its global lock. Both
ordinary lifecycle and single-client reset callbacks run outside the SQL writer.

Independent final verification used Go 1.27.1 on the recorded ARM64 host, the
credential-revocation custom binary and unchanged upstream binary. SQLite service
races passed in 31.927 s package time; PostgreSQL in 165.057 s. Runtime/API races
passed in 1.294/13.728 s. Staticcheck required only an equivalent switch in a test;
final lint reported zero issues. The complete shuffled panel suite passed in
246.52 s wall time and the panel build in 7.88 s. Source review found no remaining
blocker within this increment.

The unchanged PostgreSQL `TestGetXrayConfigScale` race test passed all original
10,000/100,000-client single/spread50 cases in 728.111 s package time (746.18 s
wall). Mean configuration generation was 8.911/8.806 s at 10,000 clients and
86.439/87.335 s at 100,000 clients, respectively; each case builds three times.
These are instrumented regression measurements, not production throughput claims.
CI now requires explicit PASS records for 23 ordinary lifecycle/writer cases in
both database jobs. Workflow YAML and all 16 shell blocks pass syntax checks.

For reproduction, set `XRAY_E2E_BINARY` to the current custom build and run the
`Ordinary managed lifecycle and writer boundaries` command from
`.github/workflows/custom-core.yml`. PostgreSQL additionally sets `XUI_DB_TYPE`
and a dedicated `XUI_DB_DSN`. The full gate uses `make test-go`,
`golangci-lint run`, `go build ./...`, and the original scale test with a 25-minute
suite deadline. No frontend or production core source changed in this increment.
Healthy legacy drain/settlement, automatic selection, full UI status and restore/global-budget fencing remain separate unfinished work.


### Permanent managed identity deletion

The real-child regressions exercise single/bulk deletion with both keep-traffic
settings, established-flow closure, lifetime preservation, stopped/lost-control
recovery and higher-version regrant rejection. Additional cases cover both orphan
entrypoints, never-initialized identity replay, SQL intent rollback, pending-read
and receipt-write failure, empty bulk scope, sibling-flow survival and fresh
identity/usage after email reuse. A 100001-record history regression first failed
at the old 100000 cap; bounded pages now reach its final identity and avoid
resweeping confirmed absence. Runtime tests traverse 1000 unknown IDs before a
live identity and reject stale initialization/candidate intersections before
confirming absence, with SQL callbacks outside the Runtime lock.

Compile-only omission checks independently reproduced stale bootstrap acceptance,
repeated absence scans, hidden SQL failure, late email marking and loss of the
migration table. Product sources were unchanged by those checks. The migration
regression covers SQLite to PostgreSQL, export back to SQLite, SQL dump/restore,
and a previous schema without the tombstone table or absence column. CI requires
explicit PASS records for all 13 permanent-deletion service/Runtime tests in both
database jobs. Final verification used Go 1.27.1 and the credential-revocation
custom binary on the recorded ARM64 host. SQLite expanded race tests passed in
98.427 s (service) and 1.814 s (Runtime). The PostgreSQL expanded service run took
407.001 s: all new deletion cases passed, while two existing orphan tests exposed
a shared-schema fixture collision. Their helper now uses the existing isolated
schema setup; the affected orphan/identity subset passed twice in 25.975 s.
PostgreSQL database/migration tests passed in 10.934 s. Final lint reported zero
issues; the complete shuffled panel suite passed in 249.40 s wall time, and
`go build ./...` passed in 8.16 s. Source review found no remaining blocker.
Workflow YAML and all 18 shell blocks pass syntax checks.

Reproduce the dedicated service/Runtime gate with the `Permanent managed identity
deletion` step in `.github/workflows/custom-core.yml`; use a dedicated PostgreSQL
DSN for that job. The cross-database test is
`go test -race ./internal/database -run '^TestClientPolicyCrossDatabaseMigration$'`.
The frontend and production core source are unchanged in this increment. Healthy
legacy handoff, automatic selection, complete UI application status, global node
revocation and coordinated backup rollback fencing remain unfinished.

### Legacy counter IO boundary prerequisite

Delayed-IO regressions first failed because sealing returned before the last
counter update. They cover ordinary Read/Write, WriteAllBytes and vector writes,
unclaimed timeout reads, readv, Copy accounting options, dispatcher writers,
inbound UDP, freedom UDP, WireGuard packets, Vision direct IO and writer transition,
and real Linux TCP splice. Splice tests use each participating manager separately,
so a missing inbound or user guard cannot hide behind an outbound lease. The
pre-splice loop waits at a channel-observed second read. Follow-up tests cover
partial transfer with error, rejected secondary/tertiary counters, nested admission
after sealing, reset/removal refusal, and independent repeated snapshot maps.

The expanded scoped race command passed: `go test -race -count=1 ./app/stats
./proxy ./common/buf ./app/dispatcher ./app/proxyman/inbound ./proxy/wireguard`
from `core/xray`. Package times were 1.434, 1.190, 1.721, 1.113, 1.093 and 1.133 s.
Source review found no blocker for this prerequisite. The complete shuffled core
suite passed in 646.25 s wall time, including protocol scenarios in 332.847 s.
The expanded core race step passed in 85.80 s wall time. The custom binary built
in 20.86 s; the complete shuffled panel suite using that binary passed in 299.54 s.
Panel lint reported zero issues (25.94 s), and `go build ./...` passed (8.76 s).
Changed Go files are formatted; workflow YAML and all 18 shell blocks validate.
CI's race step now includes buffer, statistics, proxy and WireGuard packages.

There is no public freeze/drain RPC or new capability. These tests establish
metered IO accounting boundaries, not cancellation of every business socket,
boot-scoped handoff, final SQL settlement or automatic managed activation.

### Mux cancellation and byte interface accounting

A real VMess TCP/XUDP regression exposed zero outbound upload accounting through
BufferToBytesWriter's promoted raw Write method. Delayed writes through its direct,
buffered-flush and unbuffered interfaces reproduced early sealing; explicit Write
now holds a lease through the counter update. ReadVReader's exposed byte Read has
the equivalent regression, without claiming an identified production bypass.

Real VMess TCP/XUDP traffic now has nonzero wire counters, closes through the
outbound handler and yields a repeatable final snapshot. Omitting the mux close
implementation makes both cases hit the snapshot deadline. Separate regressions
cover idle proxy cancellation, a blocked factory, rejection after closure, cleanup
of a late worker, concurrent close acknowledgement, both mux pools and propagated
proxy close failure. The combined buffer/mux/policy race run passed (1.801, 2.094
and 13.514 s package times). Source review found no additional reachable byte
interface bypass in current production call chains; this is not an assertion
that every exported wrapper interface supplies counting.

The complete shuffled core suite passed in 662.49 s, including protocol scenarios
in 337.642 s. Expanded race checks exposed an existing unsynchronized outbound
tag-cache pointer and test stop flags. Cache selection now holds the manager read
lock; test flags are atomic. All other expanded race packages passed, and the
affected full outbound race package passed again after this fix (11.080 s).
The rebuilt custom core passed the complete shuffled panel suite (304.96 s).
Lint reported zero issues (26.90 s), panel build passed (12.60 s), and changed Go
formatting, workflow YAML and its 18 shell blocks validate. CI now includes
outbound and mux packages in race checks.

Normal outbound socket cancellation, boot-scoped private control and panel final
settlement remain required work before exposing a drain capability.

### Ordinary outbound ownership prerequisite

Real TCP sockets and VMess UDP first reproduced active sockets surviving Close.
Additional regressions reproduced a closed handler reaching its dialer, admitted
dispatch/dial contexts surviving closure, and late dial results being published.
The candidate cancels those contexts, closes tracked sockets and rejects queued
dispatches. Omitting the close callback leaves connection/task records behind;
normal socket closure now releases both. Previous dial tests used an unopened
port and asserted wrapper types despite failed IO; they now transfer real bytes
with accounting enabled/disabled and verify exact counters.

Tracking initially removed no-counter readv and MultiBuffer datagram metadata;
real TCP and pipe-backed connection regressions verify both remain intact.
Review found VLESS preconnect retrying a permanently closed owner and deferred
WebSocket publishing after closure. Both failed before their fixes. The WebSocket
test pauses a real successful handshake immediately before publication; the late
socket now closes. Pre-handshake deadline calls previously panicked and now
return an explicit error. Real VLESS with two preconnections transfers TCP/UDP and
returns stable final counters after handler closure.

The isolated full-package Go 1.27.1 race run passed for outbound (11.100 s), buffer
(1.789 s), VLESS outbound (1.044 s), WebSocket (1.052 s) and policy integration
(13.857 s). Record release and queued-dispatch race checks passed separately
(1.059 s). The complete shuffled core run passed every package except policy
integration (653.72 s wall time, scenarios 343.862 s). Its mux test asserted a live
counter immediately after the echo, before the sender had necessarily recorded
its write. All three lifecycle integration tests now assert the final sealed
snapshot; removing the byte Write fix still fails both TCP and UDP accounting.
The full policy package rerun passed (11.619 s), and the expanded core race gate
passed (67.69 s wall time). The custom core built (17.73 s) and passed the complete
shuffled panel suite (301.90 s). Lint reported zero issues (26.23 s); panel build
passed (9.20 s). Changed Go formatting, workflow YAML and all 18 shell blocks
validate. CI includes VLESS outbound and WebSocket race coverage. Known manager
and transport shutdown gaps and private boot-scoped handoff remain open.


### Transport cancellation prerequisite

Real UDP and unanswered HTTPUpgrade requests reproduced packet/socket retention
before cancellation fixes. XHTTP tests exercise unanswered HTTP requests, raw H1
dial/write/response waits, underlying production dial exit, explicit client Close,
existing streams on an unhealthy client, and detached established connections.
Real HTTP/3 tests cover two waiters with only the first canceled, native redial
after the first connection closes, a retired dial followed by streaming upload,
and closure of a successful QUIC connection absent from the HTTP cache.

The shared-handshake, native-retry, client-admission and raw-packet-close cases
failed before their fixes. Separate omission checks fail when removing upload
body retention, failed QUIC socket cleanup or independent QUIC ownership. The
failed-handshake test verifies that the UDP port can actually be rebound. The
full affected-package shuffled race run passes for XHTTP (1.857 s), HTTPUpgrade
(1.042 s) and realm (1.027 s). Complete root validation then passed: shuffled
core suite 621.89 s (scenarios 333.183 s), custom build 7.16 s, expanded core race
76.25 s, shuffled panel suite 292.74 s, lint zero issues 25.73 s and panel build
8.24 s. Nine Go files are formatted; workflow YAML and all 18 shell blocks
validate. The tests do not establish idle raw H1 pool or complete manager and
transport drainage.


### Manager and reverse ownership candidate

Regressions first reproduced removed outbound handlers never being closed,
selection after manager Close, callbacks blocked by the manager lookup lock,
missing retirement/error ownership and publication after failed Start. Tests also
verify rejected inbound/outbound constructors close their created proxy resources
without changing the original error identity. Review found VLESS reverse creation
waiting forever on a closed manager and unadmitted cleanup deleting a live route.
Both failed before ownership-aware cleanup and atomic nondefault registration.
Replacement routes remain intact and static mux pools close existing/late workers.

The expanded shuffled race command initially imposed a 90 s package timeout;
core TestXrayDial was still comparing its 10 MiB payload with go-cmp when it timed
out. With a 10 minute limit the complete core race package passed in 110.716 s.
The other expanded packages passed: VLESS inbound 1.042 s, reverse 1.044 s,
inbound manager 1.129 s, outbound manager 11.120 s, commander 1.041 s, metrics
1.087 s and policy integration 13.559 s. Source review found no remaining blocker
for this ownership increment. Final root gates passed: complete shuffled core
suite 630.02 s, custom-core build 11.46 s, expanded race suite 76.02 s, complete
shuffled panel suite 299.64 s, lint with zero issues 26.27 s and panel build
8.31 s. Go formatting, workflow parsing and embedded shell validation pass.


Boot-scoped drain candidate (2026-09-29): missing control capability, canceled
startup, failed closure behind active IO, unbounded RPC responses and invalid
initial cursor side effects each have observed RED regressions. Core race tests
pass in 1.383 s and private RPC race tests in 7.639 s. The default gRPC client
reads 200002 counters for 100001 users across two endpoints sharing one instance;
the former all-at-once response failed at 13167150 bytes against the default
4194304-byte limit. Long counter names also exercise the 1 MiB page byte budget.
Independent JSON configuration and private transport validation pass. Real direct,
VMess and mux TCP/UDP flows pass final user accounting, connection closure and
port-release checks. Source review fixes are included. Complete root gates passed: shuffled core suite
653.92 s (scenarios 340.132 s), custom build 1.69 s, expanded core race 93.51 s,
focused core drain race 3.35 s, and all 51 panel package results. A daemon restart
lost the runner session after the panel stage; its complete log was audited and
the remaining lint/build steps were run with persisted exit codes. Lint reports
zero issues (29.81 s), panel build passes (8.23 s), and formatting plus all 18
workflow shell blocks pass. No panel handoff is enabled by these tests.


Process final-settlement candidate (2026-09-29): real child tests cover a lost SQL
commit acknowledgement, replay before a fresh final delta, failed final commit
retry with the same batch ID, child restart identity and unpinned refusal without
closing traffic. A real unmodified second child previously pinned another core's
socket; Unix peer-PID verification rejects it and leaves the owner healthy.
Cancellation during pending replay and exhausted sequence checks occur before
drain. Malformed pages, changed boots, wrong peers, duplicate counters, negative
values and page limits are rejected. Endpoint changes fail hot-diff preflight and
require restart; formatting-only changes do not.

Focused race checks pass (2.925 s); final complete shuffled internal/xray race
suite passes (18.514 s). Source review passes. Root gates also pass: internal/xray
race (22.27 s including command overhead), complete panel suite (282.43 s), lint
with zero issues (24.57 s) and panel build (8.19 s). Formatting and all 18 workflow
shell blocks pass. This increment does not yet wire SQL service handoff.


Automatic control checkpoint (2026-09-29): initial real-process tests failed with
no pinned boot and an invalid child reported as started. Focused race passed
after provisioning. Review then exposed the shared Custom brand as an unsafe
compatibility selector: its regression failed for an old Custom version. The
new dedicated configuration-hint regression and core version test pass. Real
new Custom, pre-control Custom and upstream binaries verify ordinary traffic,
unsupported-drain refusal, unchanged desired config, new identity/directory on
restart, failed-start cleanup and explicit-directory preservation. Full shuffled
internal/xray race passes (19.112 s). Source review passes. Root full internal/xray race (34.84 s command time) and
complete panel suite (308.33 s) pass. Lint initially caught an error-comparison
style issue and octal formatting in the new test; after correction, focused race
passes (1.590 s package time), lint reports zero issues, and panel build passes
(8.26 s). Explicit Go formatting and all 18 workflow shell blocks pass.
The version-output addition passes targeted core tests and builds;
core data-path implementation is unchanged in this increment.


Live handoff candidate (2026-09-29): real Tunnel traffic verifies ordinary-poll
settlement, a lost commit acknowledgement and a fresh final delta before seed
capture. The hand-checked baseline 100/200 plus 5+6+4 echo bytes produces a
115/215 seed billed at 330; four subsequent managed echo bytes at multiplier 2
produce 119/219 raw and 346 billed. SQL failure retains the frozen snapshot and
retry ID. Tests reject unmetered configurations, listener conflicts, changed
installed images, changed credentials, replacement stable IDs and a business
listener that merely uses the API tag. Mid-settlement executable replacement
still launches the pinned image. Delimiter-containing emails retain exact usage.

Crash regressions initially showed a fresh panel starting from stale usage after
failed final SQL, both with and without the configuration opt-in. Durable intent
and atomic completion now fence that path, including direct bootstrap/seed calls.
A final receipt update failure rolls back completion and usage together. A final
commit followed by policy-preparation failure survives loss of the panel process;
the next process captures 105/205 and 310 billed. A completion marker without its
referenced receipt is refused.

The first complete root gate passed internal/xray race and PostgreSQL handoff,
but failed TestTrafficHandoffImagePreservesDefaultResourceDirectory in the full
panel suite. An earlier API test left an explicit asset-directory environment
variable. Clearing and restoring all four asset/certificate variable names in
the default-directory fixture fixed the original shuffle seed (1790720481238172087,
17.723 s for internal/xray) and a deliberately contaminated environment race run
(1.866 s). Production explicit environment preservation remains unchanged. The
initial journal SQLite handoff race suite passes (15.379 s). PostgreSQL race
passes for service handoff (39.134 s) and cross-database migration (10.436 s),
including pending/completed markers through export/dump/restore and older schemas.
Final root internal/xray race (22.53 s command time), SQLite handoff race
(18.73 s) and complete panel suite (302.85 s) pass. Lint initially found the new
test imports lacked the repository's local-module group; that formatting-only
fix passes lint with zero issues (31.11 s) and panel build (8.31 s). All 19
changed Go files meet both repository import grouping and gofumpt; workflow YAML
and all 19 embedded shell blocks validate. Source review found no blocker for
this guarded handoff increment. Large SQL batch bounds remain separate work.


SQL settlement scale candidate: 100001 legacy client traffic rows first failed
with SQLite's `too many SQL variables`. Bounding the first read exposed the same
failure in delayed-first-use membership lookup. A separate real client linked
to 3001 listeners failed the original all-at-once inbound save. Bounded reads
and saves preserve one outer transaction. Regression checks cover a failure on
the final client UPDATE, rollback of all previous usage and receipt work, retry,
a duplicate email crossing lookup batches, lost-ack receipt replay, exact row
retention, and equal first-use deadlines in canonical and all listener settings.
The isolated SQLite race candidate passed (38.307 s); after strengthening the
late failure and duplicate checks, PostgreSQL race passed (66.923 s). Root
focused SQLite race, including live handoff regression, passes (45.421 s package
and 48.83 s command time). Complete root panel suite (300.03 s), lint with
zero issues (26.96 s) and panel build (10.29 s) pass. Source review, Go
formatting, workflow YAML and all 19 shell blocks pass. Both SQLite and
PostgreSQL CI paths explicitly require the three new scale tests to pass.


Automatic activation checkpoint (2026-09-29): tests first reproduced ordinary
owned Tunnel startup without enforcement, legacy policy edits leaving the old
child active, and stopped-core reconciliation falling through to legacy updates.
The fixed path runs real Tunnel traffic without a template opt-in. Baseline
100/200 plus four echo bytes settles to 104/204 raw and either 308 billed at the
default multiplier or 316 at multiplier 2. Editing a live metered legacy client
to multiplier 0.5 captures 105/205 and 310 billed before activation, then four
managed echo bytes produce 109/209 raw and 314 billed without repricing history.

Selection tests cover local explicit/default policy, owned Tunnel, omitted legacy
authentication policy, remote-only, disabled-listener and unbound clients. A
manually stopped core stays stopped with queued work. Unmetered legacy edits
return the managed-apply and drain-capability errors, leave desired version zero
and retain the original working child. Compiler regressions reject public or
business-routed API-tag listeners before preparation. Focused PostgreSQL race
passes (138.557 s), and complete SQLite service race passes (339.454 s), both
with real Custom/upstream process fixtures. The two CI lifecycle jobs select and
require all five automatic-activation tests.
Final root checks pass: full panel Go suite (314.69 s), lint with zero issues
(32.64 s), and panel build (19.19 s). Source review, Go formatting, workflow
YAML and all 19 embedded shell blocks pass.


Pending-policy regressions reproduce saved-but-unprepared upload/download rate,
multiplier, quota, enabled-state and expiry changes with equal acknowledged and
prepared versions. A real managed-client disable with injected compiler failure
reproduces the same missing indication while stopping old access. The corrected
projection marks pending before preparation, through version acknowledgement,
and during reset acknowledgement, then clears it without changing lifetime
usage. Renaming an account retains confirmed accounting without inventing an
enforcement change. SQLite focused race passes (28.483 s), PostgreSQL focused
race passes (121.284 s), and the real traffic component regression changes from
a missing indicator to four passing component tests. Generated schemas and API
documentation include the new boolean.

The first combined root gate exposed two earlier fixture races. The legacy job
fixture captured inbound downlink before the transport counter completed; it now
waits for all six counters, including the fixed 26/2-byte IPv4 VLESS headers. The
original job shuffle (1790723776738892903) passes five race repetitions (47.003 s).
The bootstrap fixture deferred a promoted inner Stop method, allowing the outer
process finalizer to stop its restarted child. Forced GC reproduced the missing
control socket; cleanup now retains the outer owner. The original service shuffle
(1790723842097750927) passes five forced-GC race repetitions (11.446 s).
The concurrent frontend gate passed 1757 tests but hit the default 5-second limit
in three Happ routing-editor cases and the bulk calendar renewal case. The separate
serial rerun passes every frontend check, including all 178 files / 1781 tests
(308.66 s), typecheck, lint, format, production build and Storybook. Complete Go
checks then pass: shuffled panel suite (273.23 s), lint with zero issues (25.73 s),
and build (8.30 s). No timeouts, assertions or test diagnostics were suppressed.

Policy form tests first reproduced absent fields and lost existing policy in an
unrelated save. Six real form tests now verify edit payloads, exact multiplier
strings, unchanged legacy omission, clearing overrides, correcting excessive
precision, and creating a client with both its selected inbound and explicit
default policy (11.45 s). Fourteen schema cases cover positive/default bounds,
invalid rates, exponent/whitespace syntax and fractional precision. Removing
validation makes ten negative cases fail; the restored implementation passes all
14. The earlier combined 19-test focused run and TypeScript check pass.

Remote-scope regressions first reproduced partial local writes during mixed-node
create/attach, filtered updates writing shared policy, and raw SyncInbound accepting
new or changed remote policy. Metadata HTTP regression reproduced re-sending a
policy inherited from SQL despite an omitted input field. The corrected projection
uses a separate request copy and retains SQL/inbound settings. SQLite scope and
reservation race checks pass (7.309 s), and the earlier PostgreSQL scope set passes
(30.525 s). Removing the unbound write-transaction recheck in an isolated overlay
reproduces an update succeeding after a concurrent remote attachment.

Mirror tests additionally reproduce valid/invalid multiplier and wrong-type policy
fields being saved despite SyncInbound errors, and foreign/tombstoned identities
being removed from the checked list while retained in raw settings. Guards now
check the actual saved settings before those filters. The first invalid-input test
attempt accidentally reused the valid fixture and is not RED evidence; corrected
wire fixtures fail for the intended cases before the repair. UI regressions use
the repository's Vitest assertions; unsupported Jest DOM matchers in the first
attempt were corrected before recording behavioral RED. Remote/unknown binding
controls then pass the 24-test focused form/schema set; the final set also covers
legacy remote creation and rejecting a new remote binding on an existing policy.
The final complete frontend run passes 178 files / 1787 tests (315.97 s command,
312.94 s suite), plus typecheck, lint, format, production build and Storybook.
Final PostgreSQL scope race passes (41.713 s package / 60.56 s command), including
the actual row-lock concurrency test; SQLite scope race passes (7.514 s package /
10.92 s command). The shuffled complete panel Go suite passes (297.12 s command).
Lint then requested a tagged switch in the new test's scenario selector; that
syntax-only repair passes the affected SQLite/PostgreSQL race cases again,
full lint with zero issues (24.05 s), and build (8.23 s). No production code or
assertions changed for that repair. Source review, all locale JSON, workflow YAML
and all 19 embedded shell blocks pass. The unchanged core source did not require
repeating its previously recorded full suite.


## Local Tunnel owner selection and history

The owner command is tested through JSON decoding and ordinary Add/Update, using
an existing stable UUID with quota `9007199254740993`, disabled state, credentials,
Vision Flow, rates and an exact decimal multiplier. A forged settings client is
ignored. Late link failures roll back listener/membership/traffic writes; replacing
the sole owner preserves its accumulator, while a sibling receives the detached
row when present. Migration and subsequent deletion of the reassigned listener
retain the former account's counters. A stale settings email cannot determine the
former identity. Read APIs reject ambiguous ownership and SQL lookup errors.

Scope tests cover empty/malformed/unknown IDs, protocol/node restrictions,
nonempty imported statistics, existing remote memberships, later remote binding
before any desired policy version, raw mirrors before foreign-identity filtering,
and historical mixed graphs before client-update fanout. The PostgreSQL race
synchronizes both transaction orders at the stable-ID lock; only one membership
commits. Single-inbound controller import discards the source-panel annotation
and retains imported usage under a newly generated destination identity.

The real core reassignment test shares an initial owner between two listeners,
starts at raw upload/download `100/200` and billed `300`, transfers 21 bytes each
way at multiplier 2, and reassigns one listener. Its old TCP flow closes; the
sibling still transfers. New TCP and UDP traffic (including the old UDP peer)
bills the replacement owner at multiplier 3. Final old-owner totals are
`126/226/404`, and replacement totals are `24/34/114`, including its `10/20/30`
history. Both SQLite and PostgreSQL real-core runs passed before the final gate.

Ten frontend tests cover DBInbound/form/wire round trips, server-owned account
fields, nullable legacy clients, local-only commands, required-owner navigation,
paged search, and selected-label retention. An initial picker timeout occurred
while Go compilation ran concurrently; an isolated serial rerun passed all ten
in 10.46 seconds. Test syntax and sandbox startup errors are not counted as RED
behavior evidence. The discarded remote-create fixture tried to use a deployment
selector that existing protocol eligibility deliberately omits for Tunnel.

Behavioral RED evidence is retained under `/root/task-evidence/tunnel-owner-*`:
ignored stable-owner selection, history deletion/misattribution, missing read
annotations and client IDs, source UUID import rejection, unprepared remote
scope bypass, raw mirror filtering, partial update fanout, lost legacy client
arrays/null, wrong validation tab, and missing paged selection. CI requires actual
PASS results for the real-core test and, on PostgreSQL, the identity-lock race.
This checkpoint does not validate all restore/replay fences, global node budgets,
Tunnel source ACLs or every forwarding mode.

The complete Go regression initially exposed a legacy node expiry failure:
GORM `Save` allocated a nil embedded policy while assigning fields, turning an
ordinary metadata update into an explicit default policy. The stricter raw mirror
scope check then correctly refused the second node. A real SyncInbound regression
and the existing legacy JSON seeder both failed for this unwanted policy creation.
`model.SaveClientRecord` now preserves absent policy columns in all four production
record-save paths; explicit defaults, custom values and explicit resets still
persist. Both reproductions and the original two-node expiry test pass. CI also
requires the policy-presence regression on SQLite and PostgreSQL.

The first PostgreSQL controller-import run reused the public test schema and
collided with its previous fixture port. The controller test now uses the existing
per-test schema isolation helper; three consecutive runs pass. This fixture repair
does not change production port conflict checks.

Checkpoint verification (Linux arm64, Go 1.27.1, Node 26.10.0; commands run
serially except the short isolated dependency-race reproduction):

| Check | Result |
| --- | --- |
| Full frontend | 180 files / 1797 tests, 317.91 s suite; typecheck, lint, format, production and Storybook builds pass |
| Final PostgreSQL owner/scope/policy-presence race set | Pass, 140.25 s command / 97.857 s service package; real core and both lock interleavings executed |
| SQLite owner/scope/migration race set | Pass, 27.17 s command; final nil-policy regressions and original node-expiry reproduction also pass |
| Complete shuffled panel Go suite | Pass, 287.12 s command |
| Final real TCP/UDP owner reassignment after error-assertion lint repair | SQLite and PostgreSQL race pass, 3.197 s and 3.393 s packages |
| Final Go static check / build | Zero lint issues, 23.92 s; build passes, 8.21 s |
| Workflow and generated assets | `make gen-check` passes; YAML and 19 shell blocks valid; locales parse; frontend/docs OpenAPI and installed MSW worker copies match |
| First complete repository race, before dependency repair | Failed on inherited AmneziaWG timer race; subsequent repair gate below passes |

The broader race gate exposed `Timer.duration` being cleared after unlocking in
`amneziawg-go/v3 v3.1.20260828` during the existing IPv6 domain-egress test.
The main baseline already pins this dependency; its integration sources and root
module files are unchanged by the owner editor. An isolated real-timer regression
reproduces the race, and moving the clear into the existing critical section passes
ten repetitions in a temporary candidate. The candidate result alone does not establish a passing whole-repository race
gate. The managed-source repair and its subsequent validation are recorded below.


### AmneziaWG dependency synchronization and first-packet repair (2026-09-30)

The managed source retains all 121 files of v3.1.20260828. A byte comparison against
the verified module found only two modified upstream files (`device/timers.go`
and `device/send.go`) and one added regression test; LICENSE is unchanged.
This repairs the existing panel-side runtime, not the pending single-core migration.

The real timer regression fails on the original unlocked duration clear and passes
ten times with the repository replacement under `-race -mod=readonly`. Callbacks
remain outside the modifying lock so they can rearm the timer.

The dependency device suite then exposed `TestAWGDevicePing` timing out. Five
repetitions fail both with the timer-only patch and the original module, with
unknown transport packet types after successful handshakes. A deterministic TUN
wrapper waits until both real devices have entered their read, changes S4, then
sends actual packets in both directions. Before the send-path repair both first
packets time out at S4=25. Afterward, increases to 25, decreases to 7 and clearing
to 0 retain the exact payload. That test and the real UDP socket Ping test each
pass ten times under `-race` (1.994 s package). The change covers configuration
applied during a blocked TUN read; it does not promise atomic reconfiguration of
packets already queued for encryption.

`make test-go`, `make race`, and both corresponding CI jobs explicitly run the
patched dependency device suite because `./...` excludes nested modules. CI YAML
and all 25 shell blocks parse. Read-only source review found no outstanding issue.

Serial integrated checks use Go 1.27.1 / Linux arm64 and the real custom/upstream/
legacy core fixtures recorded above. Intermediate results: the AWG runtime,
protocol and dependency-device race suites pass (44.66 s command), golangci-lint
reports zero issues (88.93 s), and the panel build passes (8.40 s). The complete `make race` passes (783.05 s), including the service package
(337.941 s), internal/xray (20.357 s), and the explicit dependency device suite
(13.650 s). The existing upstream interactive, endless handshake test remains
skipped as designed; it is not counted as automated interoperability evidence.


The two required parser fuzz targets each complete 30 seconds of exploration:
`FuzzParseLink` (56.88 s including build) and `FuzzDecodeCertPin` (300.31 s including
its first instrumentation build; 79,276 executions). `make vulncheck` passes using
govulncheck v1.8.0 (14.25 s): zero reachable-symbol and zero imported-package
findings. A separate module-only scan reports
[GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) for unmaintained
`golang.org/x/crypto/openpgp` in required module x/crypto v0.57.0, with no fixed
version listed. The application does not import the affected package according
to the symbol scan; the module-only warning is retained, not counted as a failure
of the default reachable-code check or silently suppressed.

### Standalone clients before the first listener (2026-09-30)

Five Go regressions first failed because create required an inbound. The repaired
existing API accepts empty or omitted IDs, creates a server-owned stable UUID,
preserves disabled state and exact quota `9007199254740993`, and retains explicit
policy/HWID settings. Duplicate email (including a case variant) or subscription
identity cannot overwrite the original account. An injected disabled-state write
failure rolls back creation. An unattached account has no listener, traffic row,
protocol credentials, policy version or core restart.

`TestClientStandaloneCreationFeedsRealTunnelLedger` starts from an empty database,
creates the canonical client, attaches a Tunnel, starts the real managed core and
echoes six TCP bytes and six UDP bytes. Both SQLite and PostgreSQL retain the same
stable identity and settle exactly 12 upload, 12 download and 36 billed bytes at
multiplier 1.5. The five standalone tests pass under race on SQLite (service
3.760 s / controller 2.893 s) and PostgreSQL (service 14.099 s / controller 2.983 s,
including the three existing multi-inbound creation regressions).

The form regression first failed because its create schema required a binding;
the fixed form and existing policy form pass 13 tests. Review also exposed an
owner-picker cache outside the client invalidation family. A real QueryClient
using production's 30-second freshness and the real `useClients.create` hook
reproduces the missing new owner after caching an empty list. Owner choices now
use the shared client query-key family.

The owner-cache regression and standalone form pass together (2 files / 6 tests,
13.19 s). Read-only backend and frontend review found no remaining issue after
that cache repair. The complete frontend gate passes 181 files / 1799 tests
(330.27 s suite), with TypeScript, lint, format, production and Storybook builds
also passing. The earlier full frontend run was deliberately interrupted before
changing the cache key and is not counted as a passing run.

The serial complete shuffled Go regression (`make test-go`) passes in 265.85 s,
including the explicit managed AmneziaWG dependency suite. Go static checks report
zero issues (52.84 s), and `go build -mod=readonly ./...` passes (8.27 s).

The complete `make race` gate also passes (745.85 s), including the full service
package (333.342 s), the Xray adapter and the explicit managed AmneziaWG device
suite. No production code changed during this final serial gate.

Final `make gen-check` passes; the frontend/docs OpenAPI copies and the installed
MSW worker match, all 13 locale JSON files parse, and workflow YAML plus all
19 shell blocks validate. The staged diff has no whitespace errors.

### Tunnel physical-source CIDR ACL (2026-09-30)

Environment: Linux ARM64, two CPUs, Go 1.27.1, Node 26.10.0, the managed Xray
26.9.9-custom.1 source. Tests run serially with `GOFLAGS=-p=1`. The candidate
binary reports `0878e1e1986a633d597966ef0d758457d337afff-dirty`; the actual older
custom-core fixture reports `23b1b4b` and lacks `tunnel-source-acl-v1`. Build the
candidate using `bash tools/build-custom-core.sh /tmp/custom-xray-source-acl-test`.
CI additionally builds an overlay fixture that removes only the ACL capability,
and explicitly requires each new process/service test to report PASS.

Core behavioral RED tests first showed invalid CIDRs and unsafe transports being
accepted, and disallowed IPv4/IPv6 TCP/UDP peers reaching the echo target. A
verified-TLS test also showed a disallowed peer completing its handshake. Shared
JSON and typed-construction validation plus the pre-dispatch peer check make the
regressions pass. An additional typed `localhost` receiver with no port list was
accepted as a Unix listener before the port guard; zero/empty/nil/reversed and
out-of-range port cases now fail configuration validation. Valid raw TCP/UDP,
ordinary verified TLS, no-op headers and normal localhost remain supported.
PROXY socket/raw settings, WebSocket/HTTPUpgrade/XHTTP, custom headers, transport
masks and mapped IPv6 prefixes are rejected when the ACL is nonempty. Effective
`method` and raw-versus-legacy settings precedence are tested.

The real IPv4 and IPv6 tests independently count target TCP accepts and UDP
packets. Three allowed six-byte echoes across TCP, UDP and a sibling listener
produce exactly 18 upload / 18 download / 54 billed bytes at multiplier 1.5.
Disallowed sources reach neither the target nor the policy ledger. Replacing the
handler closes its old TCP flow and rejects old/new UDP and new TCP from the now
excluded source. The sibling stays alive and another six-byte echo advances the
same ledger to exactly 24 / 24 / 72. The TLS test validates a generated certificate
against an explicit trust root and bills plaintext payload, not TLS framing.

Panel RED tests showed invalid create/update configuration being committed,
ownerless ACL listeners being enabled, and restored invalid rows reaching config
generation. Review then found that case-insensitive core JSON fields could bypass
the panel's exact map lookup. Regressions cover one alternate spelling and both
orders of mixed empty/nonempty spellings, including startup restoration. The
panel now rejects noncanonical spellings before mutation, avoiding map reordering
changing the effective ACL. Re-enable checks the canonical owner transactionally;
detach retains the ACL while disabling the last-owner listener.

Deletion regressions cover stale embedded client lists and a new canonical
attachment created after the deletion membership snapshot, through the actual
single and bulk APIs. Final cleanup locks current Tunnel rows in ascending order,
deletes canonical memberships and disables newly ownerless listeners atomically.
It performs one postcommit reconciliation after revocation. A real process test
proves both listening ports become reusable, the deleted owner's established TCP
flow closes, its tag disappears from the live config, and another client's
connection survives without a core restart. The stale-settings bulk path was
already correct; an initial raw-string assertion failed solely because JSON was
reformatted and was corrected to compare the saved settings semantically.

The real panel hot-narrowing test starts with historical 100 upload / 200 download
/ 300 billed bytes and multiplier 2. Three six-byte echoes produce 118 / 218 / 372.
Invalid normal and mixed-case ACL updates leave the database and live flows
unchanged. Two more echoes precede narrowing; rejected sources leave totals at
130 / 230 / 420 and the UDP target has seen exactly one datagram. The newly
allowed source and surviving sibling bring totals to 148 / 248 / 492 and two UDP
datagrams. The core boot identity is unchanged. Actual older/current-core startup
checks reject the missing capability before preparation or any business listener;
hot AddInbound rejects before HandlerService mutation, even without clientId.

Focused commands and observed results:

```sh
# From core/xray: real IPv4/IPv6, TLS, JSON and typed configuration.
GOTOOLCHAIN=go1.27.1 GOFLAGS=-p=1 go test -race -count=1 -v \
  ./infra/conf ./testing/policy -run TestTunnelSourceACL
# From the repository root, with the two binaries described above.
GOTOOLCHAIN=go1.27.1 GOFLAGS=-p=1 \
  XRAY_E2E_BINARY=/tmp/custom-xray-source-acl-test \
  XRAY_PRE_SOURCE_ACL_E2E_BINARY=/tmp/custom-xray-auto-control-test \
  go test -race -count=1 -v ./internal/web/service ./internal/xray \
  -run TestTunnelSourceACL
# Use an isolated PostgreSQL database and the same candidate core.
XUI_DB_TYPE=postgres XUI_DB_DSN='<isolated test DSN>' \
  XRAY_E2E_BINARY=/tmp/custom-xray-source-acl-test \
  GOTOOLCHAIN=go1.27.1 GOFLAGS=-p=1 go test -race -count=1 -v \
  ./internal/web/service \
  -run 'TestTunnelSourceACL|TestTunnelOwnerSelection|TestTunnelOwnerReplacement'
```

Expanded core tests pass (policy 2.379 s); the SQLite service/adapter race set
passes in 9.681 s / 1.401 s. The PostgreSQL set, including the subsequently added
real port-release test, passes in 97.076 s. No IPv6 or named ACL scenario is skipped
in these runs. The complete SQLite gate below also includes the port-release test.

Frontend RED tests showed the schema dropping the new field and accepting invalid
CIDRs. The real edit form now sends both the original IPv4 and newly typed IPv6
prefix with its canonical owner. Its initial test omitted Ant Design's required
Enter keyCode; correcting that interaction fixture made the actual save assertion
pass. Additional RED regressions caught mapped IPv6 spellings and legacy null.
The final four-file set passes 23 tests in 18.52 s, covering those inputs, empty
clearing and existing owner behavior. English/Chinese copy and the other 11
locale keys are present; API registry, both OpenAPI copies and API MDX were
regenerated. Final read-only core/backend and frontend review found no remaining
blocking issue. A subsequent static check requested only a simpler boolean in
the concurrency test; that expression was corrected without changing semantics.

The complete canonical `make verify` gate passes in 651.34 s: zero Go lint
issues, frontend lint/format/type checking, generated-file and MSW consistency,
all shuffled Go tests (service 106.477 s, Xray adapter 17.095 s, explicit AmneziaWG
dependency 1.042 s), 183 frontend files / 1811 tests (324.85 s), the production
frontend/Go builds and Storybook. The initial static-check failure is retained as
a failed run, not counted as a passing gate.

The complete `make race` gate passes in 790.01 s, including service 338.080 s,
Xray adapter 20.380 s and the explicit managed AmneziaWG dependency suite.
The complete shuffled core suite passes in 626.54 s, the workflow-scoped core
race suite in 85.57 s (policy integration 14.965 s), and construction/traffic-drain
race checks in 3.73 s. All five final gate commands exit successfully. Product
code and tests remained unchanged during these final gates. Routing/outbound-mode UI,
arbitrary transport ACLs, unowned migration, distributed policy and the other
goal items remain open.

### Managed loopback accounting and bounded routing (2026-09-30)

Same Linux ARM64/two-CPU environment and pinned Go toolchain as the source ACL
checkpoint; this increment starts at d00770f05156ee32f1bd3868cb6458ed95b67e2a.
Real RED tests observed a six-byte TCP echo become 12 upload / 12 download / 36
billed bytes through one loopback, and 18 / 18 / 54 through two, at multiplier
1.5. Active sessions were incorrectly two and three. After policy deduplication,
separate RED assertions still found legacy uplink counters of 12 and 18. Race
checking exposed shared AccessMessage.Detour mutation during asynchronous log
formatting. All three paths are covered by the final regression.

Review then identified two additional boundaries. A real UDP localhost target
with ForceIPv4 and loopback sniffing panicked because EndpointOverrideReader does
not implement TimeoutReader. A shared upload bucket at one byte/second made the
initial managed TimeoutReader take 1.00046 s for a 20 ms sniff timeout. The final
implementation preserves the existing managed-reader interface and adapts only
redispatch, without another counter. A timeout retains its pending byte; later
reading returns it once and total admitted upload remains exactly two bytes.
A separate RED race test exposed the outbound cancellation callback reading link
fields concurrently with wrapping; it now captures the endpoints beforehand.

Bounded fake dispatch first proved one/two-hop cycles and a 17-hop chain were not
rejected. The final guard accepts 16 distinct loopback hops, rejects the 17th and
rejects revisiting the same loopback instance. Real one/two-hop cyclic routes
terminate with zero target bytes, zero usage and no surviving session, even with
a working direct default outbound. This is not a network-listener loop test.

Focused command from core/xray:

```sh
GOTOOLCHAIN=go1.27.1 GOFLAGS=-p=1 go test -race -count=1 -v \
  ./app/dispatcher ./app/proxyman/outbound ./proxy/loopback ./testing/policy \
  -run 'TestManagedRedispatch|TestOutboundCancellationCanOverlapRedispatchWrapping|TestLoopbackBounds|TestTunnelLoopback'
```

The final focused run passes: dispatcher 2.045 s, outbound lifecycle 1.059 s,
loopback 1.028 s and policy integration 4.220 s. Real one/two-hop cases cover TCP,
UDP, repeated HTTP sniffing and resolved UDP targets with sniffing. Two independent
six-byte echoes give exactly 12 upload / 12 download / 36 billed bytes, two active
sessions and legacy counters of 12 each. An independent echo server counts the
payload. Disable closes both TCP streams and blocks both UDP flows without more
usage or target bytes. Independent links under an inherited context still meter
separately; credential revocation closes both and releases all sessions.

The quota test uses a 60-byte budget at multiplier 1.5. Three six-byte echoes
complete at 18 / 18 / 54. A final two-byte echo reaches exactly 20 / 20 / 60;
the target observes exactly 20 upload bytes, both connections close and reconnect
is rejected. The final two admitted download bytes may be in flight when close
occurs. An initial 55-byte fixture incorrectly required delivery after only one
billed byte remained (less than the next raw-byte charge); that fixture was
corrected to exercise a complete final echo and explicitly bound its in-flight
payload. No policy or quota implementation was changed to accommodate the test.

The candidate core build passes in 7.20 s and scoped go vet in 10.75 s. The
complete shuffled core suite passes in 626.77 s; the workflow-scoped core race
suite, including the added loopback package, passes in 92.12 s (policy 17.924 s).
Construction/traffic-drain race checks pass in 3.75 s. Complete `make test-go`
passes in 309.03 s (service 107.548 s, adapter 16.931 s, explicit managed
AmneziaWG device 0.979 s). The affected panel race set passes in 64.01 s (service
21.809 s, runtime 1.324 s, adapter 1.486 s), using the new candidate binary.
Its two PostgreSQL-only row-lock subcases are skipped on SQLite and are not
counted as passes. This repair changes no database code; the preceding ACL
checkpoint separately records PostgreSQL coverage.

The scoped panel race command is:

```sh
GOTOOLCHAIN=go1.27.1 GOFLAGS=-p=1 \
  XRAY_E2E_BINARY=/tmp/custom-xray-loopback-test \
  XRAY_PRE_SOURCE_ACL_E2E_BINARY=/tmp/custom-xray-auto-control-test \
  go test -race -count=1 -v \
  ./internal/web/service ./internal/web/runtime ./internal/xray \
  -run 'TestTunnel|TestClientPolicy(AutomaticallyActivatesOwnedTunnel|ConfigFeedsRealTunnelLedger|NormalUserMutationPreservesIdentityAndOtherFlows|DetachPreservesSiblingInboundFlow|DisableWithCompilerFailureStopsExistingAccess|RuntimeBootstrapPreservesLedgerAcrossChildRestarts)|TestLocalRuntimeUsesPrivateControlForHandlersRoutingAndStats'
```

`make lint-go` reports zero issues (26.68 s); `go build -mod=readonly ./...`
passes in 8.42 s. All nine final commands exit successfully, with no product or
test changes during the gate. Workflow YAML and 21 shell blocks validate. Final
read-only review found no remaining blocker after the three boundary repairs;
the reviewer did not run tests. Frontend behavior, API and schema are unchanged
by this core repair, so the preceding ACL frontend gate remains its evidence.

### Routing failures and explicit fallbacks (2026-09-30)

Baseline 997d883b6590a12fb434af1934e87354a4db7004. The real RED run let a matched,
empty balancer send traffic through a working direct default, both with and
without managed identity. Its ten controls passed: no-match default, healthy
selection, explicit direct fallback, explicit block fallback and missing concrete
outbound, each managed/unmanaged. The dispatcher now reserves default routing
for `common.ErrNoClue`; other routing errors close the link.

The focused race run (`go test -race -count=1 -v ./testing/policy -run
'^TestTunnelRouting'`) passes all twelve cases and four dynamic TCP/UDP removal
cases in 1.852 s. Dynamic tests observe distinct selected/default echo targets.
Before removal, each gets six bytes and the shared ledger is 12 upload / 12
download / 36 billed, at multiplier 1.5. Removing the selected handler closes its
old flow. Without explicit fallback, new traffic and the old UDP source cannot
reach either target; a sibling echo leaves 18 / 18 / 54 and one session. Explicit
direct fallback instead produces 24 / 24 / 72 with two surviving sessions.

Read-only review found no product blocker and identified an asynchronous-session
cleanup assertion: handler removal cancels dispatch without joining its deferred
release. Only the session-count assertion now waits, bounded to one second;
payload, ledger and target counts remain strict. All sixteen cases pass twenty
consecutive race runs (15.133 s). The reviewer did not run tests.

Final serial gates pass: core build 4.30 s, scoped vet 4.68 s, complete shuffled
core tests 625.76 s (scenarios 337.693 s), workflow-scoped core race 96.22 s
(policy integration 18.575 s), and candidate-binary panel race 58.78 s. The latter
uses the preceding checkpoint's scoped command with
`XRAY_E2E_BINARY=/tmp/custom-xray-routing-test`: service 21.897 s, Runtime 1.325 s
and adapter 1.464 s. Two PostgreSQL-only row-lock subcases skip on SQLite and are
not counted as passes; this change touches no database implementation. The final
core race and repeated focused run cover the review's test-only assertion fix.
Workflow YAML, 21 shell blocks, formatting and whitespace checks pass. No panel,
API or frontend behavior changes here; per-rule outbound selection is unfinished.

### Tunnel concrete outbound selection (2026-09-30)

Baseline `073114daab9ac65e1d749f1c1f46d86e404c4ad3`. This increment adds
custom Tunnel protobuf field 11 and `tunnel-fixed-outbound-v1`; it changes no
dependency versions or database schema. Final commands use Go 1.27.1, Node
26.10.0/npm 11.19.1 on Linux arm64 and the newly built `build/custom-xray`.

The resumed RED run of `TestTunnelFixedOutboundTreatsMuxDestinationAsPayload`
reproduced `panic: content.Attributes != nil` in an isolated child process.
An independently formed valid mux New frame targeted a distinct local echo
server; the outer Tunnel must send its 14 bytes unchanged to the selected
target. The mux wrapper treats owned or explicitly selected Tunnel data as opaque.
The GREEN race run:

```sh
cd core/xray
GOTOOLCHAIN=go1.27.1 go test -race ./common/mux ./infra/conf ./testing/policy \
  -run 'TestTunnelFixedOutbound|TestAuthenticatedProtocolsShareTunnelIdentityAndDisconnect|TestLegacyMuxClose|TestRegressionOutboundLeak' \
  -count=1 -v
```

passes (policy package 12.458 s), including inherited selection/block, fixed
selection/block/missing and the ordinary protocol mux regressions. Each allowed
14-byte echo gives 14 upload / 14 download / 42 billed bytes at multiplier 1.5;
the inner mux target gets zero bytes. Fourteen TCP/UDP selected-outbound cases
cover inherited routing, fixed freedom, blackhole, missing handler, loopback,
SOCKS and HTTP. The unsupported HTTP UDP case fails with zero payload at both
echo targets; it does not fall back to direct.

SQLite and PostgreSQL run the same real child-process and transactional checks:

```sh
GOTOOLCHAIN=go1.27.1 GOFLAGS=-p=1 \
  XRAY_E2E_BINARY="$PWD/build/custom-xray" \
  XRAY_PRE_FIXED_OUTBOUND_E2E_BINARY=/tmp/tunnel-outbound-review-fixture/pre-fixed-outbound-xray \
  XRAY_PRE_SOURCE_ACL_E2E_BINARY=/tmp/custom-xray-auto-control-test \
  go test -race -count=1 -v ./internal/web/service ./internal/xray \
  -run '^TestTunnelFixedOutbound|^TestTunnelSourceACL|^TestTunnelOwner|^TestClientPolicyRemoteScope'
```

The fixture is built with the workflow's Go overlay, removing only the advertised
fixed-outbound capability. All six named fixed-outbound service/adapter tests
actually run and pass; unset-binary skips are not accepted. SQLite's command
passes in 57.16 s (service 26.404 s, adapter 1.749 s); its two PostgreSQL row-lock
subcases skip and are covered by the separate PostgreSQL run. With
`XUI_DB_TYPE=postgres` and a private Unix socket database, the same command passes
in 177.07 s (service 170.314 s, adapter 1.721 s).

The hot-update fixture starts with lifetime usage 100 upload / 200 download /
300 billed and multiplier 2. TCP's selected and sibling echoes, then direct and
sibling echoes, then blocked-routing plus sibling echo end at exactly 130 /
230 / 420. UDP also verifies the old source enters a fresh session after listener
replacement, ending at 136 / 236 / 444. Distinct echo servers observe the selected
and direct bytes independently. The child boot remains unchanged, the old TCP
stream closes, no denied target receives payload, and sibling flows survive.

The read-only source review found one Important integration issue: the selector's
indefinitely fresh query ignored the server's background `invalidate(outbounds)`
message. A real WebSocket message through the bridge and a real query hook first
failed with `old-proxy` instead of `new-proxy`. After the cache invalidation fix,
the bridge/shared-socket/Tunnel form/adapter selection passes all 16 tests in
17.02 s with one worker. It also covers an unrelated recent local invalidation
and adjacent inbound notification. Existing Node/Vite deprecation diagnostics
remain visible. The reviewer ran no tests. Broader protocol completion,
distribution, global budgets, packaging and exhaustive transport/accounting gates
remain open in the original plan, rather than being declared out of scope.

Workflow YAML and all 24 run shell blocks parse; touched frontend formatting and
`git diff --check` pass. Full gates and the legacy mux correction are recorded below.

The first `make verify` stopped at code generation because the newly generated
OpenAPI descriptions were not yet staged; the generated file is now included.
The next run rejected `socket = this` in the new test under the repository's
`no-this-alias` rule. The test transport now captures an event-delivery closure;
the 16 focused tests still pass (13.70 s). Another full run found that the local
runner had pointed `XRAY_PRE_REVOCATION_E2E_BINARY` at a binary already advertising
credential revocation. This was a runner fixture error, not a reason to weaken
`TestManagedProcessNegotiatesCredentialRevocationBeforePreparation`. Rebuilding
the omission fixture exactly as CI does makes both older/current-core cases pass
under race in 1.445 s. Failed logs are retained in `/root/task-evidence`; none of
those interrupted gate runs is reported as a full pass.

The two-worker full frontend run completed with 184 files / 1824 tests passing
and one existing Happ editor test timing out at its unchanged 5000 ms limit.
Its assertions and timeout were not modified. The installed Vitest supports
`VITEST_MAX_WORKERS`; the complete serial gate was rerun with that variable set
to 1. `VITEST_MAX_WORKERS=1 make verify` passes in 780.27 s, including generation,
both linters, formatting, types, MSW worker consistency, shuffled backend/device
tests, all 185 frontend files / 1825 tests (476.50 s), Vite, Go and Storybook
builds. `make race` passes in 786.23 s, including the explicit AmneziaWG device
package. Both commands use the real current/upstream and capability-omission
binaries described above and the fresh pre-revocation overlay. Node/Vite
deprecation and bundle-size diagnostics remain visible. The failed two-worker
log is retained; only the one-worker full frontend result is a pass.

After that run, a new real regression found the initial mux guard also suppressed
the upstream internal mux gateway for legacy Tunnel without an owner or fixed
selection. `TestLegacyUnownedTunnelMuxStillDispatches` first failed with a reset.
The guard now leaves that legacy path intact while protecting owned or explicitly
selected Tunnel. A valid New/data frame sends six payload bytes to the inner echo
target, returns the expected 14-byte Keep/data response, and leaves the unrelated
owner's usage at zero. Three additional unowned fixed-selection controls verify
selected/block/missing routing independently of ownership. The focused race
command below passes (policy 13.994 s):

```sh
cd core/xray
GOTOOLCHAIN=go1.27.1 GOFLAGS=-p=1 go test -race -count=1 -v \
  ./common/mux ./testing/policy \
  -run 'TestTunnelFixedOutbound|TestLegacyUnownedTunnelMux|TestAuthenticatedProtocolsShareTunnelIdentityAndDisconnect|TestLegacyMuxClose|TestRegressionOutboundLeak'
```

The corresponding CI step requires explicit PASS lines for both Tunnel mux
tests. After the correction, fresh `go test -shuffle=on -count=1 ./...` passes
in 633.80 s (scenarios 336.033 s), the workflow-scoped core race command passes
in 99.04 s (policy 21.702 s), scoped vet passes in 1.99 s, and the candidate
core build passes in 5.32 s. The rebuilt binary repeats the full scoped panel
SQLite command in 55.67 s and PostgreSQL command in 174.95 s, both passing.
SQLite's two PostgreSQL-only row-lock skips are again covered by PostgreSQL.
The panel/frontend implementation is unchanged from the successful full gates
above. The initial core feature commit `d436f7c9` and compatibility correction
`62e6b3285fa7a2c1e8860c4e42fe25e4636b7c1b` have each been pushed to the fork's
`feature/custom-xray-unified-policy`; the latter remote SHA was verified exactly.

The panel/UI/API/CI/documentation commit is
`c635910d40efb320cd361ad138dfb9b24ad3981c`, pushed with an exact remote SHA
match. Comparing the pre-commit hook backup tree to this commit confirms no
hook changed the tested sources. Its independent clean local clone runs
`npm ci` (15.85 s), Vite build (4.29 s), source-stamped panel build (201.03 s)
and custom core build (2.02 s), all successfully. Both version smoke commands
pass and the clone remains clean. Existing caches were shared, but neither
`node_modules` nor compiled project output was copied into the clone.
Exact reproducible commands and artifact hashes are in deployment.md.

## Native password-proxy identity and lifecycle checkpoint

Linux arm64, Go 1.27.1, the managed core module, real loopback TCP/UDP and
private Unix-socket gRPC. No additional protocol dependency or target dialer.
Independent `golang.org/x/net/proxy` SOCKS5 and standard HTTP/wire clients
authenticate against the same server implementation used by ordinary configs.

The RED logs under `/root/task-evidence/password-proxy-*.log` cover lost stable
identity (only Tunnel's six bytes were charged), missing native UserManager,
an idle authenticated UDP association remaining open, UDP close before timer
initialization panicking, old plain-HTTP request cleanup closing a later account's
CONNECT, and missing-capability mutation accepting an empty anonymous fallback.
These failures were observed before the corresponding implementations.

Scoped verification commands:

```sh
cd core/xray
GOTOOLCHAIN=go1.27.1 GOFLAGS=-p=1 go test -race -count=1 -v \
  ./testing/policy ./common/protocol ./infra/conf ./proxy/socks \
  -run '^TestPassword|^TestTempUDPConn'
cd ../..
GOTOOLCHAIN=go1.27.1 GOFLAGS=-p=1 \
  XRAY_E2E_BINARY=/tmp/custom-xray-password-identity \
  XRAY_PRE_PASSWORD_IDENTITY_E2E_BINARY=/tmp/password-proxy-capability-fixture/pre-password-identity-xray \
  go test -race -count=1 -v ./internal/xray -run '^TestPasswordProxy'
```

Verified observations:

- Two authenticated streams and Tunnel share 18 upload / 18 download / 54
  billed bytes at 1.5. Wrong usernames/passwords do not reach the independent
  target; removing the last account does not enable anonymous access.
- Mixed SOCKS and HTTP aliases share upload and download buckets with Tunnel.
  After exhausting a 65,536-byte burst at 1 byte/s, both queued streams remain
  blocked for 200 ms. A hot change to unlimited and multiplier 0.5 releases
  both within two seconds. Final raw totals are 65,566 in each direction;
  billed totals are 196,674 (upload case) and 196,686 (download case), preserving
  historical admission and the separately observed pre-change uploads.
- Idle and active per-control-connection UDP associations close and release
  their ephemeral ports on credential removal; another user on the same IP
  continues with independent 12 / 12 / 36 usage.
- A plain HTTP POST observed by the origin has 93 serialized upload bytes;
  its original response has 70 bytes. At 1.5 the ledger has 244 billed bytes
  and 500,000 remainder. A later account on the same client socket retains its
  CONNECT after policy disable or credential removal of the old request.
- Current and omitted-capability real child processes exercise all three
  protocol names with populated and intentionally empty account lists. Missing
  support fails before preparation or any business listener. The test requires
  both binaries; its CI step also requires an explicit PASS for the named test.

Legacy anonymous HTTP, SOCKS5 and SOCKS4 remain available, and legacy password
statistics retain the wire username. In the pinned upstream version, a Mixed
listener with `auth: noauth` ignores unused `accounts` for both SOCKS and HTTP,
including an incorrect HTTP Basic credential. The new compatibility test
preserves that behavior. An initial contrary test expectation was corrected
after inspecting the upstream constructor; it was not a production regression.
Linux splice commits its counters at EOF;
the independent compatibility origin sends six bytes and closes to establish
that boundary. The initial open-origin fixture did not establish this condition
and its failed statistics assertion was corrected without changing production
splice code or expected counter totals. CONNECT preserves unmanaged splice;
managed flows retain the existing policy engine restriction on it.

Final full-suite results are recorded in
`/root/task-evidence/password-proxy-final-results.json`: complete shuffled core
tests (662.84 s), expanded CI core race (138.38 s), handler-construction race
(3.78 s), affected core vet (5.95 s), core build (4.69 s), complete shuffled
`internal/xray` race with real capability fixtures (29.18 s), panel vet (20.06 s)
and full `make test-go`, including the explicit AmneziaWG device package
(286.04 s), all passed. The adapter run omitted the optional legacy-custom
binary; that named regression passed separately with its real fixture in
`password-proxy-legacy-custom-control.log` (1.533 s).

The final native password/config/UDP race run in
`password-proxy-noauth-compatible-final.log` also passed, including the added
noauth compatibility test. Protobuf regeneration matched both checked-in files,
`make gen-check`, `make lint-go`, affected-file gofmt and `git diff --check`
passed. An optional whole-tree `golangci-lint fmt --diff` reported existing
baseline formatting differences; no unrelated source was reformatted. No
frontend code or contract changed in this increment; the earlier complete
frontend verification remains the applicable checkpoint. Panel canonical
binding, anonymous resource ownership and policy-only closure before first
dispatch remain open.

## Canonical password account persistence checkpoint

Task 5B1 exercises the actual AddInbound/UpdateInbound service and real SQL
transactions. The original implementation saved no owner links, accepted missing
owners and could not trigger the injected late membership failure. RED/GREEN
logs under `/root/task-evidence/password-owner-*.log` record these failures and
the corresponding implementation. Fixtures use two canonical owners, aliases
`alice`, `ALICE` and `用户`, distinct resource passwords, canonical policies and
existing raw usage 123/456. Reassignment, credential rotation, last detach,
protocol conversion and listener deletion preserve shared records and history.

The read-time membership guard initially treated preloaded SQL statistics as
caller-supplied mirrors; a valid detail/read regression exposed that distinction
before the guard was corrected. Deleting a membership rejects raw/detail/list
reads and updates. Generic client sync/delta cannot bypass account ownership.
Configuration generation refuses owned accounts on an unmanaged path, while
managed password adapter activation remains gated.

One inline review found three important issues, all corrected with RED/GREEN:
native users/case aliases bypassed the initial parser; owned Mixed-to-empty-HTTP
conversion lost authentication; ignored legacy email prevented reads and
repair. Tests cover Go JSON Unicode case folding (`uſers`), non-null accounts
precedence including empty arrays, null accounts with active users, ambiguous
case spellings and dormant owner rejection. Dormant unowned users remain
preserved. A further ordinary conversion test prevents imposing custom-core
authentication requirements on an unowned legacy listener. The full root
suite also exposed a restored Tunnel ACL error-precedence regression; its
original assertion passes after the read guard validates that ACL first.

Latest focused SQLite ownership/ACL tests passed in 21.200 s
(`password-owner-final-corrected.log`); PostgreSQL ownership/ACL tests passed in
76.869 s (`password-owner-postgres-corrected.log`). The PostgreSQL case
`TestPasswordProxyOwnerPostgresLocksCompetingRemoteAttachment` holds actual
inbound/client row locks while another transaction attempts a remote attachment.
The latter waits until the first commits, then rechecks and rejects the new
local-only membership even with no explicit policy. This case intentionally
skips on SQLite; its PostgreSQL PASS is required in custom-core CI. Earlier
broader SQLite Tunnel/link/scope regressions passed in 52.327 s. The final full
gates below provide coverage after the last scoped corrections.

The real frontend form adapter initially dropped owner metadata in both
protocols and dropped protected-empty-HTTP authentication. Schema changes retain
those values through validation and wire serialization; the three-file scoped
run passed all 57 tests in 3.03 s (`password-owner-form-green.log`). This proves
metadata preservation, not a completed owner selection interface.

Two preliminary full gate retries failed in the unchanged AmneziaWG
`TestPortForwardSetReconcileOpensAndClosesListeners`: binding fixed TCP port
58910 returned address-in-use. Failed logs are preserved as
`password-owner-port-collision-verify.log` and
`password-owner-port-collision-replay-verify.log`. No test assertion or
production listener was changed. An isolated package replay with the exact
failed shuffle seed and real core fixture passed in 7.506 s
(`password-owner-port-fixture-isolation.log`); no persistent listener appeared
in the subsequent socket inspection. The source of those transient collisions
has not been established. The final gate records that port's changes in
`password-owner-port-monitor.log`.

Full `make verify` passed in 792.68 s, including all root Go packages and the
explicit AmneziaWG device package, linters, generation freshness, formatting,
type checking, worker freshness, frontend/panel builds and Storybook build.
The frontend run passed 186 files and 1828 tests in 483.53 s. The unchanged
AmneziaWG socket package passed in this full run; monitored port changes did
not establish the source of the preceding transient collisions.
Full `make race` passed in 800.71 s, including all root packages with the actual
capability fixtures and the explicit AmneziaWG device package. Service race
tests passed in 357.574 s, adapter race in 22.448 s and device race in 14.303 s.
Results are recorded in `password-owner-gate-results.json`; earlier failed
results remain preserved. The latest scoped PostgreSQL tests also ran under
race against the real server; CI requires explicit named PASS evidence.
No core source changed in Task 5B1, so the preceding native full-core/race
checkpoint remains applicable. Owner UI, canonical runtime generation, grouped
credential hot changes, generic client lifecycle and legacy username-counter
handoff remain unfinished.

## Canonical password runtime configuration checkpoint

Task 5B2 first ran its configuration regressions against the unchanged
unsupported-adapter gate: owned Mixed/HTTP, disabled/empty listeners and the
real generated listener test failed (`password-config-red.log`, 0.943 s).
The implementation binds account-specific user/pass to canonical UUID/email
from the same SQL snapshot, includes every linked owner's policy and removes
disabled owners' credentials without opening anonymous access. Initial real
core and configuration race tests passed in 4.934 s.

A restored-data regression then reproduced deletion of the last owned HTTP
account opening anonymous access when the saved required-auth marker was
omitted or false (`password-config-restored-http-red.log`, 0.656 s). Preserving
the native protection implicit in the prior owned account list fixes both
cases; ordinary unowned legacy removals retain their old behavior. The combined
owner/configuration race tests passed in 13.132 s after that fix.

The final focused run includes all ten new configuration tests, owner
persistence tests and existing `TestClientPolicyConfig` cases. SQLite passed
in 21.275 s and real PostgreSQL in 132.336 s, both under race and shuffle.
`password-config-database-results.json` records their logs and command wall
times. The PostgreSQL run includes the existing competing remote-attachment
row-lock regression. Each new test has an explicit named PASS in both logs;
custom-core CI now requires those PASS lines, including the real-core case.
The workflow YAML parses and all 29 Bash run blocks pass `bash -n`; no remote
CI execution is claimed.

`TestPasswordProxyConfigFeedsRealSharedLedger` uses the actual SQL-to-compiler
output with independent SOCKS5 and wire HTTP CONNECT clients, Mixed fallback,
standalone HTTP, Tunnel and a separately counted echo target. Seven connections
deliver exactly 42 bytes to the target. The first owner's aliases and Tunnel
share 30 upload/30 download bytes, preserving a historical 100/200 seed and
settling 390 billed bytes at multiplier 1.5. A second owner settles 12/12/36;
wrong, unknown and disabled credentials reach no target, and the disabled
owner's ledger remains zero. This new test covers TCP/CONNECT; the preceding
native UDP/plain-HTTP checkpoint remains its separate evidence.

Snapshot reassignment commits new credentials and membership while compilation
is reading. The candidate must contain a coherent original or replacement
owner/credential pair; the next compile contains the replacement. Other cases
reject stale membership before policy preparation, unowned credentials and
anonymous resources, and normalize native case/alias precedence while removing
dormant users from runtime JSON. Stored resource settings and shared canonical
credentials remain unchanged by compilation.

One independent read-only review found no Critical or Important issue. Its
minor diagnostic correction states that owned credentials require managed
activation; the unmanaged guard itself remains. Grouped hot changes, live
legacy username-counter handoff, owner UI and generic lifecycle remain open.

Final generation freshness, Go lint and vet passed. `make test-go` passed all
root packages and the explicit AmneziaWG device package in 268.85 s wall time;
service tests passed in 110.598 s. The full affected service/adapter/Runtime
race run passed in 410.36 s wall time, with service race in 360.258 s.
`password-config-gate-results.json` records each command, exit status and log.
The preliminary lint run found a De Morgan expression style issue in the
snapshot assertion; equivalent named booleans corrected it, and the failed
log/results were preserved. No production behavior or assertion was relaxed.
No core/frontend source changed in Task 5B2, so Task 5A native core and Task
5B1 full frontend evidence remain applicable; this increment reruns the full
root Go tests and all affected race packages rather than claiming fresh
whole-core/frontend executions.

## Grouped managed password credential checkpoint

Task 5B3 begins with the unchanged legacy diff and typed AddUser path. Group
rotation, alias reorder and listener-level assertions observed RED in 0.077 s;
additional transfer/removal/capability regressions observed RED in 0.062 s.
The actual saved-inbound rotation test then reproduced an unrelated owner's
stream closing on both Mixed and HTTP (0.972 s). A separate managed diff fixes
these cases while preserving the legacy ComputeHotDiff and SOCKS restart guard.
Initial unit race passed in 1.311 s; real TCP and existing managed hot tests
passed in 6.302 s. Logs use the `password-hot-` prefix in task-evidence.

Changed owners are removed once by canonical email and all remaining aliases
are re-added in deterministic order. Reorder-only edits make no handler calls.
All removed stable identities resolve before mutation; remove-only operations
negotiate protocol identity, revocation and inbound-close capabilities before
preparation. Actual protobuf assertions preserve listener level 7 and reject
negative/fractional/overflow/boolean/string levels before handler writes.

`TestPasswordProxyHotChangesPreserveSiblingAndLedger` uses ordinary persisted
AddInbound/UpdateInbound calls and real managed sockets. Independent SOCKS5,
wire CONNECT, manually encoded SOCKS UDP and echo target counts cover active
and idle authenticated sockets, two owner aliases, another owner on the same
IP and the first owner's Tunnel. Rotation closes all old credential-group
TCP/UDP connections, releases UDP ports, rejects old authentication and admits
both remaining aliases. Other-owner TCP/UDP and Tunnel continue on the same
BootID. Final account removal retains password protection.

Mixed delivers exactly 120 target payload bytes: owner A settles upload 166,
download 266 and billed 564 after preserving history 100/200 at multiplier 2;
owner B settles 54/54/162 at multiplier 1.5. HTTP delivers 66 target bytes:
A settles 148/248/492 and B 18/18/54. UDP framing is excluded from payload
accounting. Existing native plain-request tests remain separate evidence.

`TestPasswordProxyHotPartialFailureStopsAndRecovers` proxies the test-owned
private control socket and discards the third AlterInbound acknowledgement
after the real core executed it. The save returns failure and stops the core;
the process does not acknowledge the incomplete candidate. Restart uses saved
credentials, rejects the old password and preserves usage: final A totals are
124/224/396 including independently counted traffic before and after failure.
The successful-change guarantee of retained sibling/Tunnel sessions does not
extend to this whole-core failure boundary.

Final SQLite race/shuffle explicitly passes all eleven new test names (adapter
1.249 s, service 7.398 s). Actual PostgreSQL hot and existing managed hot race
tests pass in 24.016 s, including all three new service cases. CI requires the
same named PASS lines; YAML and all 30 Bash blocks parse. No remote CI run is
claimed. One read-only review found zero Critical, Important or Minor findings.

Generation (0.84 s), lint (62.74 s) and vet (9.01 s) pass. Full `make test-go`
passes in 276.8 s wall time, including service 115.053 s and the explicit
AmneziaWG device package. Full affected race/shuffle passes in 401.27 s wall
time: service 365.389 s, adapter 22.906 s and Runtime 3.882 s.
`password-hot-gate-results.json` records each command, exit and log. Two test
fixture errors are preserved: incorrect int64 expectation types, and a control
directory failing the production private-directory requirement. The corrected
fixture uses a private os.MkdirTemp directory; no production guard was relaxed.
No core/frontend source changed. Their preceding verified evidence remains
applicable; no new full-core/frontend execution is claimed. Owner UI, generic
lifecycle and live legacy username-counter handoff remain open.

## Password account owner form and preservation checkpoint

The existing Mixed/HTTP account editor selects canonical owner UUIDs from the
paged clients API, with searched choices and invalidation below the clients
query root. Each resource keeps its username/password. Actual modal tests save
two aliases, reject partial ownership and owned Mixed noauth, preserve dormant
ownership across auth changes, and cover remote/noauth read-only behavior,
pagination, search, query failure/retry and standalone-client invalidation.
Persisted UUIDs and known labels survive search results that omit the owner.
Chinese/English help is translated and all locale key sets remain complete.

The original five modal/schema regressions failed before implementation
(11.29 s). One independent read-only review found the saved owner's label was
lost after an excluding search without a new selection. Its actual-modal
regression failed (12.23 s); the corrected guarded derived-state cache passes
React lint without exceptions. Final focused frontend checks pass: lint
1.21 s, format 0.53 s, typecheck 9.88 s, and 24 owner/Tunnel/locale tests
22.91 s. The reviewer had no other important or critical finding.

Real controller add/update/get/export/import tests cover both protocols,
unchanged canonical credentials/policy/history, rotated resource credentials,
alias reassignment and atomic rejection of unknown/remote owners, clients
mirrors and supplied traffic. Unknown-owner import directly asserts the
ownership error and unchanged inbound count. Single-inbound import resolves
existing destination owners; foreign identity remapping remains unimplemented.
SQLite backup/dump/restore and actual PostgreSQL migration/export/dump/restore
preserve canonical UUIDs, all aliases/memberships, disabled state, policy,
legacy traffic and the stable-ID ledger. Final shuffled race contract runs
pass on SQLite (15.25 s) and PostgreSQL (15.99 s), requiring named PASS results.

The final source-frozen default `make verify` passed generation, linters,
format, types, MSW freshness and all Go tests (service 119.213 s), plus the
explicit AmneziaWG device package. The frontend run passed 1841/1842 tests;
one existing client-policy form test exceeded its unchanged 5 s limit.
All twelve tests in that file passed when repeated in isolation (31.41 s).
The full suite passes with the installed CLI's `--maxWorkers=1`: 187 files,
1842 tests, 502.50 s wall time (Vitest duration 499.38 s). Repository concurrency,
timeouts and assertions are unchanged. Frontend/panel/Storybook builds pass
24.11 s, vet 3.38 s, and the full affected shuffled race packages pass 107.06 s:
database 80.959 s and controller 19.600 s. Combined with the source-frozen
pre-frontend gate stages, every required constituent passes; default
`make verify` itself remains a failed run. Final production source hashes match
the reviewed version. CI YAML and all 31 Bash blocks parse; no remote CI run is
claimed. Evidence is under `/root/task-evidence/password-owner-ui-`, including
`default-workers-gate-results.json`, `serial-gate-results.json`,
`reviewed-contracts.json` and `review.json`.

Earlier failed runs remain in the evidence directory: initial concurrent
frontend/source-change run, effect-cache lint rejection and source-frozen
default-worker timeout. Default, isolated and serial runs have different observed timings; the exact
source of each timeout has not been proven. Clean-source artifact provenance is recorded separately in deployment.md. Generic lifecycle, live legacy alias-counter
handoff, anonymous ownership and portable import remapping remain open.


## Canonical password owner removal checkpoint

Task 5B5 covers ClientService single/bulk Delete and Detach for owned local
Mixed/HTTP resources. Eight database cases remove both A aliases by canonical
UUID, preserve disabled B and its metadata, retain independent canonical
credentials/policy/history/ledger, and check memberships, durable tombstones,
keepTraffic, protected empty authentication and repeated detach. A B username
matching A's canonical email verifies that resource labels do not select owners.
The entrypoint regressions observed RED before implementation. Scope cases
observed RED for all four public operations, including filtered detach; late
SQL membership failure rolls back settings, links and history together.

Final-deletion interleavings add real saved aliases after the per-resource
fanout. The final locked recheck retains the canonical record for acknowledged
retry instead of leaving an ownerless credential. Actual PostgreSQL holds the
inbound row while rotating B and adding another A alias: removal waits for the
lock, removes all current A aliases and preserves the latest B fields.
Missing links cannot hide explicit owner references. Numeric password fields,
case aliases, conflicting arrays, dormant users and malformed account objects
are rejected before mutation when they reference the selected owner.

One read-only reviewer found two Important issues: single operations looked
up a mutable email again, and typed credential failures could hide owner UUIDs
when links were missing. Both failures were reproduced in four RED cases;
captured canonical records, independently extracted references and final
UUID/email/subscription snapshot fences resolve them. The same reviewer
confirmed both corrections with no new material issues. Rename interleavings
verify that replacement-owner traffic is preserved and current-identity retry
succeeds. Final source hashes are recorded in reviewed-source.json.

Eight real-core cases exercise both protocols through all four public
operations. Removed A aliases close active TCP, idle authenticated SOCKS and
active/idle Mixed UDP. B TCP/UDP on the same IP survive; A Tunnel survives
detach and closes on global deletion. Successful removals retain the core boot.
Independent echo targets count payload bytes, excluding SOCKS UDP framing:

| Case | Target payload | A upload/download/billed, including history | B upload/download/billed |
| --- | ---: | --- | --- |
| HTTP detach, single/bulk | 36 | 124 / 224 / 396 | 12 / 12 / 36 |
| HTTP delete, single/bulk | 30 | 118 / 218 / 372 | 12 / 12 / 36 |
| Mixed detach, single/bulk | 54 | 130 / 230 / 420 | 24 / 24 / 72 |
| Mixed delete, single/bulk | 48 | 124 / 224 / 396 | 24 / 24 / 72 |

A starts with history 100/200/300 at multiplier 2; B uses multiplier 1.5.
Deleted A usage remains correct across an actual restart. Both removed
credentials and anonymous HTTP are denied; final owner removal leaks no target
payload. These are scoped loopback protocol tests, not external-client parity.

The lost-acknowledgement test discards the first AlterInbound response after
the real core executed removal. The public detach fails, saved aliases/links
remain removed and the unacknowledged core stops. Recovery starts a new boot,
retains the reusable canonical identity and Tunnel, rejects old credentials
and anonymous access, and permits idempotent retry. Independently counted
18 target bytes settle exactly A 118/218/372 without replay. Whole-core stop
at this failure boundary closes unrelated flows too.

Final race/shuffle contract sets, including previous permanent-deletion and
Tunnel interleavings, pass on SQLite (58.66 s wall) and actual PostgreSQL
(182.13 s wall; service 178.725 s). All eight required new top-level tests
explicitly pass on both; the PostgreSQL row-lock case explicitly passes there
and is intentionally skipped on SQLite. Logs and command results use the
/root/task-evidence/password-owner-removal- prefix. Pre-review-fix runs and
original failed regressions remain preserved. CI requires the named results;
YAML and all 31 Bash blocks parse. No remote CI execution is claimed.

Shared client Update/enable/rename/quota/expiry, generic credential creation,
live legacy alias-counter handoff and foreign-owner import remain separate
unfinished work. This increment does not complete Task 5B or the overall goal.

Final generation (0.82 s), Go lint (50.68 s) and vet (6.84 s) pass.
Full make test-go passes in 278.49 s wall, including service 120.906 s and
the explicit AmneziaWG device package. Full affected race/shuffle passes
in 499.28 s wall: service 377.098 s, database 80.755 s and controller
19.495 s. Commands, exits and logs are in gate-results.json. One preliminary
lint failure required wrapping the original parser error with %w; its failed
log/results remain preserved. Both relevant error/identity paths pass again
on SQLite (26.20 s wall) and PostgreSQL (29.16 s wall) after that correction.
No ownership rule or assertion was relaxed. Production hashes are unchanged
through final gates. No core/frontend production source changed, so preceding
full native-core/frontend evidence remains applicable; no fresh whole-core or
frontend test suite execution is claimed for this increment.


### Single shared password-owner Update

The public ClientService.Update now persists canonical shared fields for local
explicit Mixed/HTTP owners, including password-only records, ordinary siblings,
valid empty-mirror Tunnel and optional filters. Resource account arrays and
specialized links retain usernames/passwords/owner UUIDs. Stable identity,
current omitted policy, raw history, lifetime billing, scalar clearing and
credential omission are covered; omitted peer PSK/keepalive retain their values.
A filtered shared rename updates excluded ordinary names while preserving their
wire credentials and current canonical credentials after partial runtime failure.
The ordinary multi-resource partial-success contract still applies.

One read-only review found one Critical and three Important integration issues.
Actual regressions reproduced destination-name acquisition with a compatible
subscription, changed-subscription acquisition, stranded global/node history,
filtered rename follow-up failure, and graph drift after entry preflight.
Additional correction checks reproduced excluded old credentials overwriting a
selected committed rotation, and actual PostgreSQL old-label acquisition after
the reuse check causing replacement history to be swept. Original RED and
fixture-tracing logs remain preserved. The same reviewer reports no remaining
Critical/Important issue or new material regression. No fresh reviewer was used.
Expanded final boundary race tests pass in 6.275 s; the PostgreSQL freed-label
correction passes in 3.063 s. Late SQL failure now targets the final shared-field
write after reservation/metadata updates and proves rollback.

The first rename reserves the unique destination and migrates local/global/node
metadata atomically. Later writes use the locked current canonical label and
retain ID-based membership; they never sweep or detach using the freed old name.
Actual PostgreSQL tests acquire the destination after the under-lock count,
covering password-only and ordinary paths. Current password rotation and omitted
policy survive a competing canonical row lock. Subscriptions remain intentionally
nonunique: changed IDs are checked in each serialized writer, but no uniqueness
or reservation against independent raw SQL after the final count is claimed.

Real core tests for both protocols use public Update for directional rates,
multiplier, name/shared credential rotation, disable/re-enable, quota lowering
and lifting, expiry, and restart. A aliases and Tunnel share lifetime usage; B
stays on the same live stream/core boot. Rename replaces A's Tunnel accounting
label and closes that flow; reconnect is tested. Rate evidence here verifies
acknowledged configured rates, not a new quantitative bandwidth measurement.
Each protocol independently receives 120 target payload bytes and settles:

| Owner | Raw upload | Raw download | Lifetime billed |
| --- | ---: | ---: | ---: |
| A | 178 | 278 | 432 |
| B | 42 | 42 | 126 |

A starts at 100/200/300 with multiplier 2, then changes to 0.5; B uses 1.5.
Wrong canonical shared passwords cannot replace resource credentials. Restart
and repeated polling do not replay lifetime usage. Lost ApplyPolicies response
after real execution makes public Update fail, saves the disable command and
stops the unacknowledged core. New-boot recovery denies A/anonymous access,
permits idempotent retry/re-enable, and settles exactly 24 target bytes to A
124/224/396. Whole-core stop at this uncertainty boundary closes B too.

Earlier passing full gates and intermediate contracts are preserved with
pre-review-fix and intermediate-review-fix prefixes. A corrected-source
PostgreSQL contract run failed only because its fixture compared a pre-Attach
record timestamp; the attached record's current fields/identity were unchanged.
The failure is preserved under fixture-error. Reading the post-Attach canonical
snapshot fixes the fixture; the actual PostgreSQL destination test then passes
twice (9.349 s total). No production behavior or assertion was relaxed.

Final corrected-source race/shuffle contracts pass on SQLite (46.75 s wall;
service 43.269 s) and PostgreSQL (205.03 s wall; service 200.921 s). All 15
required non-PostgreSQL tests explicitly pass on both; all 18 names explicitly
pass on PostgreSQL, including rotation,
destination reservation and freed-label interleavings. YAML and all 31 Bash
blocks parse. Named-PASS checks and final production hashes are recorded in
password-owner-update-named-contract-results.json and reviewed-source.json.
No remote CI run is claimed. Bulk enable/by-email writers, live
legacy alias-counter handoff, anonymous ownership, foreign-owner remapping and
full protocol/single-core migration remain unfinished.

Final generation (0.98 s), Go lint (72.79 s, 0 issues), vet (9.57 s) and
full make test-go (307.56 s) pass, including service 129.904 s and explicit
AmneziaWG device 1.495 s. Full affected race/shuffle passes in 555.38 s wall;
package timings are recorded in affected-race.log. The production hashes
remain unchanged through all final gates. The post-Attach test-fixture snapshot
correction is included in the final full affected race and both final database
contracts; full root production tests passed before that test-only correction.
No core or frontend production source changed. Prior native-core/frontend
suite evidence remains applicable; no fresh full native-core/frontend test
suite or remote CI execution is claimed. Clean checkpoint builds are separate
artifact verification and do not imply deployment or whole-goal completion.


### Password-owner shared fields and bulk enable

Public enable read/set/toggle, IP limit, expiry, integer-GiB quota and bulk enable
are covered for explicit local Mixed/HTTP owners: password-only, ordinary sibling
and sole-owner empty Tunnel graphs. The initial public fixture failure was a
duplicate traffic row; the corrected fixture then reproduced 36 failing cases
out of 54. Narrow current-field writes preserve unrelated canonical values,
current per-resource credentials, account arrays, links, counters and lifetime
ledger. A matching saved enable reconciles pending managed intent. Legacy
ordinary/remote fanout retains its result contract.

Boundary tests cover preflight scope, missing/malformed accounts, ambiguous
Tunnel, current unrelated edits, late SQL rollback, recycled names and changed
membership. Six actual PostgreSQL operations wait for a held resource row and
preserve the competing password/policy/credential rotation. A 129-owner shared
graph uses two managed acknowledgement boundaries and at most ten complete graph
reads; the original implementation performed 388 complete reads and failed.
That RED and all earlier fixture/implementation failures remain in task evidence.

The initial read-only review found three Important issues: classification drift
could enter legacy writers with a stale identity; a later bulk error could discard
committed owned results; relative expiry could not settle an actual first-use
receipt because password settings lack a clients array. Classification gain/loss
and fully valid old-label replacement reproduced the first issue for all six
write entrypoints. Captured identity/membership/classification now fences both
branches, including each legacy writer transaction. Bulk retains prior Changed,
restart and skipped reports after a later error.

First-use settlement skips password account arrays, updates ordinary expiry
mirrors and accepts omitted empty Tunnel clients only with the sole selected
owner. Both protocols and all three valid graphs settle a persistent engine's
actual admitted payload receipt twice without replay. A late second Tunnel owner
is rejected with expiry and receipt/total rollback. The first corrected test
incorrectly expected the historical traffic row to gain managed usage; that
fixture failure is retained. The historical row stays 123/456, while the managed
lifetime total advances to 126/456/583. First-use, renewal and legacy atomicity
race tests pass in 7.074 s. No accounting assertion was relaxed.

Real core exercises public bulk/set disable and re-enable, quota lowering/lifting
and expiry. A aliases, active/idle TCP and Mixed UDP associations plus TCP/UDP
Tunnel close under restrictions; B survives at the same source IP/core boot.
Enable cannot override quota or expiry. Refusal is measured by actual target
payload, since a CONNECT handshake can finish before payload admission is denied.
Independent target bytes and final lifetime totals are:

| Protocol / owner | Target bytes for protocol | Raw upload | Raw download | Lifetime billed |
| --- | ---: | ---: | ---: | ---: |
| HTTP / A | 90 | 160 | 1073742084 | 1073742364 |
| HTTP / B | 90 | 30 | 30 | 90 |
| Mixed / A | 108 | 166 | 1073742090 | 1073742388 |
| Mixed / B | 108 | 42 | 42 | 126 |

Each protocol's target total is shared between its two rows. A starts from
100/1073742024/1073742124 with multiplier 2; B uses 1.5. Restart and repeated
polling retain these exact totals. Set/bulk lost ApplyPolicies replies after real
execution save disable intent, stop the uncertain child and recover on a new
boot. Retry/re-enable settles exactly 24 target bytes to A 124/224/396.

Corrected-source race/shuffle contracts pass on SQLite (86.70 s wall) and
PostgreSQL (467.51 s wall; service 463.913 s). All 18 required non-PostgreSQL
names pass on both, and all 20 names pass on PostgreSQL. The additional final
writer-fence regression passes separately in 7.666 s service time. Prior Update, bulk,
ordinary/multinode, first-use and renewal contracts are included. YAML and all
31 Bash blocks parse; no remote CI execution is claimed. Production hashes and
named results are recorded under password-owner-fields task evidence. Contracts
compiled before deletion of an unused wrapper; the execution paths are unchanged.
Final full Go/race checks compile the cleaned source. Earlier pre-review results
and the subsequent lint failures remain separately archived.

Final generation (0.98 s), lint (38.99 s, 0 issues) and vet (3.94 s) pass.
Earlier corrected lint/vet results remain archived; these final checks include
the additional PostgreSQL writer-fence test.
The same reviewer clears all three Important findings: independent SQLite
correction/public/batch/first-use tests pass in 16.954 s, and PostgreSQL late
name-reuse probes at legacy single, legacy bulk and final bulk metadata writes
all pass in 2.455 s. No remaining Critical/Important issue or new material
regression was found. The three PostgreSQL probes are now required repository
CI regression tests. Full make test-go passes in 307.17 s wall, including
service 136.801 s and the explicit AmneziaWG device package (0.997 s).
Full affected race/shuffle passes in 570.50 s wall: service 447.812 s,
database 80.654 s and controller 19.665 s. This compiles the final regression
fixtures. All five reviewed production hashes remain unchanged through final
gates. No core/frontend production source changed; preceding native-core and
frontend suite evidence remains applicable, without a fresh full native-core
or frontend suite claim. The earlier default frontend timeout/split validation
record remains unchanged. Clean builds are distinct artifact checks.
Live legacy alias-counter handoff, generic credential creation, anonymous
ownership, foreign-owner import and remaining protocol/single-core work remain
unfinished. This increment does not complete Task5B or the original goal.


### Unmatched native counter retention prerequisite

The initial behavioral regressions reproduced dropped unmatched labels and
accepted changed retry intent before implementation. Retained counters keep
exact labels, original-byte SHA-256 keys, source Process/mode/managed instance
and raw amounts without assigning ownership or creating a billing ledger.
New receipt digests bind original intent independently of later matching rows.
Old blank-digest receipts retain identity-only replay compatibility; replay
creates neither an invented digest nor historical buckets. The next genuine
sequence receives a digest. Source accounting is captured before the SQL
callback and retained across config changes and pending retries.

Tests cover callback mutation and rollback, invalid UTF-8 identities, negative
and duplicate inputs, ordering-independent digest retries, changed direction,
final flag and source metadata, int64 overflow, corrupt stored counters, label
collision, source drift, and late persistence failure. A matching row created
after classification does not adopt that delta; only later batches use it.
601 unmatched labels exercise bounded writes, failure in the third chunk,
complete rollback and exact retry. The existing 100001-known-client and late
SQL-chunk regressions remain selected. The old duplicate-label settlement
fixture conflicted with the new explicit input contract and reproduced RED;
it now uses the collector's unique final delta. A separate 1001-row test keeps
the unchanged legacy accumulator's last-duplicate-delta behavior covered.

The single read-only review found three Important boundaries, each reproduced
in root before correction: newline labels were consumed without parsing; NUL
labels stalled PostgreSQL lookup/storage; native export from a pre-receipt
database failed. Native parsing now includes exact multiline labels. Reversible
JSON string serialization in TEXT stores NUL without confusing literal escape
or base64-looking aliases; hashes remain over original bytes. PostgreSQL lookup
excludes labels that cannot exist in its client TEXT column. Missing historical
receipt and bucket tables are optional during export. Valid encoded collisions
still reject. The initial raw-label collision fixture wrote invalid JSON after
the storage change; that failure is archived, and the corrected fixture writes
a valid different original label through the model serializer.

Real Mixed/HTTP ordinary collection exercises late bucket-write failure and SQL
commit with a lost acknowledgement, followed by pending retry and new growth.
Each of four cases sends 36 independent target bytes: alice 12/12, ALICE 6/6,
newline username 6/6, NUL username 6/6 and known Tunnel 6/6. The known historical
row ends at 106/206; no managed lifetime total is invented. The fixture disables
legacy splice because download counters otherwise publish only when the copy
ends. Private socket directory/mode/length and the missing binary variable
fixture failures remain archived; skipped execution is not accepted evidence.
The existing generated managed password test verifies immutable managed source
metadata and canonical native labels while its prior lifetime ledger remains
unchanged by native SQL settlement.

Actual SQLite backup/reopen upgrades and SQLite→PostgreSQL→SQLite migrations
preserve original amounts, long/case/NUL labels and receipt bindings.
Current, missing-bucket, missing-digest and pre-receipt schemas preserve existing
users and historical receipts without modifying source schema or inventing
traffic. NUL migration and literal-escape separation pass on real PostgreSQL.
The same reviewer clears all three findings: independent migration/export
12.506 s, four PostgreSQL real-core cases 6.384 s, focused PostgreSQL race
42.116 s and independent row-lock/late-insert probe 1.663 s pass. No remaining
Critical, Important or Minor implementation finding was reported. Future bucket
ownership/consumption and live password handoff remain explicitly deferred.

All final verification gates pass. The earlier SQLite 169.14 s
PASS/PostgreSQL 328.46 s FAIL run compiled before review corrections and is
archived. Final SQLite contracts pass in 156.15 s wall with all 43 selected
names and four real-core subcases explicitly passing. PostgreSQL contracts pass
in 321.50 s wall with all 47 selected names and four real-core subcases passing.
Final generation (0.85 s), lint (54.03 s, 0 issues) and vet (18.26 s) pass.
Full make test-go passes in 362.66 s wall, including service 156.919 s and
the explicit AmneziaWG device package 1.012 s. Full affected race/shuffle passes
in 643.04 s wall across service, database, controller, Xray process and traffic
job packages; service 456.493 s and database 83.362 s. This includes final
serializer/collision fixtures and ordinary/final multiline parsing. Contracts compiled before
nonfunctional unused-variable/selector cleanup; full Go/race compiles the cleaned
source. All eight reviewed production hashes are recorded and remain unchanged
after final formatting. YAML and all 33 Bash run blocks parse; no remote CI
execution is claimed. This prerequisite does not complete Task5B or the original
single-core/protocol/install goal. No core/frontend production source changed;
preceding native-core/frontend suite evidence remains applicable without a new
full native-core/frontend suite claim. The earlier default frontend timeout and
split validation record remains unchanged. Clean builds are separate artifact
checks, with provenance recorded in deployment.md.


Task5B8B startup configuration provenance passes source acceptance checks. Actual runtime
REDs cover missing startup evidence, pending snapshots, and configuration drift
between proof preparation and installation. Before first polling, a real legacy
child now exposes canonical logical and actual-written SHA-256 digests. Changes
through SetConfig or acknowledged policy CAS invalidate stability permanently;
equivalent object formatting preserves it. Restart, failed start, managed
control-only bootstrap, unknown direct command, exact large integers, array
order and concurrent getter/config/traffic access are covered. Runtime boundary
race passes in 13.731 s.

Five SQLite SQL behavior REDs precede durable proof implementation. The original
Task5B8A nil-proof golden digest remains unchanged. Present false/true proof is
bound to receipt intent, validated and detached from callback edits. Source
digests cannot change or promote unknown evidence; stability cannot recover.
Final receipt failure rolls back source, bucket and known counter changes.
SQLite focused race passes in 3.793 s; PostgreSQL source plus real-core tests
pass in 17.971 s. Four actual Mixed/HTTP late bucket-write/lost-commit-ACK cases
preserve the old pending proof through later saved-config drift; subsequent
polling downgrades stability without changing digests. Each retains the prior
36-byte target conservation and known 106/206 result with no invented billing.

SQLite backup/reopen passes in 4.629 s. PostgreSQL-backed database tests pass in
9.573 s: current and missing-source SQLite→PostgreSQL→SQLite plus old native
export preserve authentic old receipts/buckets, unknown/false/true source rows,
and historical source schema. Missing tables gain no fabricated startup proof.
Final SQLite contracts pass in 194.06 s wall with all 60 required names;
PostgreSQL passes in 301.37 s with all 66 names. Both include eight actual
Mixed/HTTP retention/proof subcases. CI requires all 19 new top-level names as
applicable and both sets of
four real-core subcases; YAML and 33 Bash blocks parse locally, without a remote
CI claim. No eligibility/adoption gate consumes this metadata. Historical API
mutation lineage and current runtime fencing remain future handoff requirements.


The single review found one Important source bug: a committed historical
receipt without a source header could be promoted by later known proof. Actual
root RED (0.588 s) precedes the locked previous-receipt-sequence fence. The final
regression uses the authentic Task5B8A golden digest and raw bucket, rejects
promotion without writes, then preserves nil-proof growth 7/11→14/22. Same-review
independent exact-digest overlays pass on SQLite (1.099 s) and PostgreSQL
(1.484 s); six SQL contracts pass on SQLite (2.058 s)/PG (4.301 s), and PG
migration/backup/export checks pass (4.739 s). No Critical or Important finding
remains; the minor required-name count was corrected to 19. Broader API mutation
lineage, dead-source eligibility and owner adoption remain outside this increment.

All final gates pass: generation 1.02 s, lint 96.21 s (0 issues), vet 12.99 s,
full make test-go 404.37 s and affected race/shuffle 678.71 s across service,
database, controller, Xray process and traffic job. The initial formatting/static
check failure (110.58 s) and earlier compiled contract evidence remain archived.
The earlier SQLite 171.02 s run passed 59 names; PostgreSQL 309.74 s failed the
required-name audit because its binary compiled before the added historical
regression. The final runs compile the corrected source and stronger fixture.
Nine reviewed production hashes remain unchanged; ten earlier binaries were
rehashed unchanged. YAML and 33 Bash blocks parse, with 17 applicable SQLite and
19 PG new names explicitly required. No remote CI run or new complete frontend/
native-core suite is claimed; the existing default frontend timeout/split record
stands. Logical commit, fork push and clean artifact evidence follow in
deployment.md. This prerequisite is not Snell/mieru/SSH support or whole-project
completion. The user's priority correction moves native protocol work ahead of
further legacy-password refinement.
