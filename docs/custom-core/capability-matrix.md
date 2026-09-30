# Protocol × feature × direction × test status

This is the initial source-audit matrix, not a support announcement. `E/U` = existing code, runtime unverified here; `N` = requested but not implemented; `NA` = not applicable with reason. New implementation uses `I/U` until matching tests pass (`I/V`). Scope is the entire original request.

## Current protocol coverage

| Protocol/path | Inbound/outbound baseline | Client lifecycle / export | Unified stable ID/rate/billing/quota/live close | Required evidence |
| --- | --- | --- | --- | --- |
| VLESS | E/U both | private Runtime credential rotation and scoped ordinary local edits I/V; full lifecycle/export incomplete | I/V scoped TCP/UDP/Mux identity, shared rate/billing/live close and explicit managed local startup; automatic cutover N | Vision, complete account lifecycle, independent client regression |
| VMess | E/U both | E/U | I/V scoped TCP/UDP/Mux identity, shared rate/billing/live close; production activation N | complete account lifecycle and independent client regression |
| Trojan | E/U both | E/U | I/V scoped TCP/UDP/Mux identity, shared rate/billing/live close; production activation N | fallback and complete account lifecycle |
| Shadowsocks/2022 | E/U both | E/U | classic AEAD TCP/UDP/Mux scoped I/V; 2022 managed identity N | other cipher variants, 2022 user-update isolation/relay, complete lifecycle |
| Mixed/SOCKS | existing inbound/outbound; native authenticated core increment I/V | native typed CRUD/alias revocation and idle/active UDP cleanup I/V; panel owner persistence/read guards, form metadata preservation, canonical runtime configuration and scoped managed grouped credential edits I/V; owner picker, JSON API/export and full-database preservation I/V; single/bulk UUID-based owner detach/delete with runtime recovery I/V; single canonical Update/name/credentials/policy/enable/quota/expiry I/V scoped; bulk/by-email lifecycle and portable foreign-owner import N | scoped password-auth TCP/UDP stable ID and shared directional rate/billing/live close I/V; generated SOCKS/CONNECT plus Tunnel shared ledger I/V; anonymous resource ownership and full panel lifecycle N | legacy counter migration, managed idle policy control, full protocol matrix |
| HTTP | existing inbound/outbound; native authenticated core increment I/V | native CRUD, empty required-auth and request identity isolation I/V; panel owner persistence/read guards, form metadata preservation, canonical runtime configuration and scoped managed grouped credential edits I/V; owner picker, JSON API/export and full-database preservation I/V; single/bulk UUID-based owner detach/delete with runtime recovery I/V; single canonical Update/name/credentials/policy/enable/quota/expiry I/V scoped; bulk/by-email lifecycle and portable foreign-owner import N | scoped CONNECT and plain-request stable ID/accounting/live close I/V; generated CONNECT plus Tunnel shared ledger I/V; full panel lifecycle N | legacy counter migration, managed idle policy control, full protocol matrix |
| Hysteria2 | E/U both | E/U | N | QUIC/mux/UDP and auth updates |
| WireGuard | E/U both | E/U | N | per-peer identity, IP/payload accounting distinction |
| TUN | E/U inbound | no account auth; resource identity required | N | packet semantics, route and owner mapping |
| Tunnel/dokodemo-door | E/U inbound; NA outbound (listener adapter) | database, local stable-owner picker, raw TCP/UDP source ACL, concrete outbound selector and scoped ordinary Runtime owner lifecycle I/V; complete forwarding UI/unowned-rule migration N | I/V for scoped Tunnel/local persistence/SQL settlement, automatic local activation and guarded handoff | remaining forwarding modes/owner lifecycle, full shared-policy coverage |
| Snell v4 | N both | N | N | official Surge interoperability + independent outbound test |
| Snell v5 | N both | N | N | v4-like paths separately, QUIC Proxy Mode mandatory |
| Snell v6 beta | N both | N | N | fixed beta client/server, shaping modes, TCP/UDP/reuse |
| mieru | N both | N | N | official client/server, TCP/UDP/mux, deleted active users |
| SSH | N both | N | N | OpenSSH -L/-D/authorized -R, strict upstream host key |
| MTProto | E/U external mtg-multi; migration N | E/U secrets/ad-tags | N | preserve features, move execution into core |
| TUIC v5 | E/U external tuic-server; migration N | E/U | N | preserve QUIC/UDP, remove panel relay after parity |
| AmneziaWG | E/U panel-side runtime; migration N | E/U peers/forwarding | N | preserve obfuscation/IPv6/per-peer data, direct dispatcher |
| Freedom/direct, block, DNS, loopback | E/U outbound | NA account service | finite local loopback TCP/UDP metering, sniffing, quota/disable and explicit cycle rejection I/V; full core and affected panel regressions pass | full DNS/balancer/chain correctness and network-listener loop detection |

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

