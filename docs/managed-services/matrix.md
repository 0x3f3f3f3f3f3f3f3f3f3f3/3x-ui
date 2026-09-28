# Protocol × feature coverage (source audit)

Legend: **V** implemented and verified for the stated scope; **U** implemented
but not yet verified; **N** not implemented to the requested semantics (partial
existing code does not count); **—** evidence-backed not applicable. No V is
awarded by source inspection alone. Snapshot: original development baseline;
new implementation evidence must explicitly update the relevant cell.

Protocols: X = VMess, VLESS, Trojan, multi-user Shadowsocks, Hysteria2;
H = HTTP/Mixed SOCKS; W = WireGuard; A = AmneziaWG; T = TUIC v5;
M = MTProto; F = existing tunnel/dokodemo/TUN. The X members share the
management/Runtime path; this grouping does NOT claim equal wire semantics.

| Feature | X | H | W | A | T | M | F | Source evidence / qualification |
|---|---|---|---|---|---|---|---|---|
| Create/edit/delete | U | U | U | U | U | U | U | service/inbound.go; sidecar-specific services |
| Enable/disable listener | U | U | U | U | U | U | U | Runtime and backend reconcile managers |
| Client CRUD/attachment | U | N | U | U | U | U | N | model.ClientRecord/ClientInbound; H accounts need canonical integration |
| Bulk operations | U | N | U | U | U | U | N | service/client_bulk.go; lacks new policy state |
| Independent credentials | U | U | U | U | U | U | N | model.Client; HTTP/SOCKS accounts; F lacks exclusive owner model |
| Quota (new + established flow cutoff) | N | N | N | N | N | U | N | xray job optional restart; MTProto secret-limits need real test |
| Expiry | U | N | U | U | U | U | N | inbound_traffic.go, backend reconcile |
| Renewal / reset | U | N | U | U | U | U | N | client_crud.go, periodic_traffic_reset_job.go |
| Online status | U | N | U | U | U | U | N | GetOnlineUsers, per-peer stats, backend activity |
| IP limit | U | N | N | N | N | N | N | fail2ban job relies on usable authentication logs; not bandwidth shaping |
| HWID/device limit | U | N | U | U | U | U | N | sub/hwid_controller.go: subscription fetch gate, not native wire auth |
| Per-client raw statistics | U | N | U | U | N | U | N | TUIC job deliberately emits zero-byte client rows |
| Inbound statistics | U | U | U | U | U | U | U | Xray stats and backend jobs; units differ |
| Client up/down rate shaping | N | N | N | N | N | N | N | no shared authenticated client shaper |
| Billing multiplier / remainder | N | N | N | N | N | N | N | no durable billed total or revision |
| Crash/replay-safe billing cursor | N | N | N | N | N | N | N | Xray cursor memory-only; sidecar delta semantics |
| Logs | U | U | U | U | U | U | U | process logs and server controllers |
| Unified target/inbound routing | U | U | U | U | N | U | U | service/xray.go; A SOCKS bridge; M Xray egress |
| Authenticated per-client routing | U | N | U | U | N | N | N | session identity needs per-protocol E2E; M bridge identity unproved |
| Outbound selection / balancing | U | U | U | U | N | U | U | Xray router and outbound template |
| DNS settings | U | U | U | U | N | U | U | xray/dnsconf; A tunnel DNS and bridge |
| Subscription / format export | U | N | U | U | U | U | N | sub/raw, JSON, Clash; client-specific formats |
| Share links / QR | U | N | U | U | U | U | N | service/client_link.go, sub/links.go, frontend/lib/xray |
| API | U | U | U | U | U | U | U | controller/inbound.go, client.go; generated OpenAPI |
| Notifications | U | N | U | U | U | U | N | stats jobs, tgbot/discord/email; derives traffic/expiry |
| Backup / restore | U | U | U | U | U | U | U | database backup and settings upload paths |
| Portable client import/export | U | N | U | U | U | U | N | service/client_portable.go; selected fields explicit |
| Installer / updater | U | U | U | U | U | U | U | install.sh, update.sh; fork preservation missing |
| Docker | N | N | N | N | N | N | N | Node 22 build stage conflicts with required Node 26 |
| Multi-node mutation/sync | U | N | U | N | N | N | N | nodeEligibleProtocols in inbound_protocol.go |
| Global multi-node policy cap | N | N | N | N | N | N | N | snapshots provide totals, not shared bandwidth credit |
| Groups / LDAP integration | U | N | U | U | U | U | N | client group model; LDAP sync uses client bulk service |
| Subscription host overrides | U | N | U | U | U | U | N | model.Host, sub/host_sub.go |
| Inbound fallbacks | U | N | N | N | N | N | N | VLESS/Trojan TCP TLS eligibility only within X |
| First-use delayed expiry | U | N | U | U | U | U | N | adjustTraffics; TUIC online signal zero-byte rows |
| Scheduled client reset / weekly renew | U | N | U | U | U | U | N | ClientRecord.ResetWeekday/TrafficReset and jobs |
| API token permissions / CSRF | U | U | U | U | U | U | U | middleware, panel/api_token.go, session |

