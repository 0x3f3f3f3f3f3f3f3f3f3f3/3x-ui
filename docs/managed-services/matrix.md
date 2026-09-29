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
| Local SSH runtime observation API | V | Actual SSH transport/channel counts, occupied-listener protection/recovery, disable/suspension/core exit, deletion/port reuse and database-handle replacement on SQLite/PostgreSQL. Pure owner-filtered HTTP read; admin/session permitted, monitor/node-sync denied. Per-client observations have separate evidence below; device counts and remote observation remain open |
| Local SSH runtime display in the existing inbound list | V | Real Chromium desktop/mobile: idle, owned listener collision → protected → recovery, two real authenticated transports for one client (1/2/1), browser offline/recovery and UI disable. Conditional 3s query; component tests reject stale/error/invalid responses. Full frontend 184 files/1821 cases pass with one worker; initial parallel run had two existing form timeouts, documented in validation. Device counts and remote observation remain open |
| Local SSH client online, last-online and transport source IP collection | V | Actual admitted idle transports; same-IP independent clients and duplicate transports; canonical membership/DB generation filtering on SQLite/PostgreSQL. Real Xray job with native online RPC marked unsupported still records online/IP/last-online without traffic. Real Chromium/OpenSSH sees Online before payload and disconnect aging; IP enforcement/device identity and remote execution remain N. Native host-wide bans are explicitly bypassed for SSH observations |
| Internal upstream TCP connector | V | Real OpenSSH 9.6p1: 43008-byte request/echo, half-close, wrong pin/key rejection, IPv6 target. Wire peers: 128-flow capacity/reuse, encrypted keys, pinned algorithm selection, deadlines/cancellation/shutdown and concurrent bytes under race detector. Editor and probe evidence below |
| Internal authenticated upstream bridge and staged generations | V | SOCKS → actual OpenSSH → independent target, half-close and wrong-pin no-fallback; auth/UDP rejection, rollback/selective commit, explicit reset on revocation, port conflict, 512 pending connections, five-second negotiation timeout and restart |
| SSH upstream settings and Runtime application | V | Existing settings service saves authored SSH; preview has no SSH listener mutation. Actual Xray routes two SSH tags and a native exit, honors block priority, rejects wrong pins/dead upstream without fallback, preserves unchanged hot-applied flows, recovers prior config after startup failure, and cleans up after SIGKILL. API-less SSH activation is explicitly refused; node/deployment acceptance remain N |
| SSH upstream route probe service | V | Actual temporary Xray → separate authenticated bridge → OpenSSH → HTTP target. Real/http/tcp modes, stale context cannot override requested pin, malformed sibling isolation, native proxy chains, failure cleanup and unchanged applied flows. Browser public-HTTPS probe and API authorization verified; continuous upstream health remains N |
| SSH outbound editor and local SQLite/PostgreSQL backup/restore | V | Strict typed form/JSON boundaries, explicit key reveal and pin validation, endpoint and HTTP batching. 183 frontend files/1810 tests pass. Actual Chromium creates/saves/reloads, edits pins, probes through OpenSSH, denies anonymous/monitor/node-sync config access, and restores key/pin plus real traffic from downloaded SQLite and PostgreSQL backups after panel restart. Automatic node template distribution remains open |
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

## Scoped mieru backend evidence

The complete-service mieru columns remain N. These rows distinguish internal
adapter checks from local public integration. PostgreSQL evidence is limited
to the explicitly named cases below.

