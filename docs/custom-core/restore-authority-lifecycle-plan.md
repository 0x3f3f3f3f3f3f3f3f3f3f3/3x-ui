# Restore authority, lifecycle and coordinated execution

This continues tasks 11 and 12 of [implementation-plan.md](implementation-plan.md), under [requirements.md](requirements.md), [architecture.md](architecture.md) and [accounting.md](accounting.md). Execute inline. Existing Snell v4/v5/v6, v5 QUIC, mieru, SSH and shared multiplier/rate/Tunnel acceptance remains a required regression gate. The original broader project, including legacy business-path migration and platform/device acceptance, remains open.

Base: `749d7d9f435a00725823c99fd8aae5cd20154b61` (documentation-only after tested product source `5ef27753`). The preceding bounded stage pins source snapshots and rejects old queued transactions/delayed ledger replies after pool replacement. It does not serialize imports, drain active transactions, fence forced runtime restart or prevent consistent SQL/core snapshot rollback from recreating budget.

## Required invariants

- Canonical UUIDs, business credentials/host keys, accumulated directional and billed totals, fractions, reset history, receipts, revocation tombstones and unknown-resource evidence survive recovery. Restoring configuration is not permission to reduce accounted usage or make outstanding budget available.
- Each node still has one Custom Xray business process. Protocol libraries and Tunnel continue to use the shared admission engine. The issuer carries control messages only.
- A current SQL handle, a bbolt epoch and a copied lease are insufficient activation authority. Every managed process boot receives a new unpredictable incarnation before business listeners open; admission requires a current grant bound to that incarnation, issuer generation, source, client UUID, quota window and policy version.
- The issuer's durable issuance/settlement journal is outside SQL import and execution-store snapshot restoration. Missing or contradictory activated authority fails closed. Initialization is a separate, explicit migration; no missing-state recovery may silently create a fresh issuer or account.
- The authoritative commit precedes exposing a grant. SQL is an idempotent projection; a projection failure cannot roll back issuance or permit a second grant. Lost response retries return the original issuance.
- Global billed capacity is divided into finite, nonoverlapping grants. Confirmed usage plus unreconciled grant capacity never exceeds the window's authorized capacity. Expiration, deletion, offline nodes or uncertain restore do not reclaim unproven unused budget. Multiplier is applied once in the core, never at aggregation.
- Directional limited rate and burst shares have explicit sums bounded by the global policy. A zero share means unavailable, distinct from an explicitly unlimited direction. Old shares stay unavailable until their enforcement interval has ended or a current incarnation has durably sealed them.
- Expiry enforcement uses a process monotonic deadline. Restarts cannot reuse persisted activation grants. Issuer restart/clock changes must preserve old-rate exclusion conservatively rather than treating a wall-clock jump as permission to overlap allocations.
- One control authority is assumed. Copying the entire authority and concurrently operating both copies requires an external fencing source; a local file lock cannot prove that exclusion. Document that boundary and do not claim arbitrary whole-machine cloning safety.

## Execution sequence

## Task 1: Reproduce lifecycle and cloning failures

Files: isolated database/service recovery tests and core persistent policy tests.

Observe before fixing: two staged imports contend for the shared temporary/fallback names; forced restart can enter after stop while replacement is pending; SQL publication races readers and exposes a pool before migration finishes; a running serialized transaction overlaps replacement; copied execution stores can each admit the old allowance under matching source/epoch/cursor. Keep actual failure logs and fixtures.

Use deterministic barriers, real temporary SQLite and private PostgreSQL schemas. No tests touch the production state or management SSH. The copy test must show independent successful payload admission rather than only comparing IDs. Separate an unsupported external authority clone from the node-store clone this implementation can fence.

Commit these regressions with their first passing implementation in logical units; an observed failing test alone is not a delivered feature.

## Task 2: Serialize and fence the complete import lifecycle

Files: `internal/database`, `internal/web/service/server.go`, `traffic_writer.go`, `database_restore_runtime.go`, `xray.go`, affected managed bootstrap/reset/deletion callbacks.

Give each import a private staging path and one lifecycle owner. Close mutation/runtime admission before confirming stop; quiesce and join old accounting work, including active transactions. Keep source generation with asynchronously prepared operations. Synchronize DB publication; publish only a fully migrated pool. All three routes share the same phases and error policy.

Phases are validate, fence, stop, drain/checkpoint, install/reopen, authority reconciliation and activate. A post-install reconciliation/reopen failure leaves business listeners fenced. Restart only a known usable pre-install state or an acknowledged new state. No unconditional error defer may activate an uncertain database. Handle repeated imports and recovery after panel termination without deleting business history.

Expected: concurrent import/forced restart/write/reset/ledger tests pass under race on both backends; normal successful import and safe pre-install failure recovery remain usable. Capture lock order and context ownership in the ledger; avoid holding SQL writer locks over network calls.

## Task 3: Implement the persistent issuance authority

Files: a dedicated control-plane authority package, configured private journal path, SQL projection and migration/bootstrap integration.