Authenticated-account increment: VLESS/VMess/Trojan/classic Shadowsocks AEAD server accounts can carry the trusted `clientId`. Eight real loopback cases exercise TCP and UDP with and without Mux, alongside an owned Tunnel, exact multiplier boundaries, a shared upload bucket and live disable. Handler mutations require a private endpoint and the appropriate per-protocol capability before changing the core. A real Runtime VLESS rotation changes credential/email while preserving the existing policy and usage. These tests use the existing codec implementations as peers; they do not establish Vision, every cipher/transport or independent official-client interoperability. Automatic DB-to-account binding and complete lifecycle/UI remain N. Managed Shadowsocks 2022 is explicitly rejected pending repair of its mutable user table and authentication-context lifetime; legacy unmanaged 2022 configurations remain supported.

Tunnel owner increment: existing client services accept a credential-free listener owner. Transactional full sync, delta and add reject a second distinct owner, including when the existing owner is disabled. Create, rename across two rules, detach, replacement and concurrent attachment tests pass on SQLite and PostgreSQL while preserving stable client records and shared usage. This is database/service evidence; unowned legacy-rule migration, compiled identity activation and the complete forwarding UI remain unfinished.

Managed compiler increment: database stable IDs replace supplied identity fields; Tunnel ownership, known authenticated adapters, private control and durable policy versions form a validated candidate. Real generated TCP/UDP Tunnel traffic settles into SQLite and PostgreSQL, and 1001-client tests cover all policy batches. Unsupported or unresolved candidates fail explicitly. Ordinary production startup, legacy usage cutover, internal management listeners, resets/first-use expiry and runtime configuration fencing remain unfinished; this does not upgrade the complete activation or UI rows.

Quota-window prerequisite: core policy/config/private RPC and panel process activation now preserve an exact effective billed baseline. Scoped tests cover fractional reset without lifetime counter loss, other restriction reasons, stale/future baselines, failure before/after commit, legacy seed retries, corrupted recovery and real Tunnel transfer. Full panel reset/renew/scheduling and period statistics remain unfinished; this does not upgrade those lifecycle rows.

SQL reset-intent increment: SQLite/PostgreSQL persist an exact committed reset boundary together with the desired policy version. Concurrent, duplicate and delayed requests preserve one current window and all lifetime accounting. Insert-time and PostgreSQL commit-time failures roll back the request and version. Cross-database migration preserves reset history and supports older schemas that lack it. This is an internal preparation path; normal reset/bulk/scheduler endpoints, Runtime retry orchestration and period UI statistics remain unfinished.

Runtime reset increment: an internal local reset service checkpoints before recording a boundary and applies only committed requests. Normal traffic polling retries a pending reset without recapturing later usage, including one in the last batch of 1001 configured clients. Real-child tests cover SQL/control failures, newly remote ownership rejection, delayed requests, disabled stream termination and restart recovery. Public reset/bulk/scheduler wiring, period statistics and automatic activation remain unfinished; this does not mark the full reset lifecycle complete.

Accounting view increment: existing client statistics, client traffic cells and inbound client details expose exact lifetime/acknowledged-period raw, billed and uncertain amounts. Pending reset versions stay visible until receipt acknowledgement. SQLite/PostgreSQL cover the existing read surfaces, fractional precision, rename/reuse, transaction failure and all 1001 snapshot clients; a real child covers lost-control recovery and restart. This does not upgrade legacy write/reset/renew flows, paging summaries/filters, aggregate badges, production activation or any additional protocol row.

Single-client reset increment: existing client/inbound service entrypoints route prepared identities through managed Runtime reset. HTTP callers may supply stable identity plus a retry key; the browser preserves that key until success. Stopped/prepared protection and transactional identity/seed rechecks cover legacy zeroing. Bulk/all/scheduled flows, later legacy activation fencing and normal production startup remain open.

Batched reset increment: manual bulk/all HTTP and inbound-client service resets persist original membership, use one managed checkpoint and atomic SQL boundaries, preserve later windows on retry, and retain legacy compatibility. The real 1001-client test exercises second-batch SQL/Runtime failures, polling recovery, mixed legacy clients and rename/recycled-email recovery. Scheduling still needs a deterministic calendar request shared across overlapping inbound/client cycles; renewal, activation fencing and production startup remain unfinished.