All source paths in this table are under `internal/` unless prefixed frontend.
N includes incomplete applicable capability; it never means excluded scope.
Single-user Shadowsocks modes must use explicit exclusive ownership instead of
pretending a shared server credential identifies several independent users.
Unauthenticated HTTP/SOCKS/TUN/forwarding requires owned resource bindings.

## New first-class services

| Required capability | Snell v4 | Snell v5 | Snell v6 beta | SSH | mieru TCP | mieru UDP | TCP/UDP forwarding |
|---|---|---|---|---|---|---|---|
| CRUD / start-stop / client management | N | N | N | N | N | N | N |
| Credential / resource ownership / revocation | N | N | N | N | N | N | N |
| Bulk / API / validation / permissions | N | N | N | N | N | N | N |
| Raw counters / billed bytes / multiplier | N | N | N | N | N | N | N |
| Aggregate live up/down shaping | N | N | N | N | N | N | N |
| Quota / live cutoff / restart persistence | N | N | N | N | N | N | N |
| Expiry / renewal / reset / reason isolation | N | N | N | N | N | N | N |
| Online status / IP-device limits | N | N | N | N | N | N | N |
| Logs / diagnostics / notifications | N | N | N | N | N | N | N |
| Routing / egress / block / balancer | N | N | N | N | N | N | N |
| DNS / available domain identity | N | N | N | N | N | N | N |
| Subscription / share / valid config export | N | N | N | N | N | N | N |
| Backup / restore / import / export | N | N | N | N | N | N | N |
| Install / upgrade / rollback / uninstall | N | N | N | N | N | N | N |
| Docker / platform capability gating | N | N | N | N | N | N | N |
| Multi-node / source deduplication | N | N | N | N | N | N | N |
| Real-client TCP / UDP interoperability | N | N | N | N | N | N | N |

### SSH verified slices of the incomplete vertical

The broad SSH rows above remain N where the full bundled capability is incomplete.
The following narrower scopes now have implementation and direct evidence:

| Scope | Status | Evidence / limit |
|---|---|---|
| Existing service create/edit, key-only client creation, disabled listener protection | V | `TestSSHInboundPreservesCanonicalCredentials`, real managed lifecycle test |
| Public-key rotation, manual disable, metadata edit: unrelated existing client stays connected | V | actual two-client SSH → Xray path on SQLite and PostgreSQL |
| Raw/billed payload, delayed expiry, reset source replacement, quota reduction | V | real service mutations and TCP payload; policy API and existing-client policy tab expose exact billing; client-list billed balances/filter/order/summary verified in the following row; node dashboard remains N |
| Versioned policy API, fixed-point multiplier boundary, precise usage strings | V | real HTTP admin/scope checks; SQLite/PostgreSQL transactions, stale/recreated identity rejection, historical carry and reset persistence |
| Public aggregate rate changes across two SSH inbounds | V | actual OpenSSH → panel-managed Xray, two same-IP clients/four channels each, raw duplex counters, unlimited baseline, 32/64/128 KiB/s, live changes within 2s; browser policy save and exact SSH billing also verified |
| Client-list billed balances, SQL filters/order/counts, info modal and depleted cleanup | V | exact strings/carry; SQLite/PostgreSQL scoped cleanup, concurrent reset/quota/identity recheck and rollback; 177-file/1760-test frontend run and real Chromium→SSH→Xray billing/quota reduction; other usage consumers remain N |
| Existing-client policy tab, precise billing, validation and conflict reload | V | 176-file frontend suite; real Chromium → HTTP → SQLite → strict-host-key OpenSSH → Xray echo with 1.5x accounting; bulk and other statistics views remain N |
| Policy backup and cross-dialect migration | V | raw rate fields and edit version survive SQLite dump/restore and SQLite→PostgreSQL migration; portable restoration has separate evidence below |
| Portable client/policy restoration | V | SQLite/PostgreSQL atomic attachments and usage, consistent export, exact byte strings above 2^53, historical charges/carry and independent restrictions, fresh identities and retained-traffic ownership. Actual OpenSSH → Xray remains denied after restore/restart and resumes with exact billing after credit. Scope: local SSH and unattached SSH; distributed restoration and large-import performance remain open |
| Port collision logging/retry, core exit protection, core stop/restart | V | actual occupied listener and actual Xray process; capacity/complete fault rollback remain N |
| Credential and permission persistence | V | canonical JSON/merge, old SQLite/PostgreSQL migration, SQLite backup restore and cross-dialect migration |
| Inbound creation and independent client credentials in existing forms | V | actual Chromium creates a local SSH listener and client, edits permission fields, and reaches real OpenSSH → Xray; form regressions preserve host/client identity and lifecycle values; bulk and other management surfaces remain open |
| OpenSSH configuration and actual host-key export | V | SQLite/PostgreSQL public-key-only metadata; actual OpenSSH parser; browser downloads consumed by OpenSSH, mismatched host key refused; exported files contain no private key; initial application follows the existing 30-second configuration scheduler |
| SSH generated API types and existing option endpoint | V | Go→Zod/types/OpenAPI regenerated; real authenticated browser reads the host public key; complete API/bulk/portable/export/deployment acceptance remains open |
| Internal upstream TCP connector | V | Real OpenSSH 9.6p1: 43008-byte request/echo, half-close, wrong pin/key rejection, IPv6 target. Wire peers: 128-flow capacity/reuse, encrypted keys, pinned algorithm selection, deadlines/cancellation/shutdown and concurrent bytes under race detector. Editor and probe evidence below |
| Internal authenticated upstream bridge and staged generations | V | SOCKS → actual OpenSSH → independent target, half-close and wrong-pin no-fallback; auth/UDP rejection, rollback/selective commit, explicit reset on revocation, port conflict, 512 pending connections, five-second negotiation timeout and restart |
| SSH upstream settings and Runtime application | V | Existing settings service saves authored SSH; preview has no SSH listener mutation. Actual Xray routes two SSH tags and a native exit, honors block priority, rejects wrong pins/dead upstream without fallback, preserves unchanged hot-applied flows, recovers prior config after startup failure, and cleans up after SIGKILL. API-less SSH activation is explicitly refused; node/deployment acceptance remain N |
| SSH upstream route probe service | V | Actual temporary Xray → separate authenticated bridge → OpenSSH → HTTP target. Real/http/tcp modes, stale context cannot override requested pin, malformed sibling isolation, native proxy chains, failure cleanup and unchanged applied flows. Browser public-HTTPS probe and API authorization verified; continuous upstream health remains N |
| SSH outbound editor and local SQLite backup/restore | V | Strict typed form/JSON boundaries, explicit key reveal and pin validation, endpoint and HTTP batching. 183 frontend files/1810 tests pass. Actual Chromium creates/saves/reloads, edits pins, probes through OpenSSH, denies anonymous/monitor/node-sync config access, and restores key/pin plus real traffic from a downloaded database after panel restart. PostgreSQL outbound restoration and automatic node template distribution remain open |
| Managed SSH ingress policy through SSH upstream | V | Actual OpenSSH → managed ingress → Xray → independent OpenSSH → target. Exact payload billing, quota reduction/reset/revocation; two same-IP clients/four channels each across two inbounds, shared duplex 32/64/128 KiB/s, live changes and restart. Upstream authentication records independently prove traversal; full multi-protocol and node policy acceptance remain N |

