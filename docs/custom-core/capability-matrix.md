# Protocol × feature × direction × test status

This is the initial source-audit matrix, not a support announcement. `E/U` = existing code, runtime unverified here; `N` = requested but not implemented; `NA` = not applicable with reason. New implementation uses `I/U` until matching tests pass (`I/V`). Scope is the entire original request.

## Current protocol coverage

| Protocol/path | Inbound/outbound baseline | Client lifecycle / export | Unified stable ID/rate/billing/quota/live close | Required evidence |
| --- | --- | --- | --- | --- |
| VLESS | E/U both | E/U | N | authenticated rotation, TCP/UDP/mux/Vision, real client regression |
| VMess | E/U both | E/U | N | TCP/UDP/mux and account lifecycle |
| Trojan | E/U both | E/U | N | TCP/UDP/fallback and account lifecycle |
| Shadowsocks/2022 | E/U both | E/U | N | cipher variants, TCP/UDP/relay, identity |
| Mixed/SOCKS | E/U both (outbound socks) | E/U | N | authenticated user versus anonymous listener, UDP |
| HTTP | E/U both | E/U | N | auth, CONNECT, normal request path, raw-copy |
| Hysteria2 | E/U both | E/U | N | QUIC/mux/UDP and auth updates |
| WireGuard | E/U both | E/U | N | per-peer identity, IP/payload accounting distinction |
| TUN | E/U inbound | no account auth; resource identity required | N | packet semantics, route and owner mapping |
| Tunnel/dokodemo-door | E/U inbound; NA outbound (listener adapter) | forwarding client lifecycle N | I/V for scoped Tunnel/local persistence/SQL settlement tests; production Runtime activation N | TCP/UDP, all routing modes, owner lifecycle, shared policy |
| Snell v4 | N both | N | N | official Surge interoperability + independent outbound test |
| Snell v5 | N both | N | N | v4-like paths separately, QUIC Proxy Mode mandatory |
| Snell v6 beta | N both | N | N | fixed beta client/server, shaping modes, TCP/UDP/reuse |
| mieru | N both | N | N | official client/server, TCP/UDP/mux, deleted active users |
| SSH | N both | N | N | OpenSSH -L/-D/authorized -R, strict upstream host key |
| MTProto | E/U external mtg-multi; migration N | E/U secrets/ad-tags | N | preserve features, move execution into core |
| TUIC v5 | E/U external tuic-server; migration N | E/U | N | preserve QUIC/UDP, remove panel relay after parity |
| AmneziaWG | E/U panel-side runtime; migration N | E/U peers/forwarding | N | preserve obfuscation/IPv6/per-peer data, direct dispatcher |
| Freedom/direct, block, DNS, loopback | E/U outbound | NA account service | managed-flow traversal N | route correctness, no fallback or loop/bypass |

## Feature cross-product checklist

Each applicable row above must cover **both directions separately** and every feature below. Until protocol-specific evidence replaces this default, original features are E/U and custom integration is N; no implicit checked cells.

| Feature group | Existing source anchors | Custom integration / tests |
| --- | --- | --- |
| Service create/edit/delete/enable, hot apply | `internal/web/service/inbound*`, `internal/xray/hot_diff.go` | N |
| Client create/edit/delete, rename/credential rotation, disable, bulk/groups | `client_crud.go`, `client_bulk.go`, `client_inbound_apply.go` | N |
| Upload/download rate, quota/multiplier/reset/renew/expiry | `inbound_traffic.go`, `traffic_writer.go`, ClientTraffic/ClientRecord | N |
| Counters, online IP, connections, logs, restriction reasons | xray API, traffic jobs, websocket | N |
| IP/HWID/concurrency restrictions | Fail2ban and subscription HWID paths | N; source IP is not a trusted device ID |
| Routing, DNS, outbounds, block, balancing and chains | core Dispatcher/Router and panel xray forms | N |
| Share/QR/subscription/config export | `internal/sub`, `frontend/src/lib/xray`, `docs/lib/xray` | N |
| API, permission checks and notifications | controller/runtime, Telegram/Discord/email/eventbus | N |
| SQLite/PostgreSQL migrations, backup/restore/import/export | `internal/database`, server service | stable identity and ledger migration I/V; portable exports/restore fencing N |
| Install/upgrade/Docker/platform matrix | install/update, DockerInit, CI/release workflows | N |
| Node sync and global budgets/rates, outage/replay | runtime Local/Remote, node and global traffic models | N |
| LDAP sync, external subscription links, hosts, renewal schedules | existing services/jobs and DB relationships | N; preserve current behavior |
| Performance, faults, fuzz, leak/security and clean build | existing tests plus requirements §15 | N |

