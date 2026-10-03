# Single Custom Xray-core implementation plan

> Execution: superpowers:executing-plans, inline in the feature branch. Use test-driven-development for behavior changes and verification-before-completion before claims or commits.

**Goal:** Complete all requirements in [requirements.md](requirements.md), including panel, core, protocol interoperability, persistence, existing data-plane migration, distribution and verified pushes.

**Architecture:** One panel/control process and one Custom Xray-core data-plane process per node. All authenticated accounts and owned listeners resolve to a stable client ID; decrypted payload passes through a common policy engine before Xray routing. The panel owns configuration and long-term accounting; the core enforces allocated budgets and keeps recoverable execution state.

**Stack:** Existing Go/Gin/GORM SQLite/PostgreSQL panel and React/Ant Design frontend; managed Xray source, Go protocol libraries, existing gRPC control transport.

**Spec:** [requirements.md](requirements.md), [architecture.md](architecture.md), [accounting.md](accounting.md).

## Current execution priority — 2026-10-01

Native Snell v4/v5/v6 (including v5 QUIC), mieru and SSH now have scoped
local core/panel/API/UI/export checkpoints backed by real client tests. The
user's next priority is shared multiplier billing, directional rates and Tunnel
forwarding. This stage completes bulk policy controls and reproduced direct and
selected-proxy forwarding gaps, then records fresh SQLite/PostgreSQL, native
client, rate and clean-build evidence. Commercial-device, other-platform and
coordinated-node acceptance remain separate open requirements.

## Global constraints

- Start from main `17d7dd46b512d0a9c22921a6094f30c672e436c9`, never from `feat/unified-client-policy-backends`.
- Push only `feature/custom-xray-unified-policy`; isolated local adapter worktrees
  may use temporary feature branches. Preserve existing branch tips. No releases,
  default-branch merges, production deployment or force pushes.
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

## User execution priority — 2026-10-01

Finish native Snell, mieru and SSH support first. Then complete multiplier billing,
per-client upload/download rate enforcement and TCP/UDP forwarding through Xray
Tunnel/dokodemo-door. Those features share canonical identity, routing, quotas,
expiry and statistics, with full DB/API/UI/config/data-path acceptance.

The official-core replacement guard is complete and published. Continue the
billing/rate/Tunnel vertical through final verified artifacts and publication;
then address remaining original distribution, restore and multi-node work.
Existing partial tests do not close a whole feature; completion requires the
agreed observable acceptance.

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

- [x] Standard OpenSSH tests for -L/-D and opt-in controlled -R, wrong/revoked keys and denied shell/exec/subsystems.
- [x] Implement in-process SSH using Go SSH, separate persistent host keys, bounded handshake/channels/listeners and strict outbound host verification.
- [x] Route direct channels through Dispatcher; meter reverse channels with explicit direction and honest target visibility.
- [ ] Verify shared policy over multiple connections/channels, listener cleanup, export and API/UI lifecycle.

## Task 8: mieru inbound and outbound

Files: core `proxy/mieru`, source-pinned library adapter, panel protocol/config/export paths.

- [x] Audit embedded Accept/authentication context and all target dial paths at pinned version; prevent independent dialing.
- [x] Test pinned official-library client TCP/UDP, mux and deletion of active users/listeners; packaged client/device acceptance remains open below.
- [x] Use one unified local quota source, trusted username mapping, route selection and policy wrappers; integrate existing panel CRUD/forms/export and real child lifecycle.
- [ ] Verify packaged official clients/devices and coordinated remote-node lifecycle; retain the explicit local scope.

## Task 9: Snell v4, v5 and v6 separately

Files: core `proxy/snell`, necessary managed library adaptation, panel version-specific config/export.

- [x] Audit OpenSnell source/license and independently pin each compatibility target.
- [x] Implement v4 native core inbound/outbound with TCP, UDP-over-TCP, reuse and trusted PSK/listener mapping; panel integration remains open.
- [x] Implement/test v5 independently including native QUIC Proxy Mode, official server and real v1/v2 HTTP/3; actual proprietary Surge inbound acceptance remains unverified.
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

- [x] Specify per-account canonical owner selection and authoritative database
  validation. Keep wire usernames/passwords resource-specific; never infer an
  existing owner from a display email, username or supplied raw core client ID.
- [x] Add transactional create/update/read guards, membership reconciliation and
  detached-history preservation. Test late SQL rollback, stale settings, two
  aliases for one owner, owner reassignment, credential rotation and unchanged
  sibling credentials/policy. Run SQLite and PostgreSQL row-lock regressions.
- [x] Bind every managed credential from canonical records, including disabled
  owners. Preserve protected authentication when all users are disabled or
  removed. Refuse mixed owned/unowned activation until every path has a trusted
  binding; legacy accounts retain existing behavior outside managed activation.
- [x] Add owner selection to the existing account form with generated API/schema
  contracts and Chinese/English strings. Validate through API, configuration
  export and backup/import paths; don't expose internal core capabilities as a
  user choice or generate unsupported subscription formats.