| Mieru capability | Status | Evidence / limit |
|---|---|---|
| Official v3.38.0 authenticated TCP and UDP | V | Official client API, all four TCP/UDP underlay and target combinations, two independent same-IP users |
| Stable identity, domain, target and original source in dispatch | V | Actual native sessions preserve policy ID, inbound tag, original localhost name, port, network and underlay source; required callback has no automatic direct fallback |
| Whole-datagram fixed-point quota and bidirectional payload billing | V | 1.5x/0.5x exact counters; rejected reply sends/charges no prefix, smaller remaining packet succeeds; native rolling quota fields are empty |
| Shared live duplex shaping across native listeners | V | Actual mixed streams/packets, two same-IP clients and two listeners, 32/64 KiB/s, unlimited baseline and live changes; receiver-observed packet flight is explicitly bounded in the test |
| Shared SSH/mieru local controller ownership | V | Real SSH plus official mieru clients on both underlays, SQLite/PostgreSQL, aggregate duplex 64/128 KiB/s and unlimited baseline, existing-flow live changes within 2s, SSH shutdown preserves mieru; direct loopback connectors in this test, public mixed-protocol attachment still pending |
| Quota, disable and owned shutdown | V | Existing TCP/UDP cutoff and server restart denial; malformed UDP cleanup, TCP backpressure, unrelated-user continuity and partial-start listener release |
| Live native credential replacement | V | Official TCP/UDP clients; atomic invalid-batch rejection, unchanged sessions, rotation/removal/re-addition, empty set, delayed/cached authentication denial, exact policy-ID reassignment billing, pending dial cancellation and idle TCP socket reclamation |
| Continuous one-way UDP target lifetime | V | Actual 31-second upload-only traffic retains one target source port and exact upload-only billing |
| Internal authenticated Xray TCP/UDP routing / policy-aware bridge | V | Private policy-ID credentials, actual direct IPv4 peer replies, user/domain/IP/source/tag/network/port/priority/block/balancer exits; official mieru clients on both underlays, exact single billing and existing-flow revocation. Other outbounds and IPv6 remain open; public Runtime evidence is listed separately below |
| Private bridge gRPC hot insertion | V | Actual core retains managed authentication and uint32 levels 255/4294967295, TCP/UDP payload and peer metadata, no duplicate user counters, existing stream continuity during add/remove; required policy definitions preloaded at startup |
| Public canonical model and native credentials | V | Local TCP/UDP/both settings, canonical email/password and durable policy ownership; transport-aware public/private port conflicts; SQLite/PostgreSQL creation and bulk lifecycle tests |
| Public Runtime lifecycle | V | Actual official clients through production Xray lifecycle; credential rotation, unrelated-client continuity, disable/re-enable, delayed expiry, quota reduction, core stop/recovery and protocol conversion; remaining full fault/rate matrix is open |
| Public natural quota under bounded concurrent payload | V | SQLite/PostgreSQL, TCP/UDP underlays, two TCP plus two persistent UDP flows, 0.5/1/1.5/2x: exactly 8 MiB raw per case, independent receive counts, same-IP peer continuity, old/new flow cutoff and core-restart denial. Declared 96 KiB outstanding-workload bound; unrestricted buffers and panel-process restart remain open |
| Public restriction combinations and abnormal core exit | V | Reset/increased quota preserve manual disable and expiry; renewal preserves manual disable on both databases/transports. Actual Linux SIGKILL on both transports closes old flows, denies new flows, releases public ports and recovers without changing durable counters; panel-process and host restart remain open |
| Public status API and management UI | V | Owner-scoped status/generated schemas plus full frontend regression; actual browser creates tcp/udp/both inbounds/password clients, edits policies, observes desktop/mobile count 2 and disables runtime. Remote/global and full public performance matrix remain open |
| Native share/subscription export | V | Official mierus parser/client TCP+UDP flows for tcp/udp/both, canonical credentials and Host overrides; actual Mihomo v1.19.30 for each emitted transport; Xray JSON and legacy Clash exclude unsupported native profiles |
| Native JSON browser download | V | Actual browser QR download imports into official mieru v3.38.0 and carries TCP/UDP echoes under tcp/udp/both settings; exact raw/billed counts and reduced-quota TCP closure; original loopback1080 export checked before relocating only the fixture's local SOCKS port |
| TCP export scheduling under sustained shaping | V | Official TCP/both profiles and TCP Mihomo nodes use independent TCP connections; real subscription, fresh browser/CLI and two SQLite/PostgreSQL mixed-rate repetitions pass. Fixed-underlay test demonstrates why manual TCP multiplexing can delay new handshakes. The earlier UDP short-window observation and remaining full performance/quota matrix stay open |
| Portable native client restore | V | SQLite/PostgreSQL native-only, shared SSH/mieru, detached and legacy snapshots preserve credentials, raw/billed/remainder, rates, quota, disable and renewal metadata; reattachment retains ownership |
| IP/device enforcement and remote/global policy | N | Local admitted-session/IP observations exist; observations do not enforce per-client IP/device limits or distribute a global budget |
| Native deployment/upgrade integration | N | Embedded adapter and pinned source exist; installation, upgrade, packaging and node acceptance remain open |
| Internal native session/queue bounds and generation diagnostics | V | Maintained protocol-only extension limits native admission before allocation, bounds payload trees/staging, reclaims finished metadata and suppresses per-generation diagnostic groups; real TCP/UDP stress and payload recovery. This is not a process RSS bound or public Runtime completion |
| Native UDP progress under application backpressure | V | Byte/segment/staging window bounds, current wire credits, earlier-fragment retention and separate ACK/delivery progress; encrypted ACK gap tests, complete native race suites and 20 unchanged real SSH-peer-stop recovery repetitions; queue bounds and rate tolerances unchanged |
| PostgreSQL mieru vertical acceptance | N | Shared ownership, public lifecycle, canonical creation, bulk lifecycle and portable restoration have scoped evidence; browser/deployment and the complete requested matrix remain open |

## UDP core prerequisite evidence

These results concern the independently built pinned core patch. They do not
establish public managed mieru activation or the remaining outbound matrix.

| Core prerequisite | Status | Evidence / limit |
|---|---|---|
| Whole payload over authenticated Trojan UDP and direct IPv4 egress | V | Real fixed-core binary and UDP socket, 0/1/8170/8192/8193/65507-byte request/reply, no additional split or duplicate packet |
| Empty packet presence independent of billing bytes | V | Actual connected UDP read and real pipe preserve one empty packet and zero byte count; empty packets consume finite queue capacity |
| Finite per-user UDP pipe configuration | V | Real authenticated UDP input and stalled TCP protocol outbound, unlimited control plus finite user policy despite unlimited global default; Linux-owned socket buffers fixed |
| Reproducible source and build identity | V | Pinned module/checksum verification, checked patch application, separate output, retained MPL-2.0 provenance; existing output paths are refused |
| Private policy-ID bridge, source-IP replies and unified route tests | V | Opt-in authenticated core extension preserves user and original target/source; direct reader returns actual IP; absent peer metadata and stock/forged capability replies fail closed. Public activation remains N |
| Distribution, installer selection and Runtime capability enforcement | N | The independent build command does not replace or install a panel core |
