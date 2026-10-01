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
availability. The alternative OpenSnell server performs independent dialing and
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

The public library lacks v5 QUIC Proxy Mode. Reject requested QUIC mode and do
not advertise full v5 capability. This increment delivers the TCP/UDP-over-TCP
adapter; v5 QUIC remains a separately recorded acceptance gap. Official Surge
client interoperability is distinct from source-client tests. Test-only official
ARM64 reference servers may verify the outbound wire implementation; they are
never product runtime dependencies. v6 compatibility is pinned against the
actual tested beta/RC pair and cannot be inferred from a version selector.

Verification observes target bytes and per-owner accounting, route rejection,
shared budgets/rates, disable/expiry/removal, unaffected siblings, UDP boundaries,
half-close/reuse and native handler cleanup. Dependency upgrades must compile and
test existing sing consumers without weakening their APIs.
