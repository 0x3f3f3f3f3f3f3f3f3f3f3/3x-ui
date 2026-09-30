# Single Custom Xray-core implementation plan

> Execution: superpowers:executing-plans, inline in the feature branch. Use test-driven-development for behavior changes and verification-before-completion before claims or commits.

**Goal:** Complete all requirements in [requirements.md](requirements.md), including panel, core, protocol interoperability, persistence, existing data-plane migration, distribution and verified pushes.

**Architecture:** One panel/control process and one Custom Xray-core data-plane process per node. All authenticated accounts and owned listeners resolve to a stable client ID; decrypted payload passes through a common policy engine before Xray routing. The panel owns configuration and long-term accounting; the core enforces allocated budgets and keeps recoverable execution state.

**Stack:** Existing Go/Gin/GORM SQLite/PostgreSQL panel and React/Ant Design frontend; managed Xray source, Go protocol libraries, existing gRPC control transport.

**Spec:** [requirements.md](requirements.md), [architecture.md](architecture.md), [accounting.md](accounting.md).

## Global constraints

- Start from main `17d7dd46b512d0a9c22921a6094f30c672e436c9`, never from `feat/unified-client-policy-backends`.
- Work and push only `feature/custom-xray-unified-policy`; leave existing branch tips unchanged. No releases, merges, production deployment or force pushes.
- Preserve existing functionality, credentials, data, upstream attribution, and supported platforms.
- No extra business proxy process, panel-side decode/bridge, source-IP identity, or independent routing subsystem.
- Missing capabilities fail explicitly. Unsupported, unimplemented and unverified are distinct.
- Rate changes affect existing sessions within 2 seconds. Multipliers 0.5/1/1.5/2/10 require exact fixed-point accounting.
- Use the same managed core module in panel, CI and runtime builds. No module-cache patches.
- Every task updates testing and capability records and makes a logical commit; push significant validated stages and compare remote SHA.

## Review focus

1. Existing main contains MTProto/TUIC sidecars and panel-side AmneziaWG: task 10 must preserve their behavior while moving execution.
2. Cached/sniffed/zero-copy/mux data could bypass or double-charge: tasks 3 and 5 must exercise these paths.
3. A crash between budget allocation and settlement can reissue quota: task 4 must inject crashes and replay/out-of-order reports.
4. A listener reassigned to another client must never transfer live sessions/counters: tasks 3 and 6 test deletion/reassignment under load.
5. Global policies cannot be copied in full to each node: task 12 tests two nodes and disconnection/recovery.

## Task 1: Audit and reproducible source baseline (in progress)

Files: `docs/custom-core/*`, `core/xray/*`, root `go.mod`, `tools/build-custom-core.sh`, `.github/workflows/custom-core.yml`.
Produces: committed source at the existing panel's core pin, a reproducible `build/custom-xray` and immutable provenance.

- [x] Read user requirements, repo guidance, main source, build/test scripts and DB models.
- [x] Verify clean checkout and create a new branch directly from main.
- [x] Verify upstream releases, fork ancestry, SSH authentication and library candidates.
- [x] Import original core with license and origin manifest; point panel module replacement at it.
- [x] Build panel, frontend and custom core; record baseline failures without claiming full validation.
- [x] Commit source import separately from implementation; push and verify remote SHA.

## Task 2: Policy arithmetic and runtime state

Files: `core/xray/app/clientpolicy/{config.proto,policy.go,accounting.go,limiter.go,engine.go,*_test.go}`.
Produces: `Engine.Apply(Policy) error`, `Engine.Open(context.Context, Metadata, func()) (*Session,error)`, `Session.Admit(direction,bytes) error`, `Engine.Snapshot(clientID)`.
Policy values use uint64 byte rates, an explicit burst, multiplier millionths and monotonic version; client identity is a nonempty opaque server-owned string. Admission is serialized per client for atomic quota, separate upload/download token buckets and connection registry. Metadata retains inbound, authenticated account, session ID, original/actual target and active policy version.

- [x] Write failing tests for fractional/batch-invariant accounting, multiplier switch, overflow and invalid fields.
- [x] Implement fixed-point arithmetic and inspect passing tests, including property/fuzz seeds.
- [x] Write failing concurrent quota and restriction-composition tests; implement policy state and cancellation outside locks.
- [x] Test two rates, unlimited control, concurrent streams and engine hot updates (remaining fairness/real-socket hot-update gates recorded).
- [ ] Commit tested runtime primitives; do not call this a delivered data plane until task 3 passes.

## Task 3: Real Tunnel data path

Files: `core/xray/proxy/dokodemo/*`, `common/protocol/user.*`, `common/session/*`, `app/dispatcher/*`, `infra/conf/*`, `testing/scenarios/*custom*`.
Consumes task 2. Produces trusted `client_id` in runtime user/session context and enforcement wrappers around decrypted payload.

- [ ] First write a failing real TCP/UDP fixed-target test that exceeds a shared client budget through two listeners.
- [x] Extend protobuf with new field numbers; retain old fields and names; reject missing policy for managed identities.
- [x] Bind configured Tunnel listener identity, meter both directions once and guard managed raw-copy; panel ownership lifecycle remains task 6.
- [ ] Verify direct, selected proxy and block route, domain/IPv4/IPv6, half-close, timeout, UDP boundaries, active disconnect and no NAT dependency.
- [ ] Verify independent socket-byte observations, 100 MiB quota at multiplier 2, last-budget contention and nonzero rate tiers.
- [ ] Commit and push measured evidence.