- [x] Prove native grouped credential hot changes preserve another owner on the
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

- [x] Write `TestPasswordProxyOwnersPreserveCanonicalRecords` using two resource
  aliases and two owners. Assert two normalized memberships, byte-preserved wire
  credentials, unchanged canonical policy/credentials and original usage.
- [x] Observe RED through the actual AddInbound/UpdateInbound API service path.
- [x] Implement command validation, transactional owner resolution/reconciliation,
  protected HTTP empty authentication and detached history retention. Add named
  regressions for missing/noauth/remote owners, mixed ownership, duplicate user,
  forged core identity, last removal, reassignment and late SQL rollback.
- [x] Reject generic settings-client sync/delta bypasses; add read-time canonical
  membership guards and remote attachment guards before filtering/fanout.
- [x] Run the focused tests under race on SQLite and PostgreSQL and existing
  Tunnel/client-link/scope/activation regressions. Then run panel checks, record
  evidence, commit and push with an exact remote SHA check.

Task 5B1 review decisions: normalize native account aliases and field case
folding, retain owned authentication across Mixed-to-empty-HTTP conversion, and
accept ignored legacy email on reads while removing it on the next write. All
three important review findings have observed RED/GREEN regressions. A further
legacy conversion regression limits the new authentication requirement to owned
Mixed accounts. A valid-read regression distinguishes preloaded SQL stats from
caller-supplied mirrors. Preserve existing Tunnel ACL error precedence.

The actual form adapter and schemas initially dropped ownership metadata; their
round-trip regressions now retain it. This preservation belongs in the database
foundation because existing form edits otherwise erase selected ownership.
No owner picker, canonical runtime binding, grouped hot diff, generic client
lifecycle or legacy counter handoff is completed by Task 5B1.

Task 5B1 checkpoint: implementation `65506131` passed complete `make verify`
and `make race`, then was pushed to the fork feature branch. Its exact remote
SHA matched `6550613112d073f246cb4aea44d75d8a2ee8f7cb`. The clean-source build
and distinct artifact provenance follow in deployment.md.

### Task 5B2: Canonical password runtime configuration

Spec: architecture.md, "Canonical password account ownership increment".
Consumes validated account owner UUIDs and canonical memberships from Task 5B1.
Ruling: generate account-specific user/pass plus canonical email/stable ID from
one repeatable-read snapshot; never substitute global client credentials.
Keep the live legacy handoff adapter gate until username-counter ownership is
verified separately. Grouped native hot changes and owner UI are later steps.

Files: new `internal/web/service/password_proxy_config.go`,
`password_proxy_config_test.go` and `password_proxy_config_runtime_test.go`;
change `client_policy_config.go`, `xray.go` and the restored HTTP protection
boundary in `password_proxy_owner.go`.
Interface: `bindManagedPasswordProxyIdentity(*xray.InboundConfig,
[]model.ClientRecord) error` validates every stored account owner and emits
native identity metadata. Strip ownerClientId, dormant users and settings.clients
from the runtime payload. Include policies for every linked owner, including
disabled owners; omit explicitly disabled owners' authentication credentials
while retaining Mixed password auth and HTTP required-auth in empty listeners.

- [x] Write `TestPasswordProxyConfigUsesCanonicalOwners` through the actual
  compiler, using two aliases, two owners, resource passwords different from
  canonical shared passwords, and existing traffic. Assert exact wire
  credentials/identities, one policy per distinct owner and no settings.clients.
- [x] Observe rejection by the current unsupported-adapter gate before changes.
- [x] Implement the native binding and skip generic clients generation for
  password protocols. Reject unowned active credentials and malformed bindings;
  protected empty listeners require no invented owner. Do not use display names
  or caller-provided core identity as authority.
- [x] Write snapshot-reassignment, stale membership, all-disabled and empty-auth
  regressions. Assert compilation neither mixes owners/credentials from two
  revisions nor mutates stored settings/shared credentials.
- [x] Prove generated Mixed SOCKS/HTTP and HTTP listeners feed the real core
  ledger using independent standard clients and targets; aliases share a stable
  ledger with Tunnel, wrong credentials reach no target, disabled owners remain
  blocked, and another owner remains usable. Run SQLite/PostgreSQL compiler
  tests and affected capability/activation regressions under race.
- [x] Record evidence, review once, fix Important/Critical findings with
  RED/GREEN, commit and push the validated configuration increment. Preserve
  the live legacy handoff guard and existing upstream hot-diff restrictions.

Task 5B2 checkpoint: `44df54288ebb77f09074a8ff53e845397852b73d` passed
SQLite/PostgreSQL compiler/ownership race tests, generation/lint/vet, full root
Go tests and full affected service/adapter/Runtime race tests. One read-only
review found no Important or Critical finding. Push to the fork feature branch
was verified by an exact remote SHA comparison. Scoped runtime binding is
complete; remaining Task 5B work is still open.

### Task 5B3: Grouped managed password credential hot changes

