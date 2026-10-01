# Native Snell core increment

The increment implements independently configured Snell v4, v5 and v6 inbound
and outbound handlers in the managed core. It consumes the existing canonical
UUID and Client Policy Engine; panel forms and credential migration are outside
this increment. All decoded TCP and UDP payload enters Xray Dispatcher. The
outbound uses the supplied Xray dialer and remains usable in proxy chains.

Use GPL-3.0-or-later `github.com/sagernet/sing-snell` at
`bc5a12ac736f235b2de2926ecd2791cc925e6b8c`. Its stream service supports v4/v5
through the v5 server constructor; v6 has its own shaping implementation. Preserve
the original license, immutable source manifest and complete dependency source
availability. Retain all 46 upstream files under `core/deps/sing-snell` and
record three narrow managed changes: v4/v5 UDP records accept one complete
datagram up to the real 16383-byte record limit, and v4 empty IPv4 replies accept
their seven-byte address header. Official-server first-13k-upload tests reproduce
the pristine source failures and verify these changes. The alternative OpenSnell
server performs independent dialing and
has no public v6 source, so it is unsuitable as the primary server adapter.

One authenticated PSK on an exclusive listener maps to one immutable configured
`protocol.MemoryUser` with canonical UUID/email. Ignore the wire client-ID field:
a shared PSK does not independently authenticate different wire IDs. Physical
connections are tracked from acceptance for revocation, and decoded callbacks
alone enter payload policy admission. Separate logical reuse requests have
separate metadata and lifecycle. Raw mode cannot authenticate the PSK and is
rejected. v6 supports `default` and `unshaped`; v4/v5 support none/http obfs.

The inbound uses normal native TCP workers and the library's decoded TCP/packet
callbacks. Each UDP destination receives a bounded separate Dispatcher link,
preserving domains and IPv6 and complete datagrams, including empty payloads.
There is no independent DNS/dialer. A listener closes all physical connections;
credential removal closes reuse, TCP and UDP resources. Only decoded payload is
metered, excluding framing, request/reply headers and padding. Managed paths
disable raw splice.

Outbound reusable clients are scoped by immutable credential generation,
inbound tag, supplied Xray dialer identity, resolved outbound gateway and request
socket mark. They cannot share a socket across credential replacement or routing
scope changes. Active sockets and pending dials share a 128-connection bound per
handler; there are at most 64 reuse scopes and 64 destinations in one UDP
association. Handler close and credential revocation cancel pending dials, fence
late successful dials, and release registry entries before peer-visible EOF.
Stream copy directions are joined before logical reuse completion. Xray user
level policy controls handshake, idle and half-close timeouts.

The focused review correction computes reuse idle deadlines at logical completion
and refreshes UDP idle deadlines in both directions. Native Snell opts in to
`session.Content.PreserveTCPHalfClose`; Freedom then calls `CloseWrite` only on
its direct TCP socket after normal upload EOF, allowing the target to finish its
reply and the authenticated Snell physical connection to remain reusable.
Standard Freedom behavior and the existing UDP source flag remain unchanged.
The outbound initial non-reuse request write has a core handshake write deadline
before starting the normal joined payload copies.

The public library lacks v5 QUIC Proxy Mode. Reject requested QUIC mode and do
not advertise full v5 capability. This increment delivers the TCP/UDP-over-TCP
adapter; v5 QUIC remains a separately recorded acceptance gap. Official Surge
client interoperability is distinct from source-client tests. Test-only official
ARM64 reference servers may verify the outbound wire implementation; they are
never product runtime dependencies. v6 compatibility is pinned against the
actual tested beta/RC pair and cannot be inferred from a version selector.

The v5.0.1 official fixture receives a complete first 13k UDP upload but returns
a truncated first large UDP reply. Keep that external reply limitation visible;
native-to-native tests preserve complete first 13k, empty, domain and IPv6
datagrams. Do not invent fragmentation or a warm-up workaround.

Verification observes target bytes and per-owner accounting, route rejection,
shared budgets/rates, disable/expiry/removal, unaffected siblings, UDP boundaries,
half-close/reuse and native handler cleanup. Dependency upgrades must compile and
test existing sing consumers without weakening their APIs.
