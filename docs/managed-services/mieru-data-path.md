# mieru authenticated data path implementation plan

> Execute inline with `superpowers:executing-plans` under the existing authorized
> main plan. Ordinary engineering decisions are autonomous; remaining SSH and
> other protocol requirements are not removed by this sequencing decision.

**Goal:** Carry actual official mieru clients' TCP and UDP payload through
stable client identity, shared shaping, fixed-point billing and quota admission.

**Architecture:** Embed the pinned official Go protocol multiplexer, retaining
its authenticated user context and wire format. Own accepted sessions and SOCKS
request parsing so every failure path closes its connection. Dispatch through
explicit policy-aware TCP/UDP connectors; never silently default to direct.

**Stack:** Existing Go/SQLite/PostgreSQL policy ledger and limiter;
`github.com/enfein/mieru/v3 v3.38.0`, official client API for real wire tests.

**Spec:** [Full requirements](requirements.zh-CN.md), sections III, V–VII and X;
[main plan](plan.md), Task 6; [architecture](design.md).

## Fixed source and design decisions

- Official release v3.38.0 was rechecked on 2026-09-28. Commit
  `b961978c3be9dd26b94158487c760858e19d1db2`; module checksum
  `h1:+DuixHFoCGEYEopoEg8fHmDJ5Ak30PuXuf1GIh2Xpu0=`; go.mod checksum
  `h1:zJBUCsi5rxyvHM8fjFf+GLaEl4OEjjBXr1s5F6Qd3hM=`.
