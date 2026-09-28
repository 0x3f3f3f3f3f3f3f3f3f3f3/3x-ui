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
protocol authentication adapters and distributed rate-share allocation remain
pending.

## Implemented shared TCP flow controller

The controller shares upload/download buckets and a serialized durable meter
across all bindings for one immutable client. It permits at most 128 active
flows and uses 32 KiB copy buffers and grants. Its Proxy operation has one
writer per direction; arbitrary adapters must preserve that ownership.
It checks policy before dialing and commits each accepted payload before its
write. A failed write may therefore leave accepted, billed bytes undelivered.
At most one committed grant per direction/flow awaits its write: up to 64 KiB
per Proxy flow, or 8 MiB at the 128-flow limit. Kernel, TLS and protocol buffers
are separate and must be included in each adapter's measured in-flight bound.

Active grants validate current enabled/expiry/quota state transactionally.
Idle checks run every 250ms once the last successful validation is at least
250ms old. Database operations have a 500ms context deadline; a separate
watchdog closes flows after one second without validation, checked at 250ms
intervals. Cancellation closes both stream endpoints, including blocked reads.
The tests require cutoff within 1.25s; production scheduling and each adapter's
Close behavior still need protocol-specific verification against the 2s goal.

Source takeover closes the prior admission-only incarnation. Already committed
grants remain billed; the old process receives no new grants. Metering-source
names belong to trusted runtimes, not client-provided labels. This fences
counter ownership, but does not implement distributed rate shares or node
leases. Only one controller per client/source may be installed by a runtime.