## Task 4: Recoverable accounting and control API

Files: `core/xray/app/clientpolicy/{store.go,command/*}`, `infra/conf/api.go`, `main/commands/*`, panel `internal/xray/*`.
Produces: capability v1, versioned policy/connection/event APIs and durable reservation state consumed by tasks 6/12.

- [ ] Test crash before/after reservation commit, counter reset, duplicate/out-of-order events and DB failure.
- [ ] Implement bounded durable budget reservations, unique instance/epoch/sequence, recoverable settlement and fail-closed I/O errors.
- [ ] Preserve all old gRPC fields/services; use protected local transport and existing authenticated node transport.
- [ ] Test capability mismatch against official/older core and transactional config validation/recovery.
- [ ] Measure persistence overhead and specify crash uncertainty before marking durability complete.

## Task 5: Existing Xray protocol identity and fast paths

Files: core inbound account parsers, mux, dispatcher, Vision/XTLS, routing context; panel account mapping.

- [ ] Write credential-rotation and cross-protocol aggregation regressions before extending trusted account mapping.
- [x] Scoped VLESS/VMess/Trojan/classic AEAD TCP/UDP/Mux and Tunnel aggregation, shared upload bucket, multiplier boundary, disable, and private Runtime VLESS credential rotation.
- [ ] Cover VLESS/VMess/Trojan/Shadowsocks/Mixed/HTTP/Hysteria/WireGuard/TUN as applicable; distinguish IP-packet accounting.
- [ ] Audit nested dispatch, loopback, mux/XUDP, sniff cache and raw/splice/Vision for bypass/double charging.
- [ ] Test authenticated client-ID routing, original versus rewritten target, DNS/balancer/block and loop detection.
- [ ] Report managed versus upstream throughput and leave unmanaged optimizations intact.

## Task 6: Panel vertical integration

Files: `internal/database/{db.go,model/*}`, `internal/web/{service,controller,runtime}/*`, `internal/xray/*`, `frontend/src/{schemas,pages/clients,pages/inbounds,pages/api-docs}/*`, translations and generators.

- [ ] Migrate SQLite/PostgreSQL legacy clients to stable IDs, preserve usage, default multiplier 1/unlimited rates; test rename/rotation/import.
- [ ] Bind existing client records to forwarding rules, node, ACL, outbounds and exclusive listener resources.
- [x] Scoped managed candidate compiler: authoritative database identities, private control, strict adapter/owner validation, durable policy batches and real generated Tunnel ledger. Local automatic activation is covered below; remote managed activation remains open.
- [x] Scoped Tunnel attachment ownership: credential-free create/attach/rename, single-owner transactional sync/delta validation, concurrent attachment exclusion and explicit reassignment preserve stable client records.
- [ ] Extend Runtime lifecycle, batch operations and state/reason/statistics UI, API registry/codegen and all locale keys (English/Chinese translations).
- [ ] Test wildcard/dual-stack/control-port collision, reassignment, reset/renew restrictions and active connection termination.
- [x] Core prerequisite: drain established TCP/UDP/Unix connections on inbound removal; test same-port Tunnel reassignment and unaffected sibling listeners.
- [x] Scoped local allocation fence: serialize remote attachment against local policy preparation; coordinated global allocation remains open.
- [x] Scoped legacy collector: atomic inbound/client/outbound settlement, lost-commit-ack deduplication, first-use rollback and SQLite/PostgreSQL receipt migration.
- [x] Guarded live legacy handoff: boot-owned final snapshot, verified executable, stable ownership and SQL settlement before seeding; durable intent rejects interrupted handoff after loss of the original panel snapshot. Automatic selection is covered by the checkpoint below; general legacy migration remains open.
- [ ] Test end-to-end DB → API → UI → generated config → measured traffic → durable events → statistics.

## Task 7: SSH inbound and outbound

Files: core `proxy/ssh`, config/control and existing panel protocol forms/export.

- [ ] Standard OpenSSH tests for -L/-D and opt-in controlled -R, wrong/revoked keys and denied shell/exec/subsystems.
- [ ] Implement in-process SSH using Go SSH, separate persistent host keys, bounded handshake/channels/listeners and strict outbound host verification.
- [ ] Route direct channels through Dispatcher; meter reverse channels with explicit direction and honest target visibility.
- [ ] Verify shared policy over multiple connections/channels, listener cleanup, export and API/UI lifecycle.

## Task 8: mieru inbound and outbound

Files: core `proxy/mieru`, source-pinned library adapter, panel protocol/config/export paths.

- [ ] Audit embedded Accept/authentication context and all target dial paths at pinned version; prevent independent dialing.
- [ ] Test official client TCP/UDP, mux and deletion of active users/listeners before completing adapter.
- [ ] Use one unified quota source, trusted username mapping, route selection and policy wrappers; integrate full panel lifecycle.

## Task 9: Snell v4, v5 and v6 separately

Files: core `proxy/snell`, necessary managed library adaptation, panel version-specific config/export.

- [ ] Audit OpenSnell source/license and independently pin each compatibility target.
- [ ] Implement v4 inbound/outbound with TCP, UDP, reuse and trusted PSK/listener mapping.
- [ ] Implement/test v5 independently including QUIC Proxy Mode; no substitute TCP/UDP-only claim.
- [ ] Implement/test v6 beta modes against fixed official client/server combination; no external-server runtime fallback.
- [ ] Maintain separate self-test versus official Surge interoperability evidence; if commercial client unavailable supply exact external procedure and leave unverified.

## Task 10: Migrate original non-Xray business paths

