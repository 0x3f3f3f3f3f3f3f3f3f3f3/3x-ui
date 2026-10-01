# Native mieru protocol adapter

The user's central deliverable is Snell, mieru and SSH inside one Custom
Xray-core. This increment implements mieru's native inbound and outbound data
paths first. Panel forms, canonical account persistence and export remain the
following vertical integration task, not a prerequisite for protocol decoding.

Pin `github.com/enfein/mieru/v3 v3.38.0`, commit
`b961978c3be9dd26b94158487c760858e19d1db2`, retaining GPL-3.0-or-later notices.
Use its compiled protocol/mux and embedding APIs, without a mita process,
panel-side decoder, local SOCKS bridge or second router. Public server-side
`protocol.Mux.Accept` yields authenticated `apis/common.UserContext`; its
`SetServerUsers` updates authentication atomically. The adapter parses the
existing mieru SOCKS request after transport authentication and maps that
verified username to an immutable server-configured MemoryUser. Wire fields
never supply the stable client ID. Library user quotas are omitted: the Xray
Client Policy Engine is the sole quota and rate source.

The inbound owns TCP or UDP mieru transport listeners inside the core. Reuse
Xray's existing native Start/Close pattern (as used by TUN): Network returns
empty, the proxy starts its own embedded listener, and AlwaysOnInboundHandler
owns its lifecycle. Receiver listen/port and tag come from the core constructor
context. Reject unavailable ports and transport wrappers that the native
listener cannot honor; do not silently ignore stream settings. Track accepted
sessions, cancel them on listener close and terminate only the removed user's
sessions on user removal. Recheck the captured credential binding before
dispatch so removal or replacement cannot authenticate an old session anew.

Decrypted TCP payload goes to the existing Dispatcher with canonical user,
inbound tag, physical source and original target in a fresh session context.
Disable splice for managed payload. UDP payload is decoded from the library's
PacketOverStreamTunnel and SOCKS UDP framing, preserving datagram boundaries and
per-packet destinations. Dispatch each destination through bounded Xray Dispatcher links;
count payload once, excluding framing. All paths share Client Policy Engine
limits, multipliers, quota, expiry and revocation with other protocols.

The outbound uses the official public profile-built transport multiplexer and
SOCKS request/response model. The adapter owns the raw logical session during
handshake so cancellation and an absolute timer can close partial responses. Supply both stream and
packet dialers backed by the Xray internet.Dialer so the library cannot create
independent target connections. The remote mieru server is the transport peer;
destination TCP/UDP requests travel inside that authenticated transport. Preserve
packet destinations and boundaries. Pools isolate immutable authenticated
MemoryUser generations, inbound tag, supplied dialer, selected gateway and
socket mark. Logical cancellation closes only its own session; credential
revocation and core Close fence pending dials and close every owned physical
socket, including late successful UDP returns. Idle pools expire after 30s;
OFF retains no idle pool. The existing dispatched link already owns
policy accounting: never add a second admission or billing ledger on egress.

Add typed core configurations and JSON registration for `mieru`, keeping
upstream field numbers unchanged. Inbound users carry username, password,
canonical email, stable client ID and level. Outbound carries endpoint,
credentials, TCP/UDP transport and multiplexing settings. Validation rejects
duplicate/empty credentials, invalid trusted mappings, invalid endpoints and
unsupported modes before opening a listener. Add a negotiated native capability
only after actual data paths and lifecycle tests pass.

Acceptance requires real official-library client to native core and native core
to official server for TCP and UDP payloads over both TCP/UDP transports, IPv4
and IPv6/domain targets, multiplexed connections, independent users and shared
identity with Tunnel. Routing to a blocked/selected outbound must have observable
target behavior. Quota/disable/expiry must close active paths; deleting a user or
listener must close its sessions while a sibling remains live. Rate and billing
checks reuse actual payload targets and the core's durable ledger. Self tests
and official reference interoperability remain separately identified. Full panel
support, complete protocol Task8 and whole project remain open until their own
vertical acceptance passes.

Source references: [official release](https://github.com/enfein/mieru/releases/tag/v3.38.0),
[server embedding boundary](https://github.com/enfein/mieru/blob/v3.38.0/apis/server/interface.go),
[authenticated context](https://github.com/enfein/mieru/blob/v3.38.0/apis/common/user_context.go),
[atomic users](https://github.com/enfein/mieru/blob/v3.38.0/pkg/protocol/mux.go).
