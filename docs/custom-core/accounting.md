# Identity, metering and enforcement contract

Status: in-memory execution primitives and Tunnel admission are implemented; durability and panel integration are not implemented. Full intended semantics remain below.

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

Dispatcher wraps the routed decoded payload after sniff caching. Upload admission occurs before handing buffers to the outbound; download admission occurs before writing each buffer to the client. Quota exhaustion cancels every registered session, including idle connections. This can discard the final admitted in-flight buffers: the 100 MiB/2× echo test observed 8192 bytes admitted but not delivered to the receiving application. No claim of delivered-byte-perfect accounting is made. The admitted budget was not exceeded. The core currently holds usage only in memory; restart persistence remains task 4 and must be completed before installation/upgrade integration.