Files: `internal/{mtproto,tuic,amneziawgnet}`, their core adapters, supervision/config/import paths.

- [ ] Audit mtg-multi/TUIC/AmneziaWG libraries and license/version/source; preserve secrets, ad-tags, QUIC/UDP, forwarding and IPv6 features.
- [ ] Move protocol handling into core; replace target dialers with shared routing/policy and remove panel-side business bridging only after parity tests.
- [ ] Test legacy data migration, active lifecycle, multi-client statistics and process tree showing only one business core.

## Task 11: Export, notifications, backup and recovery

- [ ] Correct real client formats in all existing backend/frontend/docs export implementations; retain unsupported-format errors.
- [ ] Extend bulk/API, online state, connection controls, notifications and node snapshots.
- [ ] Test full DB and core-state backup, import/export, restore fencing, replay protection and upgrade/rollback.

## Task 12: Multi-node budgets and rates

- [ ] Define global and node-scoped policies in model/API/UI; test two nodes sharing one global client.
- [ ] Transactionally allocate nonoverlapping budget/rate leases; never issue each node the full global allowance.
- [ ] Test panel outage, expiry, loss/recovery, node deletion and duplicate reports without reapplying multiplier.

## Task 13: Installation and distribution

- [ ] Build panel/core from one clean clone; pin all toolchains, tags and dependencies.
- [ ] Update Docker, install/upgrade and release build matrices with source/compatibility/checksum manifests.
- [ ] Prevent silent official-core replacement and expose unsupported platform capabilities accurately.
- [ ] Validate packages without deploying or publishing a release; document backup and rollback.

## Task 14: Full acceptance and final delivery

- [ ] Complete requirements A–H, including race/static/frontend/DB/fuzz, fault/leak/performance, real clients and clean rebuild.
- [ ] Review every requirement and matrix cell against current authoritative evidence; skips never count as passes.
- [ ] Fresh whole-branch review, fix findings, final logical commits and exact remote SHA verification.
- [ ] Final report includes all twelve requested delivery items and remaining unverified evidence; goal stays active until actual requirements are satisfied.

## Execution checkpoint — 2026-09-28

Source import commits `d50ce235` and `23f7c784` pushed and remote SHA verified. All 1039 original upstream blobs were checked against the Git index. The first import exposed inherited `main`, `debug.*`, and `.dat` ignore rules; the second commit restores those files and preserves Windows CRLF bytes. No history was rewritten. At that push verification, remote main was `17d7dd46` and the existing backend branch was `5f51dccb`. Only the new feature ref was pushed; other branches may be changed independently by their owners.

Runtime policy and Tunnel integration now exist and pass targeted race and real socket tests; exact evidence is in testing.md. Local durable quota recovery is implemented with bounded frozen uncertainty. This is a partial stage 2: panel committed-event settlement, identity migration/forms, ACL, original-target metadata, all other protocols and sidecar migration remain unfinished. Local restart tests do not establish backup rollback fencing or cross-node consistency. Native execution remains authorized; no merge/release/deployment requested or performed.


Further checkpoint: `3c22da60` (managed builds) and `db578b7b` (Tunnel policy) pushed; remote feature SHA matched `db578b7bbdc5c8a52c414d0c1af2689172e9dd54`. Remote main still `17d7dd46`; the existing backend branch had independently advanced to `6d6baf6e`. No command wrote that branch.

Task 4 partial implementation: synchronous bounded reservations, stable store identity/epochs/sequences, exact graceful recovery and conservative abrupt recovery, atomic policy batches, explicit initialization and failed-constructor cleanup. The protected control API and capability-negotiating panel adapter are now implemented; this does not complete panel ledger/replay, snapshot fencing, multi-node allocation or full configuration-start rollback gates.

Task 4 API checkpoint: protected Unix gRPC v1, coherent committed-cumulative receipts, pagination, guarded policy/revocation/connection controls and panel capability adapter pass scoped race/integration checks. End-to-end DB settlement/restore fencing remain open; Task 4 is not complete.

Task 4/6 checkpoint (2026-09-29): stable ClientRecord UUID migration and transactional receipt settlement are implemented; focused SQLite/PostgreSQL race tests pass, including PostgreSQL commit-time failure and legacy cross-database migration. Create-only core usage seeding and real Tunnel→private API→panel DB accounting across a core restart are verified in both databases. Production Runtime/config/UI activation, portable identity export, node reconciliation, scheduled settlement and restore fencing still require implementation; tasks 4/6 remain open.

Runtime bootstrap checkpoint: private state provisioning and Local Runtime database preparation pass real child-process restart tests on SQLite and PostgreSQL. The path rejects missing activated state and a core ledger behind the panel cursor. It preserves the DB-writer/Runtime lock order. Production configuration, legacy cutover, polling/projection and UI remain open; this does not complete task 6.

Task 6 policy-edit checkpoint: optional settings survive legacy client edits and attached settings export; bounded desired batches use durable monotonic versions derived from enforcement fields. The normal client-edit service dispatches changes for already activated local clients through Runtime. Real child-process tests cover live rate/multiplier changes and manual disable. General activation, all lifecycle entry points, first-use expiry/reset windows, projection and UI remain open.

Task 6 collection checkpoint: the existing traffic polling entrypoint now collects committed ledger pages through Runtime and resumes from the SQL cursor after failures. Cursor validation occurs before checkpointing, and the core avoids rewriting unchanged clients. Automatic owner/config activation and the legacy raw/billed statistics, reset and expiry cutover still remain open.

