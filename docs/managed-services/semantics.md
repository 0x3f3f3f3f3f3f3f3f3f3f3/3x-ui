# Precise policy and accounting semantics

This is the target contract. See validation.md before treating any item as
implemented or verified.

- Directions are from the client's perspective. Raw counters count accepted
  application payload at exactly one authenticated dispatch point. Each
  backend must declare differences before it can participate in billing.
- Rates are nonnegative integer bytes/second. Zero means unlimited. UI Mbps
  uses 1,000,000 bits/s; MB/s uses 1,000,000 bytes/s. GiB uses 1,073,741,824
  bytes; GB uses 1,000,000,000. No multiplier enters the rate calculation.
- Multiplier is a positive decimal in [0.001, 1000], at most three fractional
  digits, stored as integer milli-units; default 1000 (=1×). Reject zero,
  negatives, NaN, Infinity, exponents and excess precision. API uses an exact
  decimal string; legacy omission defaults to 1× at the model boundary.
- Raw bytes and billed whole bytes use signed 64-bit nonnegative integers.
  Billed remainder is [0,999] thousandths of a byte. Overflow is an error,
  never wrapping, silent clamping or free traffic.
- For delta N and multiplier M, add N*M milli-bytes to the carried remainder,
  extract whole billed bytes, retain the remainder. Compute with checked
  quotient/remainder arithmetic so N*M need not fit int64. This makes batch
  splitting invariant. Persist all components in the same transaction.
- Multiplier revision applies only after a settled boundary. For 10 GiB at
  1× then 5 GiB at 2×, raw total=15 GiB and billed total=20 GiB. Historical
  use is never recomputed using the current multiplier.
- An unlimited quota is zero. Otherwise allowance is evaluated against billed
  bytes AND the fractional remainder. A 1-byte quota at 0.5× allows exactly
  2 raw bytes; at 1.5× it cannot forward one whole byte under strict admission.
- A client policy ID is independent of email/IP/port and is never recycled.
  Every counter source has a persisted incarnation and monotonic sequence.
  Counter reset requires a new accepted incarnation; a smaller count in the
  same incarnation is not inferred to be a restart.
- Report replay cannot change raw counters or charges. Out-of-order reports
  cannot rewind a cursor. All source contributions are settled once by their
  billing owner; transport bridges, outbound stats and master mirrors are not
  new chargeable sources.
- Manual-disabled, expired, quota-depleted and backend-unavailable are
  independent restriction reasons. Reset/renew/increase-quota clears only the
  relevant reason. Admission and existing flows enforce their union.
- Aggregate shaping scope is immutable client across every local connection,
  channel and binding. Global multi-node caps require allocated shares, not
  one full bucket per node. Policy updates affect live waiters within 2s.
- Before throughput acceptance, fix burst/queue limits and measurement window.
  Allowed upper error is burst/window plus measurement-clock error, not an
  arbitrary percent adjusted after failure. Verify a meaningful lower bound.
- Before quota acceptance, fix reservation size, concurrency and flush/lease
  durations. Derive crash loss, cutoff delay and maximum overuse from those
  limits; independently measure TCP, UDP and racing connections. These bounds
  are not yet established for the final adapters and remain open acceptance
  items, not production guarantees.

## Implemented stream scheduler contract

The internal Limiter accepts integer rates in [0, 1 TiB/s], 0 unlimited.
Each instance belongs to one client/direction and must be shared by its
connections. Tokens cover raw bytes; billing multiplier is never an input.
For positive rate R, burst is `min(65536, max(1, floor(R/10)))` bytes; grants
are at most 65536 bytes and the pending queue contains at most 128 callers.
Queue overflow returns an explicit error, and canceling a waiter removes it.
Changing the rate wakes all waiters and preserves existing token credit capped
to the new burst; repeatedly saving the same policy cannot issue extra credit.

The scheduler uses transient floating-point time credit, independently of the
exact integer billing ledger. It retains no payload buffers. ShapedWriter
splits stream writes into bounded grants and propagates partial-write errors.
An owning adapter must cancel blocked I/O by closing its connection; canceling
the scheduler context interrupts queued waits only. Datagram admission,
authenticated client registration, durable quota admission, protocol adapters
and distributed rate-share allocation are still pending.