Spec: architecture.md, "Grouped managed password credential changes".
Consumes verified native email-group revocation and canonical runtime accounts.
Ruling: introduce a separate managed diff path; preserve ComputeHotDiff and the
legacy SOCKS restart guard. Rebuild a changed owner's complete alias group,
keeping unrelated owners' listeners/sessions and the same owner's Tunnel.
Live legacy handoff, owner UI and generic lifecycle remain separate tasks.

Files: new `internal/xray/password_proxy_hot_diff.go` and tests;
modify `hot_diff.go`, `api_managed.go`, password-specific `api.go` level
handling, `internal/web/runtime/client_policy_config.go` and
`internal/web/service/client_policy_activation.go`; new service real-runtime
and capability tests.
Interface: `ComputeManagedHotDiff(*Config, *Config) (*HotDiff, bool)` shares
existing static/routing/outbound checks and emits grouped password UserOps.
Resolve removed identity from all matching runtime accounts before handler
writes. Negotiate protocol/revocation/inbound-close capabilities before SQL
preparation, including remove-only operations.

- [x] Write `TestComputeManagedPasswordHotDiffGroupsOwnerAliases`: two aliases
  for A plus owner B; rotating one A alias must remove A once, re-add both A
  aliases and produce no B or listener operation. Observe current-path RED.
- [x] Implement the managed entry point and deterministic email-group diff.
  Test reorder-only no-op, username transfers, additions, final protected
  removal, malformed ownership and exact-case/Unicode username distinctions.
  Keep the existing legacy SOCKS regression assertions unchanged.
- [x] Require password protocol capabilities on remove-only and rotate paths
  before preparation or mutations. Test missing each protocol capability,
  revocation and inbound-close; counters remain zero on preflight rejection.
- [x] Preserve listener userLevel on typed password AddUser. Assert the actual
  protobuf level 7 and canonical identity; reject negative, fractional,
  overflow, boolean and string level input before handler mutation.
- [x] Through actual AddInbound/UpdateInbound and managed Runtime, retain A
  idle/active SOCKS, CONNECT and UDP sessions plus B and A Tunnel. Rotate A;
  old credential sessions close, B and Tunnel continue, old auth fails and
  both remaining A aliases work. Assert unchanged BootID and exact independent
  target/ledger bytes on Mixed and HTTP paths.
- [x] Test last removal preserving authentication and partial RPC failure
  through the existing stop/recovery boundary, without acknowledging an
  incomplete candidate or replaying usage. Keep the live handoff gate closed.
- [x] Run SQLite/PostgreSQL and affected full race/root gates; add explicit CI
  PASS checks, review once, record evidence, commit/push and verify remote SHA.

Task 5B3 checkpoint: implementation `6f9705206d81ea772ba082c8f6607d45c8aef098`
passed the eleven named SQLite regressions, actual PostgreSQL hot regressions,
generation/lint/vet, full root Go tests and full affected race tests. One
read-only review found no findings. The fork feature branch's remote SHA
matched the implementation exactly. A fresh clean-source build passed and
preserved distinct artifacts; provenance is recorded in deployment.md. Owner
selection UI, generic lifecycle and live legacy counter handoff remain open.

### Task 5B4: Password account owner form and preservation contracts

Spec: architecture.md, "Password account owner selection". Execute inline.
Ruling: add selection to existing account rows; reuse the paged clients API and
existing query invalidation, without changing the verified Tunnel form.
Keep legacy unowned editing. Source-owned single-inbound JSON must not bypass
canonical identity/traffic guards; portable remapping remains separate.

Files: new `frontend/src/pages/inbounds/form/protocols/password-account-owner.tsx`
and actual form tests; modify `accounts-list.tsx`, `api/queryKeys.ts`, Mixed/HTTP
settings schemas, shared account validation and translation JSON files.
Update `pages/api-docs/endpoints.ts` and regenerate existing API artifacts.
Add controller contract and database backup/migration preservation tests.

Interface: `PasswordAccountOwner({ index, disabled }: { index: number;
disabled: boolean })` binds `settings.accounts[index].ownerClientId` through
FormField, with independent selected-label state and shared server query cache.
Schema validation rejects partially owned lists and owned Mixed noauth before
save; UUID existence/remote membership remains an authoritative SQL check.

- [x] Write actual modal regressions for Mixed/HTTP alias owner selection and
  saving exact account credentials/UUIDs without settings.clients or traffic.
  Observe missing-picker RED before implementation.
- [x] Add paged, searched and cached owner selection. Test selected labels
  outside later searches, pagination, list errors, standalone-client mutation
  invalidation, noauth/remote read-only behavior and legacy unowned saves.
- [x] Add failing schema/save regressions for partial ownership and owned
  noauth. Implement shared account checks without erasing wire credentials.
- [x] Verify real controller add/update/read/export JSON with canonical owners,
  rejected unknown/remote/traffic commands and unchanged shared credentials.
  Verify single-inbound import guards without weakening them.
