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

Remaining durability work: committed-event export and protected control APIs, panel DB idempotent settlement, restore/rollback fencing, multi-node budget leases, full configuration-start rollback, disk/power-loss tests and further throughput work. Runtime Snapshot combines current live usage with a durable sequence marker; it is **not** an atomic committed ledger event and must not be used as one by the panel.

Additional checks found two further baseline issues: adding the upstream `core` package to race testing failed in `testing/servers/udp/udp.go` (shared `Server.accepting` flag assignment/close); broader `go vet ./infra/conf` reports unreachable legacy reverse configuration code after its removal error. These are failures, not skipped/passed checks. Targeted `app/clientpolicy` and `testing/policy` tests in that run passed; baseline fixes and exact reruns remain separate work.

Persistence checkpoint validation: focused race tests across clientpolicy/dispatcher/protocol/conf/Tunnel passed; `go vet` of clientpolicy/dispatcher/core/testing-policy passed; full panel `make test-go` with the persistence dependency passed. These do not erase the separately recorded full-core/scenario/race/vet baseline failures.

Baseline repair checkpoint: the missing WireGuard stream-settings regression first panicked, then passed after applying empty optional settings; the actual UDP core tests now pass under race after removing the racy helper flag (socket close terminates its loop). `go vet ./infra/conf` passes after removing code that was already unreachable after the existing legacy-reverse removal error. No removed feature was re-enabled or disabled by that cleanup. Full core/scenario rerun is still pending at this checkpoint.