Task 5 partial checkpoint: four authenticated account adapters now preserve server-side client identity and advertise individual capabilities. Handler mutations verify private transport and required capabilities before changes. Tests cover TCP/UDP/Mux payloads sharing a Tunnel policy, a live shared rate change, exact historical billing and Runtime credential rotation. Vision, the other account families, full panel-generated identity binding, account deletion/bulk lifecycle and production activation remain open. The Shadowsocks 2022 audit found mutable table/index and authentication-context lifetime concerns; managed 2022 is rejected until its adapter is repaired and verified.

Task 6 owner checkpoint: the existing client_inbounds relation now accepts a credential-free Tunnel owner and rejects a second distinct client, including a disabled existing owner. SQLite/PostgreSQL service tests cover normal create/attach/rename/detach/replacement and failed/concurrent mutations. This establishes database ownership, not automatic managed configuration, legacy unowned-rule migration, live service reconciliation, ACL or complete UI support.

Task 6 compiler checkpoint: a separate managed candidate compiler binds enabled local database listeners and authenticated accounts to stored stable IDs, validates the complete core configuration before preparing durable policy versions, and retains credentials behind core restrictions. Two generated Tunnel rules settle real TCP/UDP traffic through Runtime into SQLite and PostgreSQL. A 1001-client test covers every policy batch and an update in the last batch. This method is not yet wired into ordinary RestartXray; legacy counter handoff, reset/expiry semantics, runtime mutations and configuration revision fencing remain open.

Task 6 quota-window prerequisite: versioned exact billed baselines preserve lifetime ledger values and independent restriction reasons. JSON/protobuf/private RPC and negotiated child startup carry the baseline; unsupported cores reject it explicitly. Period baselines in SQL, durable reset intent, reset/renew entrypoints and statistics projection still require integration.

Task 6 SQL reset checkpoint: append-only request records preserve committed receipt boundaries and make duplicate/delayed preparation idempotent. The desired-policy compiler retains the latest window across ordinary reconciliation. Both databases cover version/request atomicity, restriction preservation and migration. Runtime application, public reset/bulk/scheduled flows, periodic traffic projection and automatic activation remain open.

Task 6 Runtime reset checkpoint: explicit internal reset orchestration commits SQL before Runtime application, while normal polling retries pending versions without granting a new window. Tests cover real control loss, transaction failure, restored process configuration, post-request remote attachment, disabled connection termination, restart recovery and the last batch of 1001 clients. Next: period usage projection and legacy single/bulk/scheduled entrypoint cutover, then automatic managed startup with a verified historical counter handoff.

Task 6 accounting view checkpoint: existing client statistics APIs and client/inbound detail traffic cells now distinguish acknowledged period billing from cumulative raw/billed/uncertain usage, preserve exact wire amounts, and show pending resets. Stable-identity lookup and atomic unattached rename prevent history from following a recycled email. Legacy quota writes, reset scheduling, paging filters/summaries and automatic activation remain open.

Task 6 single-client reset checkpoint: manual entrypoints now route prepared stable identities through Runtime; the existing HTTP endpoint accepts identity-bound request keys and the browser retains them across uncertain replies and remounts. Transactional guards prevent queued legacy zeroing after seed capture or email replacement. Continue with bulk/all/scheduled resets and renewal, then finish lifecycle/configuration fencing before enabling ordinary managed startup.

Task 6 batch reset checkpoint: original membership and completion are durable for bulk/all/inbound-client operations. One managed checkpoint precedes atomic SQL reset preparation; bounded Runtime batches and normal polling finish committed requests. The existing all-client browser action retains its request key until success. Continue with deterministic scheduled reset deduplication, renewal, legacy enforcement cutover and activation/configuration fencing; Task 6 is not complete.

Task 6 scheduled-reset checkpoint: the ordinary cron entrypoint now captures one calendar operation with immutable stable-client/inbound membership, coalesces overlapping cycles, preserves newer reset boundaries, and resumes pending local managed operations fairly from normal polling. Existing legacy node calls remain bounded best-effort propagation. Continue with renewal, legacy first-use/quota enforcement cutover and activation/configuration fencing before enabling ordinary managed startup; Task 6 remains open.

Task 6 renewal checkpoint: normal polling renews already-managed local clients from durable receipt boundaries, preserves manual disable/lifetime usage, and retries expiry-only desired versions after control loss. Legacy raw-quota/expiry/renewal writers now exclude prepared and active managed identities. Durable first-use activation and lifecycle/configuration fencing remain prerequisites for ordinary managed startup.

Task 6 first-use checkpoint: the core persists the first admitted payload time
before forwarding and enforces the resulting deadline across crash/restart.
Version-checked SQL settlement converts the duration atomically and normal
polling applies the absolute expiry without changing manual restrictions or
lifetime usage. Next: ordinary managed activation, historical counter handoff
and configuration/lifecycle fences; Task 6 remains open.


Ordinary managed lifecycle candidate (2026-09-29): normal RestartXray and local
client/inbound mutation paths now route an explicitly managed configuration
through private capability negotiation, durable preparation and canonical
configuration reconciliation. Hot changes revoke removed credentials/listeners
before granting updated policy. Post-commit compilation or bind-conflict failure
closes the old managed process; a manual invalid-candidate restart preserves it.
Traffic lifecycle and single-client reset Runtime calls run outside the SQL
writer; delayed legacy plans revalidate current bindings and credentials under
the inbound mutation lock. Config export does not consume lifecycle maintenance.

