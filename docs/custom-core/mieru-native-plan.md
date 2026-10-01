# Native mieru core implementation plan

> Execute inline using superpowers:executing-plans and test-driven-development.

**Goal:** Real mieru inbound/outbound payload inside Custom Xray-core using its
Dispatcher and shared Client Policy Engine.

**Architecture:** Compile the pinned official library; embed its transport/mux
in the core, map authenticated users to configured canonical identities, and
route all decrypted payload through Xray. Core owns listeners and sessions.

**Stack:** Go1.27.1, managed Xray source, mieru v3.38.0, existing protobuf tools.

**Spec:** [mieru-native-design.md](mieru-native-design.md).

## Global constraints

- One Custom Xray-core business process; no mita/sshd/SOCKS bridge.
- Existing Xray behavior, field numbers and third-party notices remain intact.
- Only authenticated configured identities enter shared policy.
- Same managed module for panel/core builds; no module-cache modifications.
- Publish validated logical commits only to feature/custom-xray-unified-policy.

## Review focus

- Cached multiplexed sessions cannot revive removed/replaced credentials.
- Library framing bytes never inflate decrypted payload accounting.
- UDP destination and reply addresses survive route selection and boundaries.
- Listener start failure/Close leaves no sockets or active session goroutines.
- Both outbound transport dialers use the supplied Xray dialer and cancellation.

## Task8A: Native protocol path

Files: create `core/xray/proxy/mieru/{config.proto,config.pb.go,account.go,server.go,client.go,packet.go}`
and core tests; create `core/xray/infra/conf/mieru.go`/`mieru_test.go`; modify
`infra/conf/xray.go`, `main/distro/all/all.go`, `go.mod`, `go.sum`, provenance.

Interfaces: inbound `NewServer(ctx context.Context, config *ServerConfig) (*Server,error)`
implements proxy.Inbound, common.Runnable and proxy.UserManager; outbound
`NewClient(ctx context.Context, config *ClientConfig) (*Client,error)` implements
proxy.Outbound and common.Closable. Mieru Account implements protocol.Account.

- [x] Write `TestMieruNativeConfigBuildsAuthenticatedInboundAndOutbound` using
  literal JSON with trusted client IDs and TCP/UDP modes; run it and observe
  unknown protocol RED before adding core configuration registration.
- [x] Add typed config/account conversion, pinned dependency and registrations;
  pass JSON/config validation including duplicate credentials, bad modes and endpoints.
- [x] Write real native core interoperability tests with official client/server,
  independent payload TCP/UDP targets, route selection and mux; observe RED.
- [x] Implement native listeners, authenticated session dispatch, packet framing
  and injected outbound dialers; pass actual TCP/UDP interop tests.
- [x] Add user replacement/deletion, listener cleanup, sibling survival,
  shared Tunnel identity, exact ledger, quota/expiry and aggregate rate tests.
  Observe failures before fixing lifecycle and policy boundaries.
- [x] Run focused core race/config/control, core suite, root type compatibility
  and generation/lint checks; one read-only review and corrections.
- [ ] Finish documentation, logical commit/fork push and distinct clean-source
  core/panel build provenance.

Next vertical task: panel/API/DB/forms/export/runtime binding for mieru. Snell
and SSH native adapter work remains the same central protocol priority; no
additional password migration prerequisite is inserted ahead of them.
