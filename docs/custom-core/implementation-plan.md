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
- [ ] Cover VLESS/VMess/Trojan/Shadowsocks/Mixed/HTTP/Hysteria/WireGuard/TUN as applicable; distinguish IP-packet accounting.
- [ ] Audit nested dispatch, loopback, mux/XUDP, sniff cache and raw/splice/Vision for bypass/double charging.
- [ ] Test authenticated client-ID routing, original versus rewritten target, DNS/balancer/block and loop detection.
- [ ] Report managed versus upstream throughput and leave unmanaged optimizations intact.

## Task 6: Panel vertical integration

Files: `internal/database/{db.go,model/*}`, `internal/web/{service,controller,runtime}/*`, `internal/xray/*`, `frontend/src/{schemas,pages/clients,pages/inbounds,pages/api-docs}/*`, translations and generators.

- [ ] Migrate SQLite/PostgreSQL legacy clients to stable IDs, preserve usage, default multiplier 1/unlimited rates; test rename/rotation/import.
- [ ] Bind existing client records to forwarding rules, node, ACL, outbounds and exclusive listener resources.
- [ ] Extend Runtime lifecycle, batch operations and state/reason/statistics UI, API registry/codegen and all locale keys (English/Chinese translations).
- [ ] Test wildcard/dual-stack/control-port collision, reassignment, reset/renew restrictions and active connection termination.
- [x] Core prerequisite: drain established TCP/UDP/Unix connections on inbound removal; test same-port Tunnel reassignment and unaffected sibling listeners.
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