The focused fault, real-child, Reverse and writer regressions pass and source
review found no remaining blocker in this increment. The isolated SQLite/PostgreSQL race, Runtime/API race, lint, full panel, build
and original 10,000/100,000-client configuration gates passed; this checkpoint
does not mark task 6 complete. Automatic selection
from client policy, healthy legacy drain/settlement, UI application status,
complete restore fencing and global budgets
remain open. A running legacy process is rejected before policy preparation
until its final accounting boundary can be proven.


Permanent deletion candidate: four hard-delete entrypoints now commit durable
stable-ID intent, revoke through Local Runtime and retain lifetime accounting.
Startup pages pending work before listeners open; confirmed unknown identities
have separate absence metadata and stale candidates cannot initialize them.
SQL/control failure stops a running managed process and recovery is idempotent.
Source review and final database, migration/backup, race, lint, full panel and
build checks are complete; testing.md records the PostgreSQL fixture failure and
its repeated successful rerun. Task 6 and the overall goal remain open.

Task 6 legacy handoff prerequisite: manager-owned IO leases now fence all audited
traffic counter paths, including asynchronous reads, UDP, Vision and raw splice.
Timeout/retry, partial errors, nested admission and immutable snapshots pass race
regressions; full core/panel suites, lint and builds pass. This does not expose a
drain RPC or permit live legacy activation. IO-owner cancellation, private
boot-scoped acknowledgement and atomic final settlement remain required.

Task 6 mux cancellation candidate: TCP mux and XUDP pool closure now seals new
workers and cancels proxy contexts, including late creation and concurrent-close
races. Real VMess traffic exposed a byte Write accounting bypass; explicit byte
Write/Read methods now participate in the counter boundary. Focused race tests,
source review, full core/panel suites, lint and builds pass. Expanded race checks
also exposed and verified a fix for an existing outbound tag-cache race. Ordinary outbound
socket cancellation and the remaining handoff integration are still open.

Task 6 ordinary outbound candidate: handlers own direct dispatch/dial contexts
and returned sockets, reject admission after Close and clean up normal/late
connections. Native buffer adaptations remain intact. Reviewed VLESS preconnect
and delayed WebSocket shutdown defects have regression fixes. Scoped package
race checks and complete regression gates pass after correcting a mux integration
test to inspect final sealed counters; testing.md records the initial failure
and rerun. Manager lifecycle, remaining transport close gaps and boot-scoped final
settlement remain open.


Task 6 transport cancellation candidate: realm and HTTPUpgrade socket closure,
XHTTP logical request ownership, shared HTTP/2/3 dial cancellation, raw H1 IO,
TLS/QUIC error cleanup and explicit QUIC connection ownership have regression
coverage. Source review found and resolved shared-client health, native retry,
retired-generation ownership and late-result issues. Affected package race checks
and complete core/panel gates, lint and builds pass. Manager lifecycle, idle raw
pools, remaining transports and boot-scoped final settlement remain open.


Task 6 manager ownership checkpoint: Close fences admission and selection, closes
handlers outside lookup locks and retains removed handlers through completion.
Failed additions are not published and rejected construction is cleaned up.
Reviewed VLESS reverse interactions now use atomic nondefault registration,
conditional identity removal and owned static-mux closure. Focused and expanded
race checks pass after extending the core payload-comparison test timeout; source
review is complete. Complete core/panel suites, expanded race checks, lint and
both builds pass. Boot-scoped drain/settlement,
remaining transport ownership and the overall goal remain open.


Task 6 boot-scoped drain checkpoint: a separate private control API shares one
instance boot ID and irreversible drain operation, waits for manager closure and
counter IO, and exposes bounded immutable final pages. Cancellation, failure,
100001-user pagination and real TCP/UDP/mux tests pass. Complete core/panel
suites, race checks, lint and builds pass. Panel-owned boot pinning, atomic final
settlement and activation
integration remain required before enabling healthy live legacy handoff.


Task 6 process settlement checkpoint: explicit private control pins Linux peer PID
and boot at startup, and final settlement replays uncertain batches before a fresh
frozen-counter delta. Retry IDs and snapshots survive SQL failures; each new child
clears that state. Source review, complete internal/xray race and panel suites,
lint and panel build pass. Automatic endpoint provisioning, shared SQL
receipt service, pure activation preflight and lifecycle cutover are still open.


Task 6 automatic control checkpoint: Linux ordinary custom children receive a
per-child private control endpoint selected by a dedicated binary configuration
hint. Numeric version parsing and older Custom/upstream startup remain intact.
PID/boot verification is unchanged; runtime-only config and child-owned cleanup
pass real process tests. Source review, full panel/race suites, lint and build pass.
The shared SQL writer and live legacy activation integration remain open.


Task 6 live handoff candidate: pure candidate validation, verified executable
copy, legacy metering/credential checks and stable owner fencing now precede the
owned final drain. The shared SQL writer settles pending and final receipts
before policy versions and usage seeds. Durable source intent blocks stale
startup after a panel crash; completion is atomic with final usage and supports
restart after subsequent preparation failure. Unsupported/unmetered legacy
configurations remain refused. Automatic policy selection, ordinary generated
Tunnel metering, large SQL batch bounds, UI status and the remainder of the goal
are still open. Final validation is recorded in testing.md.


Task 6 SQL scale candidate: final settlement now uses bounded email, membership,
inbound lookup and save batches inside one receipt transaction. Real 100001-user
and 3001-listener limits have observed failing regressions and SQLite/PostgreSQL
race coverage. Runtime and policy behavior are unchanged. Complete root panel,
race, lint and build gates pass. Automatic selection, generated Tunnel metering
and UI status are the next activation work.


