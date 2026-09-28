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
| Frontend full tests | `npm test` | failed: 16 timeouts in 10 files, 1726 tests passed, 2 teardown errors; concurrent load present; isolated rerun pending |
| Panel build | `go build -o build/x-ui .` | passed |
| Custom core build | `bash tools/build-custom-core.sh` | passed; distinctive Custom Xray-core 26.9.9-custom.1 version and source stamp, SHA256 output |
| Core full suite | `go test -shuffle=on -count=1 ./...` | failed in testing/scenarios: WireGuard nil MemoryStreamConfig panic, then timeout; upstream reproduction still pending |
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

`python3 tools/test-custom-tunnel-rates.py build/custom-xray` passed all six cases. Each starts a separate actual core process and two TCP connections sharing one client. Upload and download are measured separately at the receiving endpoint; exact machine-readable observations are [tunnel-rates.jsonl](evidence/tunnel-rates.jsonl).

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