- [x] Verify full SQLite backup/dump/restore plus actual PostgreSQL migration,
  export/dump/restore preserve both aliases, owner UUID/membership, policy and
  traffic; distinguish these from portable single-inbound import.
- [x] Regenerate API artifacts, run frontend checks/full tests and affected
  Go/race/database checks. Review once, record scoped evidence, commit/push and
  verify exact remote SHA. Owner lifecycle/handoff remain open.

Task 5B4 scoped checkpoint: implementation `67f5336a70216ffa169efb8978f93aa11cb0b995`
pushed to the authorized fork with exact remote SHA verification. Actual forms,
API and full database preservation pass. All 1842 frontend tests pass with one
worker; default-worker timeout remains recorded. Go/full affected race, lint,
types, builds and clean-clone artifacts pass. One important review finding was
reproduced and fixed. Whole Task 5B and the overall goal remain incomplete.

### Task 5B5: Canonical password-owner detach and deletion

Spec: architecture.md, "Password owner detach and deletion". Execute inline.
Ruling: specialize resource removal by stable UUID and reuse verified native
grouped changes. Shared client updates and live legacy handoff stay separate.

Files: new `password_proxy_owner_lifecycle.go` and service regressions; dispatch
from `DelInboundClientByEmail`, `delInboundClients`, `bulkDelInboundClients`.
Add ownership-aware preflight to public Delete/Detach/BulkDelete/BulkDetach;
retain their signatures and existing generic membership guards. CI requires
new named PASS results on SQLite and PostgreSQL.

Interface: one removal helper receives canonical record IDs/stable IDs, reads
and locks current saved settings, validates the full owner graph, removes all
selected aliases, reconciles specialized links/history atomically and applies
the existing managed runtime path after commit. Preserve remaining raw account
fields and protected empty auth. Adjust the helper shape only with recorded
source/test evidence.

- [x] Observe public single/bulk detach/delete RED on Mixed/HTTP. Seed two
  aliases for A, one for B, independent canonical credentials, disabled B,
  history, ledger and a sibling resource. Verify all A aliases removed, B
  unchanged, correct links/client/tombstones, retained usage, empty auth and
  idempotent detach.
- [x] Observe late membership-write rollback and stale-graph RED. Preserve
  original settings/links/history/records, native users/case and account fields.
- [x] Observe remote-sibling preflight RED through all four public operations,
  including filtered detach. Reject before local/remote mutations; retain
  ordinary legacy compatibility and recheck concurrent attachment scope.
- [x] Implement specialized removal and run focused race/shuffle on SQLite and
  actual PostgreSQL. Prove competing rotation/reassignment through PG row locks
  cannot be overwritten by stale whole-account writes.
- [x] Verify real core public operations close A active/idle TCP and Mixed UDP,
  preserve B sessions and A Tunnel after detach, retain boot ID and exact
  target/ledger conservation. Global deletion revokes A across resources,
  preserves usage and sibling, and survives restart.
- [x] Add required CI names, run generation/lint/vet/full affected checks,
  review once, document scope, commit/push exact remote SHA and rebuild clean
  distinct artifacts. Shared Update and live legacy handoff remain open.

### Task 5B6: Single shared password-owner Update

Spec: architecture.md, "Single shared password-owner updates". Execute inline.
Source audit: ClientService.Update currently sends every selected resource to
UpdateInboundClient with settings.clients; its full canonical fallback runs
only when no selected inbounds exist. Skipping password resources alone would
lose fields for password-only owners. Direct policy reconciliation cannot
update canonical-email authentication labels after rename.

Files: ClientService.Update and its private expected-identity dispatch;
canonical field persistence helper; SQLite/PostgreSQL/real-core regressions;
CI named PASS requirements. Preserve generic client and specialized ownership
guards; account creation and bulk/by-email lifecycle stay separate.

Interface: validate affected full owner graphs before filter/fanout, retain
captured ID/UUID through ordinary sibling writes, persist shared fields under
canonical SQL locks without changing accounts JSON, preserve current omitted
policy and history, and use managed candidate reconciliation after commit.
Keep ordinary multi-resource partial-success/retry semantics explicit.

- [x] Observe public single Update RED for Mixed/HTTP password-only owners,
  ordinary siblings and filtered ordinary updates. Verify canonical shared
  fields, unchanged resource credentials/other owner/memberships and lifetime.
- [x] Implement canonical persistence and identity fencing. Observe scope,
  reused-email, omitted-policy and late-write failure regressions; verify
  rollback and existing clear/credential/flow behavior on both databases.
- [x] Prove actual PostgreSQL concurrent resource password rotation is retained
  while shared fields update; do not write a stale account list.
- [x] Real core: normal Update changes multiplier/rate/name/shared credentials,
  enable/quota/expiry across aliases and Tunnel; preserve B and exact usage.
  Verify uncertain acknowledgement, saved-command recovery and no replay.
- [x] Require CI names; review once and resolve findings; freeze production,
  run generation/lint/vet/full Go/affected race and SQLite/PostgreSQL contracts.
