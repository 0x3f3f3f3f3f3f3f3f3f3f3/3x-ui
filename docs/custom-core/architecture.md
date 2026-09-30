# Custom core architecture and decisions

Status: design and source audit, not a deployment claim. Full acceptance is [requirements.md](requirements.md).

## Baseline and ownership

Work is isolated on a new branch from fork main. A managed `core/xray/` source directory retains upstream files, license and commit provenance. A root Go module replacement makes panel protobuf/config imports resolve to this source. This is preferable to module-cache editing (not reproducible) or a submodule in an unapproved additional remote repository. A patch-only representation would be smaller but harder to review/build offline.

The panel remains the only configuration authority and long-term ledger. Custom core owns authenticated identity binding, session registry, two directional per-client schedulers, metering and budget enforcement. gRPC extends existing control interfaces without renumbering any old fields. Protocol libraries may own handshake/encryption/transport but not independently select or dial ultimate destinations.

## Data flow

Authenticated account / exclusive trusted listener → stable client ID → per-session metadata → decoded payload policy admission → Xray Dispatcher/Router/DNS/balancer → selected outbound. Download payload crosses the same engine in reverse. Original and effective destination are retained. Control/health traffic uses separate unmanaged internal identities and is not billed as a user. A configured managed identity without an installed policy must fail closed.

The policy engine is instance-scoped, not a process-global singleton. All credentials and listeners owned by a client share its state. Policy updates wake blocked admission, re-evaluate active sessions and close affected TCP/channel/UDP resources outside engine locks. Registry cleanup must be idempotent. Admission wrappers must not expose zero-copy interfaces that bypass metering; explicitly guard raw-copy/XTLS decisions for managed sessions. Mux physical framing is not billed in addition to decoded child payload.

## Tunnel audit at v26.9.9

`infra/conf/xray.go` registers both `tunnel` and `dokodemo-door` to `DokodemoConfig`. `infra/conf/dokodemo.go` accepts `allowedNetwork`, `rewriteAddress`, `rewritePort`, `portMap`, `followRedirect`, `userLevel`; legacy `network`, `address`, `port` overwrite the respective new fields if present. Generated configuration will use one schema, not duplicate aliases.

`proxy/dokodemo/dokodemo.go` rewrites targets, creates an anonymous `MemoryUser` containing only Level, sets `CanSpliceCopy=1`, then calls `Dispatcher.DispatchLink`. TCP uses stream readers/writers; UDP uses packet readers/sequential writers and has a distinct transparent-forwarding branch. This proves existing userLevel is not a client identity. Required extensions: trusted client ID/legacy email, listener ACL and lifecycle binding, common runtime policy, and a managed fast-path guard. Default forwarding is explicit userspace L4, without transparent redirect/NAT or claimed source-address preservation.

## Creating clients before listeners

Canonical clients may be created before any listener exists. Empty or omitted
`inboundIds` on the existing client create API produce a new server-generated
stable UUID in one SQL transaction, without protocol credentials, traffic rows
or runtime activation. Duplicate email or subscription identity is an error,
never an implicit update. Tunnel owner selection subsequently links that same
canonical account; policy activation follows the existing local attachment path.

## Local Tunnel source access

A nonempty `settings.allowedSourceCidrs` admits only the physical TCP/UDP peer
before target selection, identity assignment, dispatch or payload accounting.
The canonical listener owner remains the billing identity; an allowed source
never becomes a client identity. The list is limited to 256 native IPv4/IPv6
CIDRs. Empty, omitted or null means unrestricted. IPv4-mapped IPv6 prefixes are
rejected; observed mapped IPv4 peers are unwrapped before membership checks.

The first implementation supports raw TCP/UDP and ordinary TCP TLS, including
its normal no-op header. It rejects PROXY protocol, non-raw transports, other
security/header wrappers, transport masks and Unix listeners whenever the ACL
is nonempty. Both JSON configuration and typed handler construction validate
these constraints. Configuration without the ACL retains legacy transport modes.

The panel accepts only the exact `allowedSourceCidrs` spelling to avoid
case-insensitive core JSON decoding and map reordering producing different
policies. Create/update validate before committing. An enabled ACL requires a
local Tunnel with exactly one canonical owner; startup also validates restored
rows. Last-owner detach/delete disables the listener while preserving its ACL,
including final canonical memberships added during deletion fanout. Runtime
reconciliation releases its ports after committing and revoking the identity.
Re-enable rechecks ownership in the write transaction.

`tunnel-source-acl-v1` is required independently of the trusted-owner capability
before startup preparation or hot handler mutations. ACL edits replace only the
affected handler, draining existing TCP/UDP flows. Other listeners remain live.
This increment does not claim source-address preservation, transparent NAT,
all transport wrappers or complete forwarding-mode/routing UI coverage.

## Local Tunnel outbound selection

`settings.outboundTag` is a custom field, carried as additive Tunnel protobuf
field 11. Empty, null or omitted selects normal Xray routing, including balancers.
A nonempty tag selects one concrete outbound through the existing dispatcher's
forced-detour context. That choice is consumed on the first dispatch: a selected
loopback then uses normal routing for its next hop. A missing selected handler
closes the link; it never selects the default handler. Unsupported outbound
network types retain that outbound's explicit failure behavior.