## Explicit inapplicability

- Standard SSH -L/-D/-R are TCP, not arbitrary UDP tunnels; do not fabricate UDP support.
- Snell v6 has no v5 QUIC Proxy Mode per official Surge documentation; its own UDP forwarding remains required.
- A forwarding listener has no business credential handshake: its server-owned exclusive resource is the trusted identity source.
- A client format with no representation of a protocol cannot receive a fabricated subscription entry; provide its actual config/instructions.
- Encrypted/opaque payloads do not guarantee sniffable domains, nor do ordinary proxy accounts provide trusted device IDs.

Lack of an upstream API, platform test machine or commercial client is **not** inapplicability. It is development or verification work still outstanding.

## Incremental verified evidence (does not upgrade an entire row)

- Tunnel TCP/UDP and both protocol aliases: trusted configured `clientId` reaches Dispatcher policy; two TCP listeners plus UDP share exact counters; manual disable closes active TCP and blocks UDP. Four real socket tests pass.
- Selected SOCKS outbound and default block: exercised in one core instance with a separate internal SOCKS listener as the test upstream. Metering is once at the managed ingress. Missing managed policy rejects traffic.
- Engine primitives: fixed-point multipliers, batch/fraction invariance, concurrent quota, reason composition, expiry, stale policy/revocation and shared directional token buckets pass race tests. These are not proof of all protocols, global limits or persistent accounting.
- 100 MiB quota at multiplier 2: admitted 50 MiB bidirectional payload; exact figures and endpoint loss are in testing.md.
- Panel UI/API/DB integration, durable panel settlement/restore fencing, all Snell/mieru/SSH adapters, ACL/listener ownership lifecycle and full single-core migration are still N.

- Independent binary TCP Tunnel rates: two connections share each configured upload/download limit, 256 KiB/s and 1 MiB/s; six cases including unlimited controls pass. See testing.md and evidence/tunnel-rates.jsonl.

- Local durable reservations: graceful and abrupt engine recovery, exact/frozen counters, version/tombstone persistence, atomic batches, storage failure and real Tunnel restart tests implemented. Panel settlement, restore fencing and global budgets remain N; protected core control API is implemented and tested as described below.

- Private Unix gRPC API v1: capability negotiation, atomic policy updates, current state, version-checked revocation, connection query/close, checkpoint and committed cumulative ledger. Real existing-flow RPC update resumes within 2 s with exact multiplier-boundary accounting. Panel adapter rejects unsupported cores, but Runtime/DB/UI integration remains N.

Identity/ledger increment (2026-09-29): ClientRecord stable UUID generation/backfill, immutable ORM updates, SQL backup-compatible schema, SQLite→PostgreSQL migration and idempotent committed-receipt settlement are I/V for the tests named in testing.md. Real Tunnel traffic and restart settle correctly through the private API into both databases. Core create-only legacy seeding is I/V. Production Runtime/config/UI activation, portable exports, coordinated legacy cutover, node identity mapping and restore fencing remain N; this evidence does not upgrade complete lifecycle or protocol rows.

Core listener removal increment (2026-09-29): real Tunnel tests cover established TCP/UDP termination before same-port reassignment, separate accounting for the new owner, and survival of another listener owned by the old client. Unmanaged TCP and Unix socket connections also close on removal. These scoped core tests do not establish the panel ownership UI/API or the legacy accounting cutover.

Policy-edit increment: optional rate/multiplier persistence, omission preservation, exact validation and transactionally versioned desired policies are implemented. The normal client-edit service can hot-apply policies for an already activated local client through Runtime; an actual core test verifies existing-stream rate change, historical billing and disable. Automatic initial activation, the user-facing forms, remaining lifecycle/bulk paths, period resets, first-use expiry and global allocation are unfinished. See testing.md for the exact database and real-process evidence; no full protocol/lifecycle row is upgraded by this increment.

Ledger-collection increment: the existing traffic collector now polls committed client receipts through Runtime for an already managed local process. Bounded pagination, atomic page settlement and cursor validation precede legacy operational-counter reads. Core idle checkpoint writes are avoided. Real-child and database regression evidence is recorded in testing.md. Legacy statistics projection and quota/reset cutover are still unfinished, so this does not establish complete production activation or UI support.