Admission inspects durability settings on its actual transaction connection.
SQLite requires WAL with FULL/EXTRA, or a rollback journal with EXTRA; memory
and disabled journals are rejected. PostgreSQL requires fsync on and a commit
mode that waits for local WAL flush. These choices follow the official
[SQLite synchronous contract](https://www.sqlite.org/pragma.html#pragma_synchronous)
and [PostgreSQL WAL contract](https://www.postgresql.org/docs/16/runtime-config-wal.html).
They depend on the underlying storage honoring sync operations. Tests cover
settings and process/controller persistence, not physical power interruption
or recovery of traffic newer than a restored historical backup.

## Implemented SSH server boundary

`internal/sshtunnel` now has a production manager through existing panel
Inbound/Client services and Runtime. Its UI/export vertical is still incomplete.
It uses public-key authentication, one stable policy ID per client
and a shared flow controller across SSH connections, channels and bindings.
Authentication/SSH framing, encryption and internal bridge bytes are excluded
from application-payload billing. The authenticated transport is tracked for
revocation but its encrypted stream is not counted a second time.

Direct TCP channels (-L/-D) preserve the requested host and port in the required
dial adapter, alongside policy ID and inbound tag. Empty target rules deny all;
explicit `*` host or zero target port permits any value in that dimension.
Only direct-tcpip channels are accepted from clients. Session channels, hence
shell/exec/PTY/SFTP, plus agent/X11 and client-originated forwarded-tcpip are
rejected. No native UDP capability is implemented or advertised.

Reverse forwarding is disabled unless the client has explicit Reverse rules.
Each rule allows an exact literal bind IP and port; reverse port zero permits
only a request for an OS-allocated port, not arbitrary client-selected ports.
IPv4/IPv6 listeners use their explicit address family. A transport may own at
most 16 reverse listeners; the server at most 256. Cancellation closes only the
owned listener. Disconnect, disable, expiry, quota and ACL/key revocation clean
up all that transport's listeners. Existing accepted channels are separately
tracked by the shared controller. A reverse target channel opens only after
policy admission, and its directions remain relative to the SSH client:
server-listener → client is download; client → server-listener is upload.

The SSH connection protocol's forwarded-tcpip message contains the bound
listener and originator, but not the client's local target. Therefore these
server-side listener permissions cannot prove a client-side -R destination
allowlist. That requirement remains unresolved; the UI/export must disclose
the limitation and must not offer uncontrolled public listeners by default.
See [RFC 4254 §7.2](https://www.rfc-editor.org/rfc/rfc4254.html#section-7.2).

The server limits authentication to 10 seconds/3 attempts, 256 transport sockets,
64 forwarding channels per transport and 256 forwarding channels per server.
SSH/channel and kernel buffers are additional to the controller's copy buffers.
Adding a key keeps valid existing sessions. Removing a key cancels sessions
authenticated by that key; changes to target/reverse ACLs cancel affected
sessions so stale grants cannot persist. Authentication is revalidated after
the signed handshake to catch revocations during authentication. Rate changes
go through the shared controller and preserve healthy existing connections.

## Implemented authenticated TCP routing bridge

`internal/routedbridge` supplies the concrete -L/-D dial adapter:
OpenSSH → authenticated SSH policy ID → shared payload admission/shaping →
private authenticated SOCKS5 entry → existing Xray rules/outbound → target.
Each entry keeps the managed inbound's tag. Its immutable binding maps policy
ID to the current client label and a separate random 256-bit credential. The
label becomes Xray's authenticated username, preserving exact and regexp user
rules without rewriting them; it never selects the billing account. Credentials
belong only to this bridge instance and are not exported to tunnel clients.

Original domains and ports pass through SOCKS without local resolution or
sniffing. IP requests remain IP requests. The SSH adapter obtains source IP/port
from the transport socket, not the client-supplied channel origin; the private
bridge carries it through PROXY protocol. Listeners must be literal loopback
addresses, authenticated, and distinct from existing configured ports/tags.
No anonymous-auth downgrade or direct fallback is permitted. Handshakes have
a five-second ceiling and close promptly on context cancellation. The caller
owns the returned connection and must attach it to the admitted policy flow.

Each bridge adds a separate unused Xray policy level, inheriting default timeout
settings but disabling user upload/download and online counters. The existing
client-level Xray counters therefore count only native ingress, even when that
same client also uses SSH. The admission ledger counts SSH application payload
once, before bridge forwarding; bridge headers and protocol framing are excluded.
Inbound/outbound operational counters may still observe the hop and are not an
additional billing source. Actual Xray tests verify these counter boundaries.

The production SSH manager now owns configuration and credential reconciliation;
see design.md for its verified scope and remaining membership-change disruption.
Panel online/IP
limits must use the authenticated adapter's transport records; the bridge's
online stats are deliberately disabled. -R has not gained a client-side target
from this bridge. Balancer, DNS-refresh, routed throughput/quota stress, global
nodes, disruption-free credential rename and complete production recovery remain open.
The bridge builds on the documented [Xray SOCKS inbound](https://xtls.github.io/en/config/inbounds/socks.html)
and pinned core source at `52a412d9e2f5` (`proxy/socks/server.go`, `infra/conf/socks.go`).

## Panel lifecycle for admission-owned accounts

Existing single-client, inbound, all-client and bulk traffic reset operations
use the durable account boundary when a client has a usage account. A reset
closes every active admission source, clears raw upload/download, billed bytes
and fractional carry, and advances the account revision once per client.
It preserves multiplier, quota, expiry and operator enable state. The canonical
client lock prevents racing grants from crossing the reset boundary. An active
observed source causes an error and rollback because its unreported bytes
cannot be inferred from the durable cursor. Bulk rollback includes earlier
clients, source closures, raw projections and group display baselines.

The flow controller fences old TCP connections within its existing 1.25-second
test bound. The SSH production manager automatically configures a fresh source
after fencing; other adapters still need equivalent reconciliation.
Reconfiguration still checks independent expiry and manual-disable restrictions.
Bytes already admitted before the reset may remain in the documented bounded
protocol/kernel buffers; resetting a counter does not retract delivered bytes.

Automatic renewal uses the existing interval/calendar/prepaid-cycle schedule,
resets the account only when the renewed expiry is in the future, and updates
canonical, traffic and attached settings expiry together. It never clears
operator disable. Legacy traffic ticks no longer compare raw totals against
billed quota for these accounts or encode their expiry/quota as manual disable.
Increasing their quota also preserves operator disable and historical billing.

A multi-attachment single-client reset creates one local boundary, retains
inbound reset timestamps and node dirty markers, then dispatches one Runtime
reset request per remote node. Remote failure is reported explicitly after the
local commit. This preserves existing dispatch behavior; it is not a distributed
atomic reset or proof of node credit/history deduplication. Durable distributed
reset events, node leases and accounting migration remain open. Production
account activation must coordinate with legacy lifecycle selection and writes,
including outer reset/auto-enable decisions currently outside the serial
writer. Those initial-activation races remain to resolve before exposing a
public activation path.


The current production SSH adapter uses the ledger default multiplier 1 and
unlimited controller rates. Persisted public policy controls are still required;
the internal backend's rate/multiplier tests do not imply those controls exist.
Delayed expiry starts on authenticated transport establishment, including a
transport that never opens a channel. Merely starting the listener does not start
the clock. SSH host keys are persisted in administrator-only inbound settings;
the generated Xray bridge credentials are written to the existing atomic 0600
core configuration file. Backups must retain the host key to preserve client pins.