Automatic local selection checkpoint (2026-09-29): enabled local owned Tunnel
and explicitly configured policy bindings now enter managed startup and ordinary
mutation reconciliation without a template opt-in. Compatible live legacy edits
settle final traffic before seeding; unmetered legacy configurations refuse with
a saved-but-not-applied error. Stopped cores queue activation. Whole-process
identity/capability checks and strict legacy control-listener recognition remain
mandatory. Real traffic, refusal, stopped state and selector boundaries pass
SQLite full-service race and PostgreSQL focused race. Complete UI application
state, general legacy Tunnel migration, remaining protocols and multi-node
budgets are still unfinished.


Saved enforcement status checkpoint: existing confirmed accounting now compares
the current saved policy/reset fingerprint as well as acknowledgement versions.
This prevents pre-preparation compiler failures from concealing pending changes
in the traffic cell. It preserves exact historical usage and does not claim live
health or first activation status where no confirmed receipt exists. Ordinary
client policy editing is covered below; bulk editing and complete activation
status remain work in progress.

Ordinary client policy form checkpoint: add/edit exposes shared upload/download
B/s limits and exact decimal billing multiplier, preserving omitted legacy policy
and existing explicit values. Real create/edit/clear/invalid-input tests and
wire-schema boundary regressions pass. Full frontend and Go verification, both
database scope regressions, lint and builds pass. Tunnel owner selection, bulk controls and
first-activation/live status are not established by these form tests.

Remote policy scope checkpoint: local-only policy editing is enforced in the form,
CRUD preflight and transactional membership boundary, including concurrent unbound
updates. Mirror adoption checks the actual settings before identity filters and
rolls back unsupported or malformed policies. Historical values remain read-only
and unrelated metadata preserves them. This closes unsupported mutation paths;
coordinated remote budgets, stable identity transport and managed receipts remain
unimplemented. Final verification and source review pass as recorded in testing.md.


Task 6 local Tunnel editor checkpoint: add/update accepts an optional `ownerClientId` command naming an existing stable UUID. The server resolves and locks the canonical client inside the same SQL transaction as listener/traffic/membership changes, and changes only the link, not the account. The form uses paged client search, requires an owner for new local rules, preserves legacy unowned edits and `clients:null`, and keeps remote ownership read-only. Reassignment rehomes the former owner’s legacy accumulator to a sibling or an unattached canonical account; migration no longer deletes the latter. Read APIs derive the annotation from membership, and single-inbound import discards a source-panel UUID annotation while retaining portable settings/statistics. Local Tunnel ownership blocks later remote binding even before a desired version exists, including raw mirror input before identity filtering. Real same-port TCP/UDP reassignment and SQLite/PostgreSQL history/rollback/scope tests are present. Full Tunnel ACL/routing-mode UI, unowned-rule migration, distributed budgets, other requested protocols and packaging remain open.

The next dependency identified here was standalone client creation: the existing
form/API required an ordinary inbound before the first Tunnel could get an owner.
The implementation checkpoint below closes that dependency; source ACLs follow.


Task 6 regression repair checkpoint: the complete root race gate exposed an
inherited AmneziaWG timer data race. The exact pinned module is now managed in
`core/deps/amneziawg-go` with provenance and licenses. Its dependency suite also
exposed an independently reproduced first-packet padding bug during blocked TUN
reads. Deterministic regressions, repeated real UDP checks, source review, complete
root/dependency race, static check, build, parser fuzz and reachable-code vulnerability
checks pass. This preserves the current panel-side AWG implementation; migration
into the Custom Xray process is still open. Continue with standalone client
creation so a fresh panel can assign an owner to its first Tunnel.

Task 6 standalone client checkpoint: the existing create endpoint and form accept
empty or omitted inbound IDs. An atomic create-only transaction generates the
stable UUID, preserves disabled state, exact quota and policy, rejects duplicate
email/subscription identities, and creates no listener, credentials, traffic row
or policy version before attachment. A real first-Tunnel flow exercises TCP/UDP
and exact billed totals on both databases. Owner choices share client cache
invalidation so creating an account refreshes an already cached empty list.
Tunnel source ACL/routing-mode coverage and the other remaining goal items are
still open.


Task 6 source ACL checkpoint: the existing Tunnel form
and API carry up to 256 native IPv4/IPv6 source CIDRs. The same core validates
JSON/typed transports and checks physical peers before dispatch/accounting;
capability negotiation prevents older cores ignoring the ACL. Create/update,
startup restoration and re-enable enforce local canonical ownership. Final
client deletion checks canonical links again after fanout and reconciles removed
listeners, including concurrent attachments and stale embedded client lists.
Scoped real IPv4/IPv6 TCP/UDP/TLS and panel hot-replacement checks pass. Full
make verify/race, core unit/scoped race and construction race gates pass;
commands, results and final review are recorded in testing.md.
Routing/outbound selection UI, additional forwarding modes and unowned migration,
other requested protocols, distributed policy and packaging remain open.

Task 6 loopback accounting checkpoint: real
one/two-hop tests exposed repeated policy sessions and payload billing, repeated
legacy counters and an asynchronous access-log race. Same-link redispatch now
retains its original admission, rejects identity changes and separately meters
new links. Explicit loopback cycle/hop limits replace the incidental repeated
session cap. Review added UDP domain/sniff timeout preservation and cancellation
endpoint capture. Focused real TCP/UDP/sniff/quota/disable and race checks pass. Complete core
unit tests, scoped core/construction race, all panel Go tests, affected panel
race, static checks and both builds pass; results and skips are in testing.md.
Per-rule outbound/routing UI and the remaining goal items are still open.

