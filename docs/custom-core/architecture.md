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

## Existing paths requiring migration

Source evidence: `internal/mtproto` supervises mtg-multi (one process per inbound); `internal/tuic` supervises tuic-server behind a panel UDP relay; `internal/amneziawgnet` runs AmneziaWG/gVisor inside the panel and bridges per-peer authenticated SOCKS into Xray. Their current behavior/data must be retained while moving to core adapters. Until all three migrations are tested, the installation is not fully single-core. Host administrative SSH and existing security services remain untouched.

## Protocol choices

Evaluate OpenSnell's GPLv3 implementation at a pinned commit, separately for each wire version; v5 QUIC Proxy Mode is its own gate. Official Surge documents v6 beta, deployment-derived shaping and no QUIC Proxy Mode. Use official mieru embedded server/client API, intercept authenticated requests before dialing. Use `golang.org/x/crypto/ssh` for SSH; reject session/shell/subsystem channels and verify outbound host keys. Reverse forwarding is disabled by default, bounded and authenticated, with no invented knowledge of the client's final target.

## Scope decisions

Local budgets/rates are not global limits. Multi-node work must allocate disjoint budgets and rate shares with bounded leases; expired/lost control cannot turn a limited user unlimited. Snapshot restore must be fenced from outstanding node leases. A recoverable core store is execution state under panel-issued policy, not a second administrator/control plane.