- [x] Document scoped evidence, commit/push exact fork SHA, rebuild clean
  distinct artifacts and record provenance. Bulk lifecycle/handoff stay open.

Task 5B5 scoped checkpoint: implementation e1efc255bea94da9d9b68e699a068c9f4f110e15
pushed with exact remote SHA; all final scoped SQLite/PostgreSQL, full Go and
race gates pass. Two Important review findings were reproduced and resolved;
the same reviewer found no new material issue. Clean-source rebuild and
distinct artifact checks pass, with provenance in deployment.md. Single Update
and remaining lifecycle/handoff work were open at that removal checkpoint.
The following Update checkpoint advances only the single-client scope; Task5B
still remains incomplete.


### Task 5B7: Canonical password-owner field and bulk lifecycle

Spec: architecture.md, "Canonical password-owner field and bulk lifecycle".
Execute inline. Audit: BulkSetEnable and by-email setters currently search
settings.clients, absent from explicit password resources and empty Tunnel
mirrors. Shared enable reads must use canonical state; matching saved intent
must still reconcile uncertain runtime application. Field writers must retain
current unrelated fields instead of replaying a stale complete Client.

Scope: CheckIsEnabledByEmail, Set/Toggle enable, IP/expiry/quota setters and
BulkSetEnable for local explicit Mixed/HTTP owners. Legacy ordinary/remote
fanout and public result contracts remain. Generic creation, Telegram traffic-ID
writer, external links, anonymous ownership and live legacy handoff stay separate.

- [x] Observe actual public RED for both protocols and password-only,
  ordinary and empty-Tunnel graphs. Cover reads, setters, toggle, dedup/missing
  bulk reporting; verify preserved resource credentials/history/links/ledger.
- [x] Implement narrow current-field mutation, canonical enable reads and
  matching-intent retry. Validate full graph before writes and under locks;
  retain legacy fanout/Changed/Skipped behavior and unrelated current fields.
- [x] Verify malformed/missing/remote/ambiguous graphs before fanout, stable
  identity through name reuse, late SQL rollback and actual PostgreSQL locks.
- [x] Fence captured classification in both owned and legacy writers; preserve
  committed bulk results after later failures. Settle actual relative-expiry
  first-use receipts across password-only, ordinary and sole-owner empty Tunnel
  graphs, rejecting late ambiguous membership with atomic rollback.
- [x] Real core: A alias active/idle TCP/UDP and Tunnel disable/re-enable,
  quota/expiry; B survives at same source IP/core boot with independent target
  bytes and exact shared lifetime ledger. Lost response stops/recovers saved
  field intent and permits retry without replay.
- [x] One read-only review and same-review corrections; freeze source, named
  SQLite/PostgreSQL contracts and required generation/lint/vet/full Go/race.
- [x] Scoped docs/matrix/checklist, logical commit+exact fork SHA push, distinct
  clean builds and provenance. Whole Task5B and original goal remain open.


Task 5B6 scoped checkpoint: implementation `3d3590f99d897c1b920b21e79c86ab96d7e16f17`
is pushed with matching fork SHA. Final SQLite/PostgreSQL named contracts,
generation/lint/vet/full Go and affected race pass. One Critical/three Important
review findings and correction follow-ups were reproduced and resolved by the
same reviewer; final source hashes are unchanged. Distinct clean artifacts and
version/provenance checks pass. Task5B7 bulk/by-email lifecycle and live legacy
handoff remain open; Task5B and the original goal are incomplete.


Task5B7 scoped checkpoint: implementation `5ec2d9dba46ddfee0b76c6166129539dc18ccf5d`
is pushed with matching fork SHA. All 18 SQLite/20 PostgreSQL required names,
generation/lint/vet/full Go/affected race pass. The same reviewer resolves three
Important issues and independently verifies final PostgreSQL writer fences.
Distinct clean panel/core builds, version and prior artifact preservation pass;
provenance is in deployment.md. Live legacy handoff and the original goal remain
incomplete.


### Task5B8A: Durable unmatched legacy counter retention

Spec: architecture.md, "Unmatched legacy counter retention prerequisite".
Execute inline. The normal collector currently drops a native username when no
client traffic row matches; its cursor still advances. Final alias mapping alone
cannot recover those earlier deltas. Preserve them before opening password live
handoff. A committed receipt also needs original payload binding for safe retries.

Files: `internal/xray/process_traffic.go`,
`internal/database/model/legacy_traffic_receipt.go`, new
`internal/database/model/legacy_unassigned_traffic.go`, database schema/migration,
`internal/web/service/xray_traffic_settlement.go`, new
`internal/web/service/legacy_traffic_retention.go`, and collector/migration tests.

- [x] Actual RED: unknown labels/case variants/long UTF-8 survive settlement;
  changed retry payload rejects; late bucket failure rolls back all layers.
- [x] Persist raw unmatched source-labelled buckets and original receipt digest
  transactionally; capture matched rows once and preserve ordinary maintenance.
  Validate identity/input, checked addition and hash collision; bound SQL batches.
