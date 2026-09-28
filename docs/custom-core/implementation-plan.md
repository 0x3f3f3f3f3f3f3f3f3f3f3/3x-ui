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
- [ ] Import original core with license and origin manifest; point panel module replacement at it.
- [ ] Build both baselines; record failures/skips without claiming full validation.
- [ ] Commit source import separately from implementation; push and verify remote SHA.

## Task 2: Policy arithmetic and runtime state

Files: `core/xray/app/clientpolicy/{config.proto,policy.go,accounting.go,limiter.go,engine.go,*_test.go}`.
Produces: `Engine.Apply(Policy) error`, `Engine.Open(context.Context, Metadata, func()) (*Session,error)`, `Session.Admit(direction,bytes) error`, `Engine.Snapshot(clientID)`.
Policy values use uint64 byte rates, an explicit burst, multiplier millionths and monotonic version; client identity is a nonempty opaque server-owned string. Admission is serialized per client for atomic quota, separate upload/download token buckets and connection registry. Metadata retains inbound, authenticated account, session ID, original/actual target and active policy version.

- [ ] Write failing tests for fractional/batch-invariant accounting, multiplier switch, overflow and invalid fields.
- [ ] Implement fixed-point arithmetic and inspect passing tests, including property/fuzz seeds.
- [ ] Write failing concurrent quota and restriction-composition tests; implement policy state and cancellation outside locks.
- [ ] Test two rates, unlimited control, concurrent streams, fairness and hot updates before implementing limiter.
- [ ] Commit tested runtime primitives; do not call this a delivered data plane until task 3 passes.

## Task 3: Real Tunnel data path

Files: `core/xray/proxy/dokodemo/*`, `common/protocol/user.*`, `common/session/*`, `app/dispatcher/*`, `infra/conf/*`, `testing/scenarios/*custom*`.
Consumes task 2. Produces trusted `client_id` in runtime user/session context and enforcement wrappers around decrypted payload.

- [ ] First write a failing real TCP/UDP fixed-target test that exceeds a shared client budget through two listeners.
- [ ] Extend protobuf with new field numbers; retain old fields and names; reject missing policy for managed identities.
- [ ] Bind Tunnel listener ownership, meter both directions once and disable raw-copy bypass only for managed sessions.
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
