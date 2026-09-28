# Identity, metering and enforcement contract

Status: runtime primitives, Tunnel admission and local durable reservations are implemented. Panel settlement, rollback fencing and multi-node budgets remain unfinished. Full intended semantics remain below.

- `client_id` is a panel-generated opaque UUID, independent of display email, protocol UUID/password/PSK, port and node. Migration assigns once and preserves old email-keyed API/statistics compatibility. Deletion/recreation gets a new identity; shared subscriptions alone are not sufficient identity evidence.
- Count decoded TCP bytes and complete UDP payloads at the admission boundary, once per business leg. Separately label IP-packet paths. A byte admitted toward a destination counts even if the remote subsequently fails; kernel retransmissions, encrypted frames, sniff replay and internal bridge copies do not count again.
- Raw upload/download and billed total are separate. Fixed point uses multiplier millionths, positive range 0.000001–1000.000000; no float input accumulation. Preserve sub-byte millionths over batches and multiplier changes. Detect arithmetic overflow before mutation. Legacy multiplier is 1 and old billed usage equals old raw usage without resetting it.
- A policy version creates an exact admission boundary: prior usage keeps the old multiplier; subsequent bytes use the new one. Lowering quota below charged use restricts immediately.
- Per-client upload/download rates are bytes/second, independently configured; zero means unlimited. UI must label Mbps conversion distinctly. Shared token buckets have explicit bounded burst. For interval T admitted bytes must be ≤ R*T+B plus documented clock granularity; old-policy admitted buffers are separately reported at updates. UDP datagrams stay intact; wait in a bounded queue or reject/drop with an explicit counter.
- Disabled, expired, quota exhausted, unavailable budget and revoked identity are independent restriction reasons. Reset/top-up/renewal only clears the applicable reason. Apply changes to existing sessions, including idle sessions and UDP/reverse listeners.
- Normal quota admission uses one atomic check/debit across both directions and all connections. Do not call periodic panel polling an enforcement mechanism. Track admitted-but-not-written bytes separately when reporting endpoint observations.

## Durability design

Panel issues versioned budgets; core durably reserves bounded portions before admitting payload. Committed raw/billed counters and event cursor are saved together, events carry core instance, epoch, sequence and policy version. On uncertain crash recovery an outstanding reservation remains spent/unavailable until reconciled, never freely reissued. Do not fabricate raw traffic for an uncertain reservation: report conservative reserved usage separately. A persistence failure closes/rejects managed sessions.

Before implementation acceptance, choose and test the exact reservation quantum, maximum outstanding reservations, crash uncertainty and overshoot bound. No claim of zero overuse is made here. Local file loss or rollback is not equivalent to a fresh account; require panel reconciliation/fencing. Restoring a panel backup must not overlap live node budgets. DB transactions atomically commit totals plus idempotency cursor; multiplier applies once in the core event, not again at node aggregation.

## Tests fixed before runtime implementation

Multipliers: 0.5, 1, 1.5, 2, 10; arbitrary split batches must produce identical totals/remainders. 10 GiB at 1 then 5 GiB at 2 bills 20 GiB. A 100 MiB quota at 2 permits at most 50 MiB admitted bidirectional raw bytes (whole datagram granularity may leave budget unused). Concurrency test contends on the final budget. Compare independent sender/receiver observations at the same boundary; echoes count both directions.

Rate measurements: 256 KiB/s and 1 MiB/s, burst 64 KiB, 10-second steady windows after a 2-second warmup; unlimited baseline must exceed 4 MiB/s or result is inconclusive. Upper bound R*T+B+1% timing allowance; healthy steady throughput at least 85% of R. Report scheduling/buffer uncertainty, startup burst and update delay separately. These are initial gates, not numbers to loosen after failures.

## Current execution boundary and limitation

Dispatcher wraps the routed decoded payload after sniff caching. Upload admission occurs before handing buffers to the outbound; download admission occurs before writing each buffer to the client. Quota exhaustion cancels every registered session, including idle connections. This can discard the final admitted in-flight buffers: the 100 MiB/2× echo test observed 8192 bytes admitted but not delivered to the receiving application. No claim of delivered-byte-perfect accounting is made. The admitted budget was not exceeded. Configured core traffic now uses durable reservations; the whole task 4 contract is not complete and installation/upgrade integration remains pending.

## Implemented local reservation protocol

The execution store uses MIT-licensed bbolt v1.5.0 with synchronous transactions, an exclusive process lock, private file permissions and a 256 MiB file bound. The state holds panel-issued policies, exact checkpointed directional counters/fraction, persistent revocation tombstones, frozen uncertain bytes, reservation ceiling and a monotonically committed sequence. A stable store instance ID is checked on open; each successful open atomically increments its epoch. Missing state is an error; initialization is a separate explicit action, never an automatic response to missing/corrupt data.

Per client, at most one 65,536-raw-byte reservation is outstanding across upload and download combined. Its billable ceiling is committed before any bytes in that block are admitted. Near quota, the reserved raw amount is reduced to the affordable whole-byte budget. Payload admission may not exceed that quantum. A reservation refill checkpoints exact prior usage, then reserves the next block in the same transaction. A policy/multiplier update atomically checkpoints prior usage and releases unused reservation before applying the new version. Batch updates validate every member before one database transaction. Permanent client-ID tombstones prevent reuse after restart.

On clean checkpoint/shutdown the exact admitted counters are committed and unused reservation released. On abrupt restart, the prior outstanding billable ceiling becomes frozen `uncertainBytes`; checkpointed raw counters remain unchanged. The ceiling rounds upward by less than one billed byte, independently reported from confirmed billing. Maximum additional unavailable budget per client per abrupt exit is `ceil(65536 × multiplier + prior fractional remainder)` billed bytes, capped by the last reservation's affordable amount. At multiplier 2 this is at most 131,072 bytes when prior remainder is zero. Repeated restart without new admission creates no new reservation. Full/ambiguous I/O failure closes all managed sessions and blocks further policy/data admission; recovery keeps possibly committed reservations frozen.

This bounds **unavailable budget**, not confirmed delivered traffic. Committed reservations prevent reissuing admitted quota after the tested process crashes, assuming the store and its filesystem durability contract remain intact. File loss, administrator rollback, cloned state or unfenced panel restore are separate failure classes that still require the panel authority and external fencing. No universal zero-overuse or power-loss guarantee is claimed.

Normal reservation commits are amortized over raw traffic, not performed per byte. The 131,073 one-byte test performs three reservations; local measured unlimited throughput is ~29–34 MB/s with the 64 KiB quantum, much lower than memory-only operation. Larger quanta/group commit need explicit crash-budget tradeoffs and new measurements before acceptance.

Committed cumulative records are now available through protected API v1; see [control-api.md](control-api.md). The panel must still implement transactional cursor/totals settlement before these records become its reliable ledger.