Task 6 routing-error prerequisite: the dispatcher distinguishes no matching rule
from a failed matched balancer. Only no-match retains the default outbound;
explicit balancer fallbacks remain supported. Real managed/unmanaged TCP and
dynamic TCP/UDP removal tests observe distinct selected/default targets and exact
shared accounting. Final gate results are recorded in testing.md. This remains
a prerequisite for the unfinished Tunnel outbound selector.

Task 3/6 fixed outbound increment (2026-09-30): local owned Tunnel rules now
select a concrete template or active subscription outbound, or clear the field
to inherit routing and balancers. The additive core field reaches Dispatcher;
startup and hot mutations negotiate its capability independently. Missing
handlers and unsupported networks fail without direct fallback. Transactional
create/update/re-enable validate spelling, type, canonical ownership and concrete
tag availability. Removed subscriptions retain the saved selection.

Real TCP/UDP tests on SQLite and PostgreSQL establish listener-scoped replacement,
old-flow termination, surviving sibling flows and exact shared lifetime billing
without restarting the core. A real valid mux-frame regression first reproduced
a panic and route bypass at the reserved internal address; Tunnel payload now
remains opaque while ordinary authenticated mux retains its behavior. A read-only
review identified stale subscription tags in the new picker. A real WebSocket
message/query regression failed with the old tag and passes after outbound
invalidations refresh the configuration cache, including beside unrelated local
mutations. Focused frontend checks pass 16 tests. Final regression/build results
are recorded in testing.md as each gate completes.

This increment does not complete Tasks 3/6, migrate unowned legacy forwarding,
add Snell/mieru/SSH, migrate the existing sidecars, coordinate global node policy,
or deliver installation/restore fencing. Those original requirements stay open.

Next Task 5 source-audit dependency: this selected core's Mixed/SOCKS and HTTP
builders collapse credentials into `map[string]string`, without stable client
identity metadata. The servers set legacy email to the authenticated username.
These remaining adapters need server-owned account-to-ID mappings and lifecycle
capabilities before the managed compiler can admit them. SOCKS in this version
already allocates a temporary UDP association listener under its authenticated
TCP context and closes it with that context; do not assume a shared unauthenticated
UDP listener from older versions. Verify that boundary and HTTP's repeated
request authentication with real traffic before extending the existing policy
coverage. No Mixed/HTTP stable-ID support is claimed by the outbound-proxy tests.

### Task 5A: Native password-proxy identity and credential lifecycle

Spec: architecture.md, “Native Mixed/SOCKS and HTTP identity increment”.
Execution remains inline under the original authorization to decide routine
engineering choices. This core increment precedes panel binding/migration; it
does not make unsupported panel configurations eligible for managed activation.

Files: create `core/xray/common/protocol/password_validator.go` and its tests;
extend `proxy/{socks,http}/config.proto`, generated protobuf, server lifecycle
and `infra/conf/{socks,http}.go`; create HTTP request connection lease helper;
extend `proxy/socks/temp_udp_listen.go`; add real `testing/policy` regressions;
negotiate the new capabilities in `app/clientpolicy/command` and panel adapter.

Interfaces: `protocol.PasswordValidator` provides
`Add(username, password string, user *MemoryUser) error`,
`Authenticate(username, password string) *MemoryUser`,
`Remove(email string) error`, `GetUser(email string) *MemoryUser`,
`GetUsers() []*MemoryUser`, `GetCount() int64` and `Close() error`.
HTTP's shared-validator constructor accepts the same validator from SOCKS;
existing `NewServer` signatures remain unchanged. Both handlers implement the
existing `proxy.UserManager`, with no new RPC service or field renumbering.

- [x] Write real failing `TestPasswordProxiesShareTunnelIdentityAndDisconnect`
  using standard SOCKS5/HTTP connections: six-byte echoes from two account
  streams plus Tunnel share 18 upload / 18 download / 54 billed at 1.5;
  disable closes all three, wrong passwords never reach either target.
- [x] Verify RED with `go test -race -count=1 -v ./testing/policy -run
  '^TestPasswordProxies'`; retain failure logs without weakening assertions.
- [x] Write config regressions for both aliases, explicit metadata, no-auth
  rejection, legacy omitted fields, managed duplicate usernames, orphan protobuf
  metadata and HTTP empty required-auth. Preserve legacy expected protobufs.
- [x] Implement the additive fields and shared validator. Test exact Unicode
  usernames, distinct case-sensitive credentials, duplicate index rejection,
  removal/re-add with old-pointer revocation, closed-handler rejection and
  concurrent authentication/removal under race.
- [x] Implement native handlers/UserManager and per-connection credential
  tracking; test active TCP, mixed HTTP fallback, idle/active UDP association
  cleanup and two same-IP users with distinct stable IDs. Re-add keeps the
  existing client's ledger and does not restore revoked old streams.
- [x] Implement request context/lease isolation. Keep user A's upstream open
  after a complete HTTP response, reuse the client socket for user B CONNECT,
  then disable/remove A: B must keep echoing and ledger attribution must stay
  separate. Removing the last authenticated HTTP user must return 407 for an
  unauthenticated request, including explicit empty required-auth construction.
- [x] Negotiate both capabilities, including HTTP required-auth without owners;
  older-capability handler probes must fail before mutation. Keep the panel
  compiler's unsupported-protocol gate until canonical bindings are implemented.