Scheduled reset increment: the existing periodic job uses durable calendar membership, bounded managed batches, monotonic reset ordering and normal-poll recovery; duplicate/old tasks preserve later usage and independent restrictions. Legacy node propagation remains best effort. This does not establish automatic managed activation, renewal, global node budgets or full lifecycle completeness.

Managed local renewal is implemented for already-activated identities, including finite catch-up and recovery of committed expiry-only versions. Legacy writers no longer override these identities. This scoped result does not establish durable first-use activation, ordinary startup activation or coordinated node renewal.

First-use expiry increment: a negative duration starts at the first admitted
payload, persists before forwarding and survives an abrupt core exit. Private
ledger/state replies carry that timestamp. Panel settlement preserves newer
operator edits and manual disable, and normal polling applies the absolute
deadline. Real Tunnel/child-restart and SQLite/PostgreSQL checks are recorded in
testing.md. Ordinary managed startup, global allocation and remaining protocol
adapters are still unfinished.


Ordinary managed lifecycle increment: explicit managed templates now use the normal
restart and local mutation entrypoints. Real VLESS/Tunnel fixtures verify shared
identity, sibling-flow preservation, credential/listener removal ordering,
control/compiler/bind-conflict failures, stopped-core queuing, live-legacy refusal
without preparation side effects and Reverse clearing. SQLite/PostgreSQL exercise
single/bulk error propagation, writer reentry and stale traffic plans. The full
independent gate and original configuration scale cases pass (testing.md).
This scoped evidence does not complete automatic activation, healthy legacy
handoff, all protocol lifecycle combinations,
complete UI application status, backup rollback fencing or global node budgets.


Permanent deletion increment: single/bulk and both orphan hard-delete entrypoints
persist stable-ID intent and route local managed revocation through Runtime.
Startup recovery, never-initialized identity fences and bounded history traversal
are implemented. This does not upgrade global node revocation, all protocol
combinations, automatic activation or backup rollback fencing. Verification is
recorded in testing.md.


Local policy form and remote-boundary increment (2026-09-30): ordinary client
add/edit controls for whole-byte upload/download limits and an exact decimal
billing multiplier are I/V for local bindings. Legacy omission, explicit defaults,
clearing overrides and invalid-input correction have real component/schema tests.
Remote or unresolved bindings are read-only, and staged local policy edits cannot
be silently discarded when a remote listener is selected. Backend preflight and
locked SQL membership guards reject unsupported remote policy creation/changes;
node mirrors validate their saved raw settings before identity filtering. Existing
remote metadata and revocation compatibility remain, without claiming coordinated
remote budgets, rates, stable identity transport or managed application receipts.
Confirmed accounting's saved-but-unprepared pending indication is I/V, including
real compiler failure and equal prepared/acknowledged versions. First activation
without a receipt, complete listener application status, Tunnel owner selection,
bulk policy controls and coordinated multi-node enforcement remain unfinished.

The local owner picker checkpoint covers stable-ID selection, legacy nullable settings, single-inbound import, former-owner history, local-only scope before first activation, and real same-port TCP/UDP reassignment with a surviving sibling. It does not complete Tunnel ACLs, every routing mode, global node budgets or migration of existing unowned rules.

Standalone client creation is I/V for the existing form/API, atomic identity and
disabled-state persistence, duplicate rejection and the first owned Tunnel on an
empty panel. SQLite and PostgreSQL real TCP/UDP tests retain the generated stable
ID and settle exact multiplier-adjusted usage. It does not complete the remaining
Tunnel ACL, forwarding-mode or unowned migration requirements.


Local Tunnel source ACL increment: raw TCP/UDP and ordinary TCP TLS check native
IPv4/IPv6 CIDRs against the physical peer before dispatch and billing. Empty is
unrestricted; PROXY, other transports, masks and Unix listeners are rejected
when configured. The form/API, typed construction, capability negotiation,
atomic validation and hot narrowing are I/V within this local scope. Full
make verify/race and core unit/scoped race gates pass; results are in testing.md. This does not establish arbitrary
transport ACLs, transparent interception, complete forwarding modes or global
node policy.

Local Tunnel fixed outbound increment: the existing form/API carry
`settings.outboundTag`, with empty/null/omitted selecting ordinary routing and
balancers. Concrete template/subscription selection, owner/enable validation,
missing-selection retention, independent capability negotiation and listener-
scoped replacement are I/V for local owners. Real TCP/UDP tests cover selected
freedom, SOCKS, loopback, HTTP TCP and HTTP UDP refusal; missing/block selections
cannot send payload through the direct default. A valid internal mux-frame
payload reaches the selected echo target unchanged and is counted exactly once.
Legacy unowned Tunnel without a fixed selection retains its internal mux gateway;
a real New frame reaches its inner target without charging an unrelated owner.
SQLite/PostgreSQL hot updates preserve the child boot, sibling flows, stable ID
and exact lifetime ledger. The frontend query refreshes after background outbound
subscription invalidation. This does not establish every forwarding/network
mode, unowned-rule migration, remote policy, other requested protocol adapters
or a fully single-core installation.