## Protocol-specific applicability

Standard SSH direct-tcpip/forwarded-tcpip channels carry TCP only: native UDP
and a fabricated SSH UDP subscription node are not applicable to those modes
([RFC 4254 §7](https://www.rfc-editor.org/rfc/rfc4254#section-7)). An extra UDP
encapsulation scheme would be a separate capability, not claimed here.
Xray TLS/REALITY/XTLS fields are not Snell, SSH or mieru server options; their
own authentication and transport fields must be used instead. This excludes
invalid fields, never common management features.

No common management feature is marked not applicable because of backend
difficulty or missing runtime support. Snell IP-only egress domain visibility,
MTProto egress identity and TUIC attribution remain unresolved requirements.

## Internal SSH backend evidence

The new-service matrix above covers integrated panel services and remains N
until those paths exist. These narrower backend results are verified separately;
they do not imply UI/API/routing/deployment or multi-node completion.

| Internal SSH capability | Status | Evidence |
|---|---|---|
| Public-key TCP -L/-D and authorized -R | V | Real OpenSSH 9.6p1 requests and exact payload checks |
| Identity/destination passed to dial adapter | V | Stable policy ID, inbound tag and original domain/port |
| Default privilege denial and listener/channel bounds | V | Authenticated SSH requests, capacity rejection and listener reuse |
| Live credential revocation | V | Existing-key removal, other-user isolation and an in-flight signed handshake |
| Aggregate duplex rate / live update | V | Two same-IP clients, two SSH processes/four channels each, independent socket counts |
| Quota / multiplier / restart denial | V | OpenSSH long transfers at 0.5/1/1.5/2×; server/controller restart |
| Internal Xray TCP route execution / no double billing | V | OpenSSH through real Xray: exact/regexp users, domain/IP/port/source, two exits, block priority, native-versus-bridge counters |
| Production manager and service/API integration | V | actual Runtime/configuration wiring, process lifecycle and durable local policy application; complete UI/deployment/global-node work remains N |
| Enforcing a standard -R client's local target | N | Target absent from protocol; listener ACL is not target enforcement |

Panel lifecycle prerequisites now recognize admission-owned accounts: single
and bulk resets retire old sources and preserve other restrictions, renewal
uses an atomic ledger reset, and legacy raw-quota jobs do not auto-disable
those accounts. SQLite/PostgreSQL and real TCP tests cover these service paths.
This does not change the integrated-service matrix: native collection, public
activation, remote accounting and protocol management remain incomplete.