The panel validates the exact field spelling and string type. Enabled fixed
selections require one canonical local owner and one available template or active
subscription outbound. Creation, editing and re-enable validate transactionally.
An outbound removed later leaves the saved tag intact; traffic fails when the
corresponding configuration is applied, and re-enable refuses the unavailable
selection. Startup validates restored canonical ownership.

The existing Tunnel form shares the outbound configuration cache, lists concrete
tags, preserves unavailable saved choices visibly, and clears to normal routing.
Remote selection is read-only until remote managed policy support exists.
Changing or clearing a selection replaces only the listener, draining its old
TCP connections and UDP sessions without restarting the core or closing sibling
listeners. Stable owner identity and cumulative accounting are retained.

`tunnel-fixed-outbound-v1` is negotiated independently before startup preparation
and hot handler mutation. An older core cannot silently discard the new field.
An owned or explicitly selected Tunnel remains an opaque L4 forwarder even when
its configured target is the internal `v1.mux.cool` address; the mux wrapper passes
it to Dispatcher instead of interpreting the payload as internal channels. A
legacy Tunnel with neither owner nor fixed selection retains the upstream internal
mux gateway. Authenticated protocol mux paths retain their existing behavior and
shared policy enforcement.

## Existing paths requiring migration

### Planned Mixed/SOCKS and HTTP identity increment

This is the next Task 5 increment, not an implemented protocol claim. Existing
password accounts gain optional server-configured `clientId` and canonical
`email`. Username/password authentication returns an immutable `MemoryUser`
with the typed authenticated account and stable ID. A wire username never acts
as a client ID. Legacy accounts retain username-based statistics; anonymous
SOCKS/HTTP and SOCKS4 retain their existing behavior without a managed identity.
Managed identity on no-auth SOCKS is rejected. Anonymous resource ownership,
panel account-to-client binding/migration and remote/global policy remain later
work; the managed compiler must continue to reject unsupported activation.

Keep the existing protobuf `accounts` credential maps and all field numbers.
Add SOCKS fields 7 `client_ids` and 8 `account_emails`; HTTP fields 5
`client_ids`, 6 `account_emails` and 7 `require_authentication`. The latter keeps
an intentionally password-protected empty HTTP listener closed. Omitted legacy
settings preserve their old behavior. Metadata must resolve to an existing
credential, and managed duplicate usernames are rejected before construction.
HTTP's required-auth mode cannot turn anonymous after deleting its last user.
Changing a no-auth listener's mode requires configuration replacement rather
than an AddUser RPC silently changing authentication for existing streams.

A shared username/password validator holds immutable credential objects behind
a mutex. Authentication is a direct username lookup; removal removes both
indexes, then revokes the credential outside the lock. SOCKS and its built-in
HTTP fallback share this validator, so deleting either account view cannot leave
another protocol path active. Native UserManager CRUD/list APIs use typed SOCKS
or HTTP accounts. Closing a handler revokes all accounts and prevents later adds.
`trusted-socks-client-id-v1` and `trusted-http-client-id-v1` are advertised only
with the complete corresponding adapter; HTTP's empty required-auth setting
also requires the latter capability before handler/startup mutation.

SOCKS UDP retains the selected core's per-authenticated-TCP ephemeral listener.
It inherits the exact authenticated user, not a source-IP lookup. Credential
tracking covers the associated TCP connection before the first datagram, and
closing it must release its UDP listener safely even during timer setup. UDP
payload still passes through the existing Dispatcher and common policy engine.

Each HTTP request receives a separate inbound context and a connection lease.
An active request may close its physical connection on quota/disable/revocation;
after its response completes, the lease becomes inactive. Thus delayed cleanup
of user A's upstream cannot close a later user B request/CONNECT on the same
keep-alive client socket. Plain HTTP accounting retains the existing dispatched
HTTP-message boundary (serialized request/response headers plus body); CONNECT
and SOCKS meter the target payload. No target dialer is added outside Xray.

Acceptance uses independent standard SOCKS5/HTTP clients, observable targets,
exact shared totals with Tunnel, wrong credentials, deletion/re-add, idle and
active UDP association cleanup, same-IP different users, last-account HTTP
refusal, ordinary anonymous regressions and delayed HTTP upstream cleanup.
Ordinary mux and native handler construction tests remain part of regression.

Source evidence: `internal/mtproto` supervises mtg-multi (one process per inbound); `internal/tuic` supervises tuic-server behind a panel UDP relay; `internal/amneziawgnet` runs AmneziaWG/gVisor inside the panel and bridges per-peer authenticated SOCKS into Xray. Their current behavior/data must be retained while moving to core adapters. Until all three migrations are tested, the installation is not fully single-core. Host administrative SSH and existing security services remain untouched.

## Protocol choices

Evaluate OpenSnell's GPLv3 implementation at a pinned commit, separately for each wire version; v5 QUIC Proxy Mode is its own gate. Official Surge documents v6 beta, deployment-derived shaping and no QUIC Proxy Mode. Use official mieru embedded server/client API, intercept authenticated requests before dialing. Use `golang.org/x/crypto/ssh` for SSH; reject session/shell/subsystem channels and verify outbound host keys. Reverse forwarding is disabled by default, bounded and authenticated, with no invented knowledge of the client's final target.

## Scope decisions

Local budgets/rates are not global limits. Multi-node work must allocate disjoint budgets and rate shares with bounded leases; expired/lost control cannot turn a limited user unlimited. Snapshot restore must be fenced from outstanding node leases. A recoverable core store is execution state under panel-issued policy, not a second administrator/control plane.