Use the already pinned bbolt dependency for synchronous bounded journal commits. Define generation, per-client window, source/incarnation, issue request ID, grant sequence, finite billed capacity, directional rate/burst shares, deadlines, committed usage and sealing status. Validate limits/overflow before mutation. Require matching persisted identity and exclusive open; creation must not overwrite an existing journal.

Initial migration takes one drained, coherent view of current local SQL plus execution state. It preserves existing lifetime usage/reset/deletion evidence and explicitly freezes uncertain capacity. Authority and projection retry boundaries are durable. Keep older evidence/source files intact.

Tests: concurrent issuance cannot overlap quota or rate/burst sums; response loss/idempotent retries, transaction failure before/after commit, process exit/reopen, corrupt/missing/wrong-identity journal, restored old SQL/core snapshots, deleted IDs, explicit reset and quota decrease. Demonstrate that the issuance record survives SQL rollback before allowing the next grant.

## Task 4: Enforce boot-bound grants in Custom Xray

Recovery continuation after the verified same-policy paired-snapshot case:

1. Persist bounded, credential-free policy/reset evidence in the same journal transaction as its policy change. Exact retries retain it; conflicting evidence is rejected. Preserve existing seed/migration hashes and older journal records.
2. Read committed changes through exact or bounded per-client queries. Before activation, restore acknowledged reset boundaries and tombstones from protected evidence, retain the current journal window, and advance any restored desired version monotonically. Missing or contradictory evidence remains closed.
3. Reconcile only monotone known local usage into a dormant, current-boot core through its private authenticated control channel. Do not return old held grants or reset the authority window. Preserve any execution usage already ahead of that floor.
4. Distinguish acknowledged protected reset history from the current disposable execution epoch/cursor in accounting. Full reset, policy change, deletion, response-loss and fault acceptance precede enabling activation from restored snapshots.
5. Keep node scope guards throughout; the local recovery path does not seed multiple nodes with duplicated global lifetime counters. Bounded batch/reset membership and original migration history must also survive recovery before claiming complete history support.

Files: `core/xray/app/clientpolicy`, protobuf/private control API, `infra/conf`, protected process startup and panel runtime adapter.

Generate a fresh boot nonce independently of persisted execution epoch. Add capability negotiation and a private grant RPC. Bind admission, finite reservation capacity, directional shares and expiry to the current nonce and grant. Existing raw/billed/fraction state remains intact. Do not authorize from the stored policy alone. Grant loss/expiry closes applicable active sessions and rejects reconnection; control traffic remains unbilled.

Seal a superseded grant at an atomic admission boundary before unused capacity can be acknowledged. Persist spent/frozen capacity and committed counters together. Recovering old execution state cannot acknowledge capacity already consumed by another incarnation. A duplicate/replayed grant never resets a limiter, quota or cursor.

Tests: copied stores under one authority obtain disjoint grants or denial; replay a grant into a new boot and observe no listener/payload admission; crash with outstanding capacity remains unavailable; actual Tunnel plus native Snell/mieru/SSH share the same bound. Check policy hot updates, quota reset, expiry and credential revocation retain their existing semantics.

## Task 5: Wire authenticated nodes and scope through the product

Files: existing node manager/runtime, API/database models, forms, config export and accounting views.

Use the existing authenticated encrypted node channel and private local core adapter. Distinguish global and node-scoped policies in saved/API/UI data. Nodes request bounded grants; the master reconciles cumulative confirmed usage without applying multiplier again. Preserve identity across attachments and report conservative unavailable capacity separately from raw delivered traffic.

Keep current managed remote-scope guards until issuance, execution and settlement are implemented together. Then replace them with validated capability/scope checks. Reject an unsupported or unavailable issuer before activating managed business traffic. Avoid exposing technical lease details in ordinary user flows unless needed for an accounting decision.

Tests: two actual nodes with the same canonical client and two core processes enforce aggregate quota/rate shares, including TCP/UDP Tunnel and protocol aliases; outage, expiration, lost/reordered/duplicate reports, node removal, stale incarnation and SQL projection failure. No claim of perfect work-conserving fairness; state the utilization tradeoff of reserved shares.

## Task 6: Recovery acceptance and publication

Run actual backup/restore after known consumption, paired software rollback with retained authority, old SQL plus old core-state restoration, concurrent imports, and crash at each lifecycle phase. Include both SQLite and PostgreSQL without treating fixture-only routes as database integration evidence. Produce real endpoint/admission measurements, bounded unavailable budget and rate/burst sums.

Run appropriate race/static/full backend and existing native/shared-Tunnel gates, then one fresh whole-stage review and one correction pass. Build one clean paired source checkpoint with accurate source/compatibility stamps. Preserve every previous package/log/fixture. Integrate and normally push only the authorized fork feature, checking its remote SHA. No force push, default merge, release or deployment.

## Review focus

Review issuer commit versus SQL projection, lost responses, incarnation replay, finite capacity arithmetic, old shares across restart/clock drift, reset/deletion history, copied node execution state and import error activation. Inspect all direct/background mutation and runtime entrypoints for bypasses, lock ordering and stale context. Verify the test suite proves payload rejection and aggregate bounds; enumeration, mock success, skipped protocols and persisted cloneable IDs do not establish enforcement. Whole-authority external cloning remains a documented boundary, never an implied guarantee.