- Source: [official release](https://github.com/enfein/mieru/releases/tag/v3.38.0),
  [`apis/server`](https://github.com/enfein/mieru/tree/v3.38.0/apis/server),
  [`protocol.Mux`](https://github.com/enfein/mieru/blob/v3.38.0/pkg/protocol/mux.go).
  Preserve GPL-3.0-or-later notices and dependency/source attribution.
- Native `UserContext.UserName()` supplies authenticated identity. TCP/UDP
  underlay selection is distinct from TCP/UDP destination payload; test all four
  combinations. Native rolling `User.Quotas` stay empty; panel accounting is the
  sole billing authority. Diagnostic native counters are not charged again.
- Credential replacements use an opaque native username per authentication
  generation. The native `HashedPassword` override contains the official hash
  of the external username and password, so existing official client configs
  retain their wire behavior. The opaque identity maps to the stable panel
  policy ID; it is not a new billing account or an exported username.
- `Server.UpdateClients` validates a complete replacement before publication.
  Unchanged clients retain their generation and sessions. Password, username or
  policy-ID changes and removal cancel the retired generation, close its TCP
  and UDP payload sessions and cancel pending target dials. An empty replacement
  revokes everyone without replacing listeners; re-adding creates a new
  generation. Delayed SOCKS handshakes and native cached ciphers cannot resolve
  a retired identity to a replacement user's policy.
- Retain the authenticated identity on owned TCP sockets after a logical session
  ends, so rotation also releases idle cached underlays. Send logical close
  notifications first, with a shared 100ms grace before aborting the captured
  retired sockets. Capture exact sockets before asynchronous cleanup so a later
  replacement cannot cause cleanup to close new users' connections. The shared
  UDP listener stays open throughout credential changes.
- The public server API parses requests inside `Accept` and does not return the
  accepted connection on parsing error. Use official `protocol.Mux` directly
  with an owned bounded request handler, retaining the actual wire engine.
- Native `Accept` may run before its segment worker publishes `UserName`.
  Read the bounded SOCKS handshake first, then require the authenticated user
  mapping before opening any target. Actual client tests exposed this ordering.
  Native `Read` clears its deadline after each call, so partial request reads
  explicitly retain the same five-second absolute handshake deadline.
- Native session `Close` can wait behind a backpressured TCP send lock. Retain
  ownership of the accepted TCP socket and close asynchronously; after 100ms,
  abort that exact TCP underlay if close is still pending. An underlay belongs
  to one authenticated native user; this can also retire that user's other
  multiplexed sessions on the same stalled transport. Never abort the shared
  UDP listener for one user's policy change. Closing sessions still consume
  the adapter's session slots until native cleanup finishes.
- Full shutdown stops TCP accepting first, retires and joins logical sessions,
  lets native TCP/UDP close messages finish, and finally closes physical
  listeners, sockets and the multiplexer. Accepted handlers and close workers
  are joined. Partial
  startup releases every successfully acquired owned listener.
  Native cancellation and adapter cleanup can call `Close` concurrently;
  owned transports wait for the first close operation to complete before
  returning, so an error return cannot precede actual listener release.
- UDP payload is accounted as complete datagrams, excluding mieru/SOCKS framing.
  Reject a packet that cannot fit remaining quota without sending or charging a
  prefix; a smaller packet can still consume the valid remaining allowance.
  Maximum ordinary UDP payload is 65507 bytes; zero-length datagrams still pass
  policy admission and preserve their packet boundary without invented bytes.
- Packet shaping shares the existing per-client/direction FIFO with streams.
  A whole packet may consume bounded token debt when larger than the configured
  burst; later packets/streams repay it before sending. This bounds rate excess
  by the configured burst plus one maximum datagram, rather than accumulating
  independent per-flow packet credits. Live rate changes wake existing waiters.
- Retain default unlimited rates, positive fixed-point multipliers, existing
  freshness/cutoff bounds and durable pre-admission. Failed sends may retain
  admitted payload charges as already specified for streams; no refund race.
- Bound the adapter to 256 TCP underlays and 256 accepted/closing sessions,
  the controller to 128 flows per client, and each UDP association to 16 target
  sockets with one bounded receive buffer per target. UDP target inactivity
  is 30 seconds, refreshed by uploads as well as downloads.
- The [maintained native extension](../../tools/managed-mieru/README.md) now
  reserves a session slot before native allocation: 256 total and 128 per
  authentication generation across listeners. Its four segment trees each
  hold at most 256 segments / 128 KiB, and receive staging holds at most
  64 segments / 128 KiB. Native TCP backpressure and UDP retransmission retain
  ordered payload delivery. These queue bounds exclude partial application
  reads, active protocol workers, encryption, metadata and kernel buffers;
  they are not a process RSS limit. Ready/accept queues hold at most 64
  references each. Worker cleanup clears payload and removes session metadata
  before releasing the slot; shutdown also waits for accept workers.
- Managed mode suppresses per-generation diagnostic groups because the official
  registry cannot remove them. It rejects session admission for users carrying
  native quotas in that mode; the panel ledger owns accounting and quota.
  Aggregate native diagnostics remain active.
  The official client stays unmodified. Source pin, license, reviewable patch,
  checksum validation and byte-for-byte reproduction are checked in.
- The original SOCKS bridge is TCP-only. The managed authenticated Trojan bridge
  now carries native TCP/UDP payload with stable policy identity through the
  applied Xray router, with real official-client evidence. Public management
  integration is tracked in the [integration plan](mieru-integration-plan.md);
  deployment, nodes and the remaining acceptance stay open in main Task 6.

## Review focus

Oversized datagrams must never be split. A short quota remainder must remain
usable by smaller traffic. Mixed stream/packet connections cannot each gain a
separate rate allowance. A zero-byte packet cannot bypass disable/expiry/quota.
Malformed authenticated sessions, shutdown and revoked identities must release
all backend resources without granting unmetered direct access.

## Task 1: Shared datagram shaping and durable admission

Files: `internal/clientpolicy/limiter.go`, new limiter datagram tests;
`internal/policyflow/datagram.go`, `controller.go`, new actual UDP tests.

Interfaces: `Limiter.AcquireDatagram(ctx context.Context, requested int)
(int,error)`; `Flow.DatagramWriter(direction Direction, destination io.Writer)
io.Writer`. Existing stream `Acquire` and `Writer` semantics stay intact.

- [x] RED: packet larger than burst receives one complete grant; subsequent
  stream/packet cannot bypass its bounded debt; cancellation/rate edits wake it.
- [x] RED: actual UDP echo preserves packet length and empty datagrams; exact
  bidirectional fixed-point usage; insufficient allowance sends/bills nothing,
  smaller packet still works; independent same-IP client remains usable.
- [x] Implement exact packet admission without the stream partial-grant retry,
  preserve shared limiter/meter identity and cancel exhausted active flows.
- [x] Focused and package race/regression tests; record measured bounds.

## Task 2: Official mieru authenticated listener

Files: `internal/mieru/server.go`, framing/config helpers and actual wire tests;
`go.mod`, `go.sum` fixed dependency.

Interfaces: a validated server config with explicit local listener transports,
independent username/password to policy-ID bindings, shared policy controller,
and a required destination dial callback carrying network, original host/port,
authenticated policy ID, inbound tag and actual underlay source.

- [x] RED: official client API reaches actual loopback TCP/UDP echoes over each
  underlay; two clients behind one IP retain independent counters and quota.
- [x] Implement bounded accepted-session handlers, request/reply and datagram
  framing, policy before target dispatch, empty native quotas, owned shutdown.
- [x] Verify malformed requests, bad credentials, policy revocation, existing
  UDP cutoff and same-client concurrent streams/packets with actual clients.
- [x] Run focused/race checks and record precise implemented/remaining scope.

## Task 3: Unified routing and vertical management

Continue main Task 6 with a real authenticated UDP-capable core bridge, routing
priority/user/domain/IP/network/egress tests, then existing model/Runtime/API/UI,
client export, backup, node and deployment flows. This plan does not declare
mieru complete after a loopback echo or a library test. Publish logical verified
milestones only to the already approved feature branch and verify remote SHA.

The [packet bridge](xray-datagram-bridge.md) now has a reproducible pinned-core
patch and actual direct IPv4 route/exit tests. Official mieru clients exercise
both underlays and both payload networks through it. UDP replies consume each
packet's actual IP peer via `ReadFrom`; a connected UDP socket may supply its
known `RemoteAddr`. No new DNS lookup invents the response address.

The core uses a separate user level with user traffic/online counters disabled;
the panel policy controller remains the billing owner. Real core outbound
counters independently verify the transferred payload, and enabling duplicate
core user meters makes the integration test fail. Public service selection,
Runtime reconciliation, exports, deployment and node integration remain open.

The adapter now supports live credential replacement. Real official client
tests cover unchanged users, atomic rejection of invalid replacement batches,
rotation/removal/re-addition, an empty user set, delayed authentication, reuse of
an already authenticated native transport, policy-ID reassignment with exact
separate ledger totals, idle TCP socket reclamation and pending target dial
cancellation. Backpressured rotation uses the same owned connection cleanup as
disable and shutdown. See the [validation record](validation.md) for commands,
observed failures, mutation checks and final verification results.

## Managed client export scheduling

TCP and TCP+UDP official profiles, and TCP Mihomo nodes, explicitly disable
native connection multiplexing. The per-client shaper remains shared across
all resulting connections. This avoids a new session's handshake waiting behind
another session's buffered TCP payload during shaping, without growing queues
or terminating the active flow. UDP-only exports retain native multiplexing.
See [the compatibility decision and real test](mieru-integration-plan.md#tcp-client-scheduling-compatibility).
Manually enabling TCP multiplexing retains the underlying head-of-line blocking
behavior; it is not covered by a handshake-latency guarantee.

## Public UDP rate acceptance

The production Runtime now has an official-client sustained UDP workload on
SQLite/PostgreSQL and both native underlays. Two same-IP clients each use four
associations across two inbounds. Receiver observations cover unlimited traffic,
32/64 KiB/s duplex policies, existing-flow changes within 2s, exact 2x billing
and core restart. One 2048-byte packet per direction/association is outstanding
from the baseline onward. This is a bounded-workload result; unrestricted
buffers and natural disconnect cleanup remain separate acceptance items.

Concurrent payload admission now uses the shared controller's
[bounded durable batches](semantics.md#durable-admission-batching) to amortize
small-packet database commits. Both pacing gates still apply and every payload
waits for its cursor transaction before delivery. See the
[validation record](validation.md#public-udp-payload-rates-and-durable-admission-batching-2026-09-29)
for the failed baselines, fixture correction, unchanged tolerances and negative
controls. No complete Task 6 claim follows from these scoped checks.

## Observed native session closure

The adapter now binds first-use activation, TCP proxying and UDP policy waits
to both the credential generation and the individual native session. Once the
native session ends, its child context cancels pending work and closes owned
targets. Other sessions and the shared listener keep running. The tracked
watcher exits when the handler or its native session ends.

Official-client tests cover queued shaped TCP/UDP payload on both underlays and
blocked first-use activation. Native resources and adapter presence must clear
within 2s after the official session Close returns. A new same-policy flow
synchronizes with any already-started admission transaction before the exact
stable-usage check. Committed but undelivered bytes keep their existing charge.
This evidence concerns observed authenticated session closure; silent UDP
peer loss and close frames behind full TCP queues remain separate cases.