- [x] Capture legacy/managed/unknown source mode and managed instance before the
  SQL callback; retain it in pending retries, digests and buckets. Never infer it
  from a later current child or reuse managed native bytes as legacy usage.
- [x] Real ordinary polling, lost-commit acknowledgement, cursor retry/growth,
  late-created matching row, many labels and preserved known sibling counters.
  Existing handoff/first-use/job behavior remains green.
- [x] SQLite/PostgreSQL schema/backup/migration/export preserve buckets/digests,
  including old tables/columns absent without source mutation or fabricated data.
- [x] One read-only review/same-review corrections, frozen source and named DB
  contracts plus required gen/lint/vet/full Go/affected race checks.
- [x] Scoped docs/matrix, logical commit/exact fork push and distinct clean
  artifact provenance. Password live-handoff gate remains closed; owner mapping,
  conflicting labels and historical configuration proof remain later work.


### Task5B8B: Native startup configuration provenance

Spec: architecture.md, "Native startup configuration provenance prerequisite".
Execute inline with superpowers:executing-plans and TDD. This is conservative
proof storage, without alias ownership, bucket adoption or an open handoff gate.

Files: modify `internal/xray/process.go`, `process_traffic.go`,
`internal/web/service/legacy_traffic_retention.go`, `xray_traffic_settlement.go`,
`internal/database/db.go`, `migrate_data.go`; create
`internal/xray/process_traffic_provenance.go`,
`internal/database/model/legacy_traffic_config_source.go`,
`internal/web/service/legacy_traffic_provenance.go` and matching provenance tests.

Interfaces: `xray.TrafficConfigProof` holds ConfigDigest, EffectiveConfigDigest
and ConfigStable; `TrafficBatch.ConfigProof *TrafficConfigProof` is optional.
`Process.NativeTrafficConfigProof() *TrafficConfigProof` returns a detached value
(or nil for unknown), never credentials. Private digest/capture helpers prepare
proof before child start; Process mutators invalidate stability under mu.
`retainLegacyTrafficConfigSource(tx *gorm.DB, batch *xray.TrafficBatch, previousSequence int64) error`
locks/persists the optional proof without altering counters or owner state.
The locked previous receipt sequence fences missing-header historical sources
as unknown, preventing later proof insertion from promoting their history.

- [x] Behavioral RED: real child's written/logical startup digests exist before
  first poll; config restore cannot erase drift; missing command evidence stays
  unknown. Observe actual failures before runtime implementation.
- [x] Capture per-child proof before startup, preserve lock order and detached
  ordinary/final/pending snapshots; cover canonical-equivalent replacements,
  bootstrap, restart and concurrent config/traffic access.
- [x] RED/GREEN original Task5B8A digest compatibility, changed-proof receipt
  retry, cloned validator intent, immutable source digests, monotonic SQL
  stability and late rollback across all layers. Persist source atomically.
- [x] SQLite/PostgreSQL schema/backup/migration/export current and missing-source
  schemas, authentic original receipts/buckets and no fabricated proof.
- [x] Named real-core DB contracts and prior retention/handoff/first-use/job
  regressions; update required CI PASS names and shell/YAML checks.
- [x] One read-only review/same-review corrections, frozen source, required
  gen/lint/vet/full Go/affected race and scoped capability/testing docs.
- [x] Logical commit/exact fork push, distinct clean builds and provenance.
  Future full mapping/consumption and password handoff remain unfinished.

## Native SSH core checkpoint — 2026-10-01

Core implementation follows native-ssh-design.md and native-ssh-plan.md. One
independent review's two Important lifecycle findings plus bounded CLOSE
acknowledgment follow-up were reproduced and corrected before integration.
Exact acceptance and parent verification are in native-ssh-testing.md.
Task 7 remains open for panel/API/DB/forms/export, dedicated business host-key
provisioning/rotation, negotiated capability and full lifecycle acceptance.
Snell remains a core priority and mieru panel integration is in progress; no
new password/legacy migration feature scope is introduced by this checkpoint.

## Native Snell TCP/datagram integration — 2026-10-01

The reviewed v4/v5/v6 increment is integrated locally, with additive dependency
notices retaining mieru and SSH modules. Parent corrected-file hashes and native
race validation passed, as did merged Dispatcher/CPE/buffer/pipe and root-module
MVS callers. Full merged core/root generation, lint, vet and Go gates passed, including
all 175 core package results and real existing protocol scenarios.
Single-review three Important corrections are included; the official-v6
missing-first-reply assertion Minor is deferred and visible.

This covers authenticated TCP and UDP-over-TCP paths. Native v5 QUIC is being
implemented separately from official codec probes; full panel lifecycle, actual
Surge-client inbound and the official-v5 large-reply gap remain open. No deployment
or release is authorized. See [native-snell-testing.md](native-snell-testing.md).