- [x] Run affected core/config/adapter race and static checks, existing protocol
  mux and policy regressions, complete core shuffled tests and scoped core race.
  Review this increment, fix findings, update status/evidence, commit and push
  with exact remote SHA verification before proceeding to panel account binding.

Task 5A scoped checkpoint: native password identity, shared Mixed validators,
typed UserManager, idle/active UDP credential cleanup, request-scoped HTTP
identity, protected empty authentication and capability preflight are implemented
and verified with real sockets under race. Mixed aliases and Tunnel share both
directional rate buckets and exact historical multiplier totals. A read-only
review found no important/critical issue; its unmanaged HTTP CONNECT splice
observation was resolved by retaining the raw connection for terminal CONNECT
while earlier plain requests retain separate inactive leases. All final full-suite
gates passed (see testing.md). Commits `ef8e87dd` (spec), `e390be67` (core) and
`e9adcccf` (adapter/CI/evidence) were pushed to the fork feature branch; its
remote SHA matched `e9adcccf8865fbd8f05b7788ea3c6f96bdb0333d`. The panel
compiler gate is unchanged. Ruling: Mixed noauth ignores unused accounts in both
branches, matching the pinned upstream constructor; an initially contrary test
expectation was corrected rather than changing legacy authentication behavior.

### Task 5B: Canonical password account ownership (in progress)

The source audit found that Mixed/HTTP `settings.accounts` currently contains
only user/pass, whereas normalized `clients` and `client_inbounds` carry the
stable ID and shared policy. `SyncInbound` merges nonempty password/UUID fields
into the global client, so account ownership must use a specialized membership
transaction rather than passing resource credentials through that merge.

Primary source anchors: `frontend/src/pages/inbounds/form/protocols/accounts-list.tsx`,
`frontend/src/schemas/protocols/inbound/{mixed,http}.ts`,
`internal/web/service/{inbound,client_link,tunnel_owner,client_policy_config,client_policy_activation,client_policy_handoff}.go`.

- [ ] Specify per-account canonical owner selection and authoritative database
  validation. Keep wire usernames/passwords resource-specific; never infer an
  existing owner from a display email, username or supplied raw core client ID.
- [ ] Add transactional create/update/read guards, membership reconciliation and
  detached-history preservation. Test late SQL rollback, stale settings, two
  aliases for one owner, owner reassignment, credential rotation and unchanged
  sibling credentials/policy. Run SQLite and PostgreSQL row-lock regressions.
- [ ] Bind every managed credential from canonical records, including disabled
  owners. Preserve protected authentication when all users are disabled or
  removed. Refuse mixed owned/unowned activation until every path has a trusted
  binding; legacy accounts retain existing behavior outside managed activation.
- [ ] Add owner selection to the existing account form with generated API/schema
  contracts and Chinese/English strings. Validate through API, configuration
  export and backup/import paths; don't expose internal core capabilities as a
  user choice or generate unsupported subscription formats.
- [ ] Prove native grouped credential hot changes preserve another owner on the
  listener, close idle as well as active old credentials, preserve the client
  ledger and keep the same core boot ID. Do not loosen the legacy production
  SOCKS hot-diff guard before negotiated custom-core behavior is verified.
- [ ] Audit legacy username-based statistics during managed handoff. The current
  handoff implementation keys history by canonical email and rejects password
  proxy protocols; do not open that gate without alias-to-owner mapping and
  independent final-counter settlement evidence.
- [ ] Keep local/remote scope restrictions effective before fanout and import
  filtering. Anonymous ownership, policy-only pre-dispatch control registration,
  multi-node budget allocation and remaining protocol families stay open.

### Task 5B1: Authoritative password-owner persistence

Spec: architecture.md, "Canonical password account ownership increment".
Ruling: reuse `settings.accounts[].ownerClientId` and `client_inbounds`; a second
credential table would duplicate settings without adding a distinct authority.
The cost of a mismatched mirror is rejected reads/configuration, not inferred
identity. Keep the managed compiler gate until the rest of Task 5B is verified.

Files: new `internal/web/service/password_proxy_owner.go` and
`password_proxy_owner_test.go`; change `inbound.go`, `client_link.go`,
`client_policy_scope.go`, `client_policy_activation.go` and existing read guards.

Interfaces: `preparePasswordProxyOwnerCommand(*model.Inbound) error` validates
wire commands; `resolvePasswordProxyOwners(*gorm.DB, *model.Inbound)
([]model.ClientRecord, error)` locks/resolves; specialized owner reconciliation
writes links only. Stored-binding validation compares distinct account owner
UUIDs against joined canonical membership and refuses missing/mismatched rows.

- [ ] Write `TestPasswordProxyOwnersPreserveCanonicalRecords` using two resource
  aliases and two owners. Assert two normalized memberships, byte-preserved wire
  credentials, unchanged canonical policy/credentials and original usage.
- [ ] Observe RED through the actual AddInbound/UpdateInbound API service path.
- [ ] Implement command validation, transactional owner resolution/reconciliation,
  protected HTTP empty authentication and detached history retention. Add named
  regressions for missing/noauth/remote owners, mixed ownership, duplicate user,
  forged core identity, last removal, reassignment and late SQL rollback.
- [ ] Reject generic settings-client sync/delta bypasses; add read-time canonical
  membership guards and remote attachment guards before filtering/fanout.
- [ ] Run the focused tests under race on SQLite and PostgreSQL and existing
  Tunnel/client-link/scope/activation regressions. Then run panel checks, record
  evidence, commit and push with an exact remote SHA check.
