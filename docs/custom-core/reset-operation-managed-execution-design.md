# Durable managed reset preparation and core completion

## Intent and scope

The original priority remains one Custom Xray serving Snell v4/v5/v6 (including v5 QUIC), mieru and SSH, common multiplier billing, directional limits and Tunnel/dokodemo-door TCP/UDP forwarding. A reset must retain its original quota boundary through SQL/core restoration. This integration connects the existing schema6 preparation/completion journal to ordinary public managed resets and genuine empty operations. It is a step of the active restore-authority parent, not completion of the wider project.

The user authorized ordinary engineering decisions from source, official documentation and experiments, with recorded reasons. Execute inline in the existing isolated worktree. Preserve source, evidence, fixtures and plan workspaces. No repeated design permission or publication gate is needed for local work.

## Existing behavior and selected approach

Public capture already retains original request/calendar selection and managed membership before SQL commit. SQL `Applied` is set while preparing, before actual core application. Per-client authority changes retain committed reset baselines, but an operation has no independent exact prepared witness or core completion acknowledgement. Losing the SQL transaction after preparing can otherwise cause retry to select a later usage boundary.

Use the existing source-bound `PrepareResetOperation` and `CompleteResetOperation` interfaces. Preparation is committed before the SQL preparation transaction returns. Completion is committed only after that transaction and successful managed policy application and resumption. Neither SQL `Applied` nor capture alone constitutes completion. A failure preserves preparation and omits completion. The first successful preparation installs the existing schema6 old-writer fence atomically; no new schema or dependency is needed.

An alternative of marking completion from SQL `Applied` is rejected because it precedes core acknowledgement. An alternative of adding another storage abstraction is rejected because the already tested bounded journal supplies the exact dependency digests and writer fence.

## Typed payload and identity

Use a strict Schema1 preparation payload containing the original request, exact reset time, active managed UUIDs, affected result and original semantic per-client reset rows. Bind it to the original capture digest and source/authority identity. Do not include credentials or repeat the full potentially large target selection. Reset rows are keyed by source/client/request; surrogate SQL IDs are normalized for semantic comparison and regenerated on restoration. Policy request remains the existing `batch:` SHA256 of the public request.

Validate strict JSON with no trailing values, sorted unique membership, capture subset, exact managed-result correspondence, valid reset usage/version/source/request and bounded cardinality. A retained preparation wins over eligibility or receipt recomputation. A completed operation retains the same immutable preparation on retry, including no-op operations and later desired configuration/reset changes.

## Locking and execution

Acquire the lifecycle lock for the whole application, including zero-managed operations. Split the existing managed reset helper into its locking public/private wrapper and an implementation requiring the caller's lifecycle lock. Pin the SQL handle with the existing serialized transaction helper; check the authority's original database/source before preparation and completion. Do not hold a SQL transaction across RPC. Authority methods keep their own mutex and checkpoint/suspend/apply/resume behavior. A completion journal error is a failed operation and requires restart, not success.

Initial preparation/completion support is for captured managed-only and genuinely empty operations without calendar inbound or other legacy effects. Operations with client legacy effects or captured inbound counter/remote effects continue existing behavior and conservative ambiguity refusal; they must not acquire a false completion. Manual inbound stamp effects also require their own exact effect identity and remain outside this first managed execution integration. These effects, durable remote acknowledgements and full protocol business migration remain required parent work.

## Recovery

Before compiling cold-start policies, traverse existing bounded service captures, validate their preparation and completion dependencies, restore original operation progress and exact managed reset rows. Do not acknowledge execution during metadata recovery. Restore missing semantic rows without overwriting later desired configuration or later acknowledged reset boundaries. A prepared boundary ahead of a retained account can represent SQL/core interruption; preserve the original reset and let the ordinary authority reconciliation apply it after recovery. Never replace a renamed UUID with an email reuse or recreate a deleted identity. Invalid/conflicting preparations fail before activation.

Existing protected account history remains authoritative for later committed changes. Recovery must coordinate its ordering with pending preparations so that older seed/account metadata does not suppress a pending newer reset. Exact funding, held capacity and lifetime billing remain unchanged by metadata projection. Completion survives recovery and retry; its original result is retained even when clients were later deleted.

## Acceptance and remaining parent work

Use real manifest-owned core and public all/bulk/calendar resets. Observe actual missing-witness RED before implementation. Verify warm and later Tunnel payloads at exact 2x billing, no-op eligibility, missing SQL row, failed SQL commit after preparation, failed core application before completion, exact retry and cold recovery. Include later reset/configuration edits, renamed/deleted UUIDs, damaged witness, cancellation and source/database replacement, finite funded grants and both SQL backends. Keep earlier schema4/schema5 original-writer and native protocol gates with explicit provenance; do not label opaque storage witnesses business acknowledgements.

Full legacy/inbound effects, remote idempotency, whole-operation owner-loss/cloning, multi-node accounting/rates, all restore/crash routes, scale/platform/device acceptance and final current paired distribution remain open until their own actual tests pass. Snell v6 reference currently is RC, not stable. No whole-project readiness or deployment claim follows this bounded integration.