Clean Snell TCP/UDP checkpoint sourced from d4b93bbb was built and exactly
pushed to the fork feature branch; both named artifacts and27 preserved prior
hashes are recorded in native-snell-testing.md. Native v5 QUIC and Snell panel
integration continue; this is a core-only checkpoint, with no deployment.

## Native mieru panel and Snell QUIC merged verification — 2026-10-01

Reviewed Snell QUIC fee56ca3 integrates as7a822795. The mieru panel plan, backend,
forms, export and single review correction integrate as1f523178,772f7dac,
8d884f9a,994e27b7 andb1773bf1. All native SSH/mieru/Snell core code remains in the
same managed core. CI preserves prior references and adds the combined42 Snell
required guard plus34 SQLite/36 PostgreSQL mieru panel guards.

Merged-source race verification passes27 mieru core,23 SSH and42 Snell named
acceptances. A separate merged official-fixture run passes all four real QUIC
v1/v2 HTTP/3 paths and strict first13k/native UDP TCP fallback with zero skips.
SQLite34 and actualPG36 public panel required cases pass against the merged
core. The initial PG run found two new tests lacked schema isolation; repeated
failures are retained, isolated count2 and the complete36-name rerun pass.
Product code was not weakened to resolve a setup failure.

Complete root Go/AWG,175 core-package shuffled regression, generation, lint0,
vet, frontend typecheck/lint pass. All37 changed frontend files exactly match
the independently reviewed290-test candidate; no duplicate broad frontend run
was needed. Distinct clean-source artifacts and fork publication follow this
validation commit. New SSH panel design/plan are staged for inline execution;
SSH/Snell panel, packaged clients, coordinated nodes, sidecar migration and the
remaining whole-project matrix remain open.

The native mieru panel/Snell QUIC checkpoint is published at ab997b39 with
matching remote SHA. Clean-clone panel/core and frontend builds pass; distinct
checksums and30 unchanged prior artifacts are recorded in mieru-panel-testing.
The local mieru panel plan is complete within its explicit scope. SSH panel
Task1 now executes inline from ab997b39 in /tmp/3x-ui-native-ssh-panel; native
Snell panel and the remaining original full-project requirements stay open.

## Reviewed local native SSH panel checkpoint — 2026-10-01

SSH panel tasks complete within local scope: canonical independent authentication
and database business trust3525f678, existing forms3b20eab0, truthful native
OpenSSH/public HTTP lifecycle676ddcc3, one final review and single correction
1dd19b3a/21fd73f7, integrated/published exact source69ed3f4f. Core source remains
the same managed Snell/mieru/SSH core. Final47SQLite/49actualPG SSH and35/37 mieru
requirements pass against the exact clean core artifact;42 pinned Snell parents
also pass. Root Go/static/generation and affected frontend checks pass.

The32 earlier artifacts remain unchanged. Native SSH panel checksums, bounded
review findings/fixes and precise clean-build provenance are recorded in
native-ssh-panel-testing.md. Local SSH panel work is complete; Snell panel is
the next core-priority vertical. Remote key/budget coordination, packaged clients,
real proprietary devices and the broader original migration remain open.


## Reviewed local native Snell panel checkpoint — 2026-10-01

Canonical backend88aa5ddc, existing formsa2346d58 and exports/public runtime
d442fa36 were reviewed together. Single correctiona94e102b fixes sniffed
SOCKS/HTTP source cancellation. Source publication and exact fork feature HEAD
are verified. Clean artifacts pass Snell47/50, SSH47/49 and mieru35/37 required
SQLite/PostgreSQL checks;56 core race parents and1807 frontend tests pass.
All34 preceding artifacts are unchanged. See native-snell-panel-testing.md for
hashes, negative evidence and boundaries. Continue Task13 installation/upgrade/
distribution for this core; the remaining original requirements stay open.

## Shared policy and Tunnel local checkpoint — 2026-10-02

After the native Snell/mieru/SSH panel checkpoints and published official-update
guard, source `7d96018973b756c68a4f967151b9e4c0da45f638` completes the bulk policy
form and repaired local Tunnel forwarding acceptance. The one whole-stage
review's three Important issues and stale status note were addressed in one
correction batch; full regression also caught and fixed Snell source-socket
cancellation and a controlled test-port collision.

Fresh evidence: 1819 frontend tests, root Go regression, 98 core test packages,
114 complete pinned-fixture Snell/policy race parents, 121 policy/listener/mieru
race parents, 77 named required core cases, actual SQLite/PostgreSQL native HTTP
acceptance and all six independent rate cases. Clean paired artifacts preserve
37 prior hashes. See `policy-tunnel-testing.md` for exact source provenance,
billing fractions, quotas, rate windows, failures retained and scope boundaries.

This closes the scoped local billing/rates/Tunnel vertical. It does not complete
the original distribution, coordinated nodes/global budgets, restore fencing,
existing sidecar migrations or commercial-device/other-platform requirements.
Continue those original requirements without revisiting legacy/password work
unrelated to making the requested architecture usable.


## Execution checkpoint — 2026-10-02 restore consistency