### Tunnel outbound selector acceptance cells

These cells describe this increment, rather than upgrading all Xray routing or
all protocol rows from the narrower fixtures.

| Direction / feature | Status | Evidence or remaining requirement |
| --- | --- | --- |
| Inbound: fixed selected freedom, TCP/UDP | I/V | Distinct selected/default echo targets; exact shared policy totals |
| Inbound: select SOCKS, TCP/UDP | I/V | Real in-process Mixed proxy hop; no default-target payload |
| Inbound: select HTTP, TCP | I/V | Real CONNECT proxy hop |
| Inbound: select HTTP, UDP | NA for that outbound | HTTP adapter rejects UDP; test observes no silent direct fallback |
| Inbound: select loopback, TCP/UDP | I/V | Forced first hop is consumed; subsequent virtual-inbound routing works; billing once |
| Inbound: missing concrete tag or blackhole | I/V | Fixed/inherited refusal controls observe zero forbidden target bytes |
| Inbound: owned/selected opaque TCP payload at internal mux address | I/V | Valid 14-byte frame remains payload, 14/14/42 usage for owned flows, inner target untouched; unowned fixed selection/block/missing also enforced |
| Inbound: legacy unowned TCP internal mux gateway | I/V scoped | No fixed selection; real New/data frame reaches the inner echo target and returns a valid mux response; unrelated owner usage stays zero |
| Inbound: normal routing / balancer mode | I/V scoped | Clearing returns to routing; preceding balancer failure/fallback tests; exhaustive routing remains open |
| Inbound: local CRUD/enable and canonical ownership | I/V | SQLite/PostgreSQL atomic save, disabled/re-enable checks, retained removed-subscription selection |
| Inbound: hot listener lifecycle and accounting | I/V | Real TCP/UDP replacement, child boot unchanged, surviving sibling, exact committed ledger |
| Inbound: startup / handler API compatibility | I/V | Capability-omission executable and handler probe refuse before preparation/mutation |
| Inbound: existing editor / saved settings / tag refresh | I/V | Real form/adapter/query tests, unavailable tag stays visible, actual WebSocket invalidation |
| Inbound: remote fixed selection/global policy | N | Remote selection remains read-only; backend rejects unsupported local-owner scope |
| Inbound: all remaining address/transport modes | I/U or E/U | Fixed-outbound tests here use IPv4 raw TCP/UDP; separate ACL IPv6/TLS evidence does not prove every fixed-outbound mode |
| Outbound: Tunnel as a proxy protocol | NA | Tunnel is a forwarding listener, selects existing outbounds |
| Distribution / whole-system single core | N | Original installers/sidecars and panel-side AmneziaWG remain; no process/deployment parity claim |

Password owner UI checkpoint: actual Mixed/HTTP modal, schema, paged client
queries and API contracts are verified. Full database backup/migration restores
canonical UUIDs, memberships, credentials, policy and history. Single-inbound
JSON resolves existing destination owners and rejects unknown owners or supplied
traffic; foreign-owner remapping is N. Generic lifecycle and live username-based
legacy handoff remain N. See testing.md for the complete split validation gate,
including the retained default-worker timeout.


Password-owner removal increment: single/bulk ClientService Delete and Detach
are I/V for local Mixed/HTTP explicit owners. UUID grouping preserves other
owners, protected empty auth, history and shared ledger. Whole-graph preflight,
SQL row-lock rebasing, final identity/alias fences and uncertain runtime
stop/recovery are covered on SQLite and PostgreSQL with real-core sockets.
Shared client updates and live legacy alias-counter handoff remain N; full
protocol lifecycle and single-core migration are still incomplete.


Single password-owner Update increment: canonical persistence, filtered shared
name propagation, current omitted fields, destination isolation, history,
full graph/Tunnel validation and saved-command recovery are I/V for local
explicit Mixed/HTTP owners. Real TCP Update tests preserve resource passwords,
other-owner flows, stable UUID and exact shared ledger; rate settings are
acknowledged, without a fresh quantitative shaping claim. Own Tunnel rename
replaces its accounting label and closes/reconnects that flow. Existing
ordinary partial-success semantics remain. Subscription IDs are intentionally
nonunique; independent raw-SQL acquisition after the final check is not covered.
Bulk/by-email lifecycle, live legacy alias-counter handoff, all remaining
protocol adapters and complete single-core installation stay N.
