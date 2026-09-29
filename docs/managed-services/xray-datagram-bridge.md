# Authenticated UDP routing bridge plan

Continue main Task 6 inline under its existing authorization. The complete
requirements remain binding; this increment establishes a routing prerequisite,
not public mieru support or complete native-protocol policy enforcement.

## Decision and boundary evidence

The pinned core is `v1.260327.1-0.20260908222543-52a412d9e2f5` (26.9.9).
Keep the dependency and upstream baseline. Maintain a narrow, reproducible
source patch when stock packet handling cannot satisfy the promised boundary.
Never edit the module cache or silently replace a running core.

Evaluate a private, per-client authenticated Trojan stream per UDP destination.
It carries the original domain/IP and authenticated user into the existing
dispatcher; one stream per destination prevents the native UDP dispatcher from
reusing the first destination's route for a later destination. Retain the
existing SOCKS TCP bridge and its authentication readiness check.

The [official wire format](https://trojan-gfw.github.io/trojan/protocol) carries
a two-byte UDP length. The pinned implementation instead rejects payloads over
8192 bytes, drops empty payloads, and allocates only 8192 bytes for a response
including its header. Real core tests reproduce empty upload loss, 8192-byte
response failure and 8193/65507-byte upload loss; 1 and 8170 bytes pass.
The first fixture omitted an explicit permission for its loopback target and
failed even the small control. Core debug logs proved the default final rule
blocked it; the corrected fixture allows only its loopback target network.

Trojan is deprecated in this pinned core. This private bridge therefore depends
on the maintained pinned build and explicit capability checks, not continued
upstream support. Credentials are random per binding and remain loopback-only;
this is not a new public Trojan service or a replacement for TLS on public
connections. No fallback target may be configured for rejected credentials.

The official mieru UDP wrapper rejects domain names in response headers.
Preserve original domains for routing but return only actual available peer IP
metadata. Do not resolve again and guess which address a routed outbound used.
Outbounds that do not expose that metadata require an explicit protected state
until an appropriate adapter is implemented and verified.

## Steps and verification contract

1. Add real core tests before changes for empty, header-boundary and maximum
   65507-byte UDP payloads in each direction. Establish the small control.
2. Add a pinned source preparation/build command with checksum validation,
   patch provenance and distinct build identity. Fix complete packet allocation,
   packet-aware empty handling and bounded queue admission. Add upstream-level
   tests for buffer/queue behavior before the corresponding patch.
3. Verify source patches against the pinned core's affected package suites and
   the actual core data path. Add malformed/oversize framing tests. Preserve
   byte counters at zero for empty packets and avoid unbounded empty queues.
4. Implement the private packet bridge with policy-ID credentials, original
   source and target, deadlines, exclusive connection ownership and no direct
   fallback. Test user/domain/IP/network/port/tag/priority/block/egress routing,
   wrong credentials, core exit and cancellation with real requests.
5. Connect official mieru clients to that bridge and verify per-client policy,
   exact single billing and existing-flow cutoff over both native underlays.
6. Continue the model/Runtime/UI/API/export/deployment/node/backup work from
   main Task 6. Public activation must validate the required core capability
   before changing desired/actual runtime state.

Do not claim stock cores, all outbounds, IPv6, public integration or multi-node
execution are verified from a single direct IPv4 loopback result.

## Managed bridge implementation decision (2026-09-29)

Keep the SSH SOCKS bridge. Add a separate managed bridge for both stream and
packet dispatch by new adapters, retaining one public inbound tag and a private
32-byte random credential per policy-ID binding. Its core account uses the
existing email label for user routing and a separate unmetered policy level.

An explicit core `managed` setting enables the private extension. Before any
target request, the bridge sends an authenticated reserved probe containing
invalid address type zero, plus a fresh 32-byte nonce. The managed core returns a
versioned HMAC-SHA256 acknowledgement using the private account password. A
stock core rejects that address type before routing, even if an outbound has
a destination override, and cannot produce the acknowledgement. Only after
validating the acknowledgement does the same connection send its TCP/UDP
target. Readiness checks close at the acknowledgement without any outbound.
Use bounded handshake deadlines and exact reads so fragmentation cannot cause
anonymous downgrade, partial-header bypass or an unbounded wait.

UDP streams are fixed to the original target; reject a different address in a
later packet. The client adapter exposes each response's supplied IP/port via
`ReadFrom`, and the native mieru handler must use that metadata after reading
the packet. On the explicitly managed path, direct freedom packet reads retain
their actual socket peer instead of substituting a domain alias. Ordinary
inbounds retain their existing response-address semantics. Missing IP metadata
fails the managed packet path; other outbounds still require real acceptance.

First tests: real-core authenticated readiness without targets; wrong secrets,
stock-core rejection and canceled/stalled handshake; original domain routing
and actual IP reply; complete packets; independent same-IP users and source/tag/
network/port/IP/block/egress/balancer selection; official native clients through
the bridge with exact single billing and existing-flow policy enforcement.

Implemented internal path:

```text
official mieru client → native authentication → stable panel policy ID
  → shared payload admission/shaping/ledger → private per-ID core credential
  → original user/tag/source/domain/IP/port/network routing → selected outbound
  → real target; replies retain actual available IP/port → payload policy → client
```

The generated private level disables core user upload/download/online counters
and selects a finite 64 KiB pipe policy. Managed UDP response metadata must
contain an actual IP; missing metadata closes the flow. The client rejects
malformed or oversized response frames and closes their stream. All writes
carry one complete packet, including an empty packet. These internal results
do not enable a public service or select the new core in Runtime.

The panel's gRPC inbound builder now recognizes the private managed Trojan
settings, retains uint32 client levels and serializes protobuf managed field 3
from pinned patch 0002. It accepts only literal loopback listeners and explicit,
unique identities with nonempty credentials and levels; fallback or unknown
private settings are rejected. Ordinary inbounds keep the existing builder.
Actual hot-add tests preserve authenticated health, TCP/UDP payload, actual UDP
peer metadata, absent duplicate user counters and an existing core stream
through addition/removal. Both level 255 and 4294967295 are exercised.

The test core starts with the required private policy definitions. The gRPC
serializer does not install policy definitions or override the panel's decision
to restart for a policy/routing change. Successful gRPC insertion alone is not
readiness: the runtime still has to verify `ManagedBridge.Check` before opening
a public listener. Full public mieru Runtime activation remains outstanding.