The native Snell/mieru/SSH and shared multiplier/rate/Tunnel checkpoint, paired distribution and its single review correction batch are integrated and pushed at d1ca3626. Source artifacts identify 2b36bc23; all retained hashes and limitations remain in paired-distribution-acceptance.md. Seven-target feature review passed; missing geodata and cumulative PostgreSQL CI timeout corrections are in progress.

Task 11 now repairs consistent source backup reads, unconfirmed-stop restore continuation, queued writes and delayed managed ledger replies crossing SQL replacement. Each reproduced behavior has a RED→GREEN regression. Full root regression passed 50 tested packages, and current source passed eleven required native/shared-Tunnel HTTP cases per SQLite/PostgreSQL backend with zero skips. These do not complete snapshot rollback/cloning safety or task 12. Continue with an import-wide barrier and an authority/grant contract that cannot silently recreate spent or outstanding allocation from restored SQL/bbolt files; retain canonical identities, business credentials, committed usage, reset history, receipts/tombstones and unknown-resource evidence. Do not remove managed remote-scope guards until coordinated budget/rate issuance and expiration are enforced in the one core per node. Legacy MTProto/TUIC/AmneziaWG movement and remaining platform/device acceptance stay open.

PostgreSQL owned restore checkpoint2026-10-03: real private pg_dump/pg_restore recovery, literal-target/TLS/environment equivalence and post-restore protected quota/grant/uncertain allocation acceptance closed at6e05de7a9db9c81429990b772d9e1367189854f6 with exact fork feature SHA. Both48-parent affected races, both5-parent native/shared HTTP gates and full52-package Go passed. Task12 continues with [node-authority-discovery-plan.md](node-authority-discovery-plan.md); remote scope guards remain until delegated coordinator allocation/rate products are proven.

Authenticated node discovery checkpoint2026-10-03: owned process/socket/current boot and fresh challenge now traverse strict32KiB authenticated verified HTTPS. Both required actual-core/backend gates, native5-parent gates, affected races and full52-package Go passed; API catalog/inventory contracts and test-only PostgreSQL auth isolation were corrected. Evidence: [node-authority-discovery-testing.md](node-authority-discovery-testing.md). This is a Task12 transport prerequisite; delegated mode, global/node policy model/API/UI, actual budget/rate partitioning and two-node outage/recovery remain open. Sole review I1 was reproduced and fixed in one author pass; corrected both-backend actual/native/affected gates, full52-package Go and current source/core hash checks pass. Normal feature publication and independent SHA are recorded in the next receipt; continue durable delegated node mode before typed remote budget/rate transport.

Discovery publication receipt:69afd7b63db61a564157195f30d9dddd6d2f45f4 matches independent fork feature SHA. Continue [delegated-node-mode-plan.md](delegated-node-mode-plan.md) inline; original full parent remains required.

Durable delegated mode checkpoint2026-10-03: immutable schema2 role pinned in original journal evidence, fresh stopped setup over authenticated verified HTTPS, real delegated bootstrap without a local allocator, and source/role retention across restart. Both-backend seven-parent actual owned/HTTPS gates and five-parent Snell/mieru/SSH/shared-policy gates pass without failures/skips; full52-package Go, affected vet, frontend/OpenAPI/CI/hash checks pass. Evidence: [delegated-node-mode-testing.md](delegated-node-mode-testing.md). Task12 remains open for typed remote grants, global/node model/API/UI, budget/rate partitioning and actual two-node outage/recovery. Sole phase review raised two Important admission defects; both were reproduced and fixed in one author pass. Corrected15-parent owned/API and5-parent native gates on each backend, full52-package Go and33 current input/core hashes pass. All seven declined items have explicit scope/cost rulings; authorized feature publication and independent SHA follow before typed remote authority transport.

Durable delegated publication receipt2026-10-03: `084ecb9a0bc4b26ff71f583667e6b72a9b36f908` normally pushed and exact full remote feature SHA matched. Continue [remote-authority-transport-plan.md](remote-authority-transport-plan.md): six authenticated typed grant/demand operations with actual independent coordinator journal execution. Task12 global/node model/API/UI and actual two-node quotas/rates/outage acceptance remain mandatory; original native3/shared billing/rates/Tunnel priorities unchanged.

Typed authority transport checkpoint2026-10-03: six owned delegated-node HTTPS operations and a pinned structural remote adapter now execute independently journal-issued grants through production auth/TLS/CSRF32KiB boundaries. Actual TCP/UDP raw16/16,billed64 at2x, demand/install/get/pause/seal/monotonicrenewal, duplicate cumulative reports, lost-success conservative holds, actual restart/oldboot and genuine local issuer refusal pass on SQLite/PostgreSQL. Exact8phase/5nativeparents per backend/full52Go/vet/frontend/docs/YAML/current26source/core provenance verified. Sole fresh phase review/publication remain pending. Task12 global/node model/API/UI, canonical UUID mapping and actual two-node quotas/rates/outage remain unchecked; original full project/native3/sharedbilling/rates/Tunnel priorities persist.
