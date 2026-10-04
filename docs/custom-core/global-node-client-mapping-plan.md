# Global and node client mapping implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans inline, task by task. One fresh phase review after all tasks; one author correction pass. No earlier task review or second phase review.

**Goal:** Execute a coordinator grant for a canonical global client against a different node-local stable client and policy version, with durable authenticated mapping and exact single billing.

**Architecture:** Both independent journals retain the exact enrollment evidence. The node proves its original execution role and real current effective policy under the existing retained admission. A pinned mapped adapter translates cloned identity/version fields while preserving allocations, windows and cumulative usage.

**Tech stack:** Existing Go/GORM/SQLite/PostgreSQL/bbolt, paired custom Xray core, production Gin TLS/token routes, existing TypeScript OpenAPI catalog. No new dependency or sidecar.

**Spec:** `docs/custom-core/global-node-client-mapping-design.md`.

## Global constraints

- Preserve immutable stable resource IDs and credentials; no identity inferred from email or protocol credentials.
- Original priorities remain Snell/mieru/SSH, multiplier billing, directional limits and Xray TCP/UDP Tunnel.
- One canonical global allowance; never give each node the full global quota or rate.
- Keep explicit remote policy scope guards until subsequent global/node model/API/UI and actual two-node acceptance are complete.
- Strict authenticated verified TLS, existing32KiB bounds, fixed typed operation, source/boot/role proof and opaque failures.
- Initial enrollment is fresh only; exact retry after use is allowed for the same committed mapping. Consumed-source adoption requires following explicit sealed handoff.
- Translate explicitly proven global/local policy versions and effective policy; reports already contain billed bytes and are never multiplied again.
- Retain all failed logs, fixtures, writer probes and plan workspaces. Normal authorized feature push only; independently verify exact remote SHA. No full-project completion claim here.

## Review focus

1. Lost enrollment/install response must retain original mapping/allocation; retry cannot create another account or allowance. Tasks1/2/4.
2. Different canonical/local versions and identical effective policy must execute; a changed multiplier/rate/quota/expiry or unplanned version must refuse. Tasks2/3/4.
3. Unknown local demand sorting before mapped demand must not starve the mapped client. Task3 reads all128 bounded pending entries before filtering/truncation.
4. SQL import/restore, node deletion, role/source change or stale boot must not retarget durable mapping or settle uncertain capacity. Tasks1/2/4.
5. Older schema writers and concurrent enrollment/reopen must not erase the mapping fence or reset history. Task1, with all existing writer probes retained.

## Task 1: Original journal mapping evidence and compatibility fence

**Create:** `internal/policyauthority/client_mapping.go`, `client_mapping_test.go`.
**Modify:** existing `journal.go` metadata, `validate.go`, `reset_operation.go`, `reset_operation_progress.go` only where schema7/bucket-presence compatibility requires it.

**Interfaces:** `ClientMapping` carries `Authority Identity`, `NodeAnchor Identity`, `NodeID`, `SourceID`, `GlobalClientID`, `LocalClientID`, `GlobalPolicyVersion`, `LocalPolicyVersion`, `PolicyDigest`. `ClientMappingSide` constants `ClientMappingNode` and `ClientMappingCoordinator` distinguish the original anchor. `Journal.RecordClientMapping(side ClientMappingSide, mapping ClientMapping) error`, `LookupClientMapping(side ClientMappingSide, sourceID, localClientID string) (ClientMapping,error)` and `ClientMappings(side ClientMappingSide, after string, limit int) ([]ClientMapping,error)` commit/return value copies. Stable UUIDs are canonical; versions are positive bounded integers and digest is64 lowercase hex. A source/global reverse index rejects two local clients for one canonical client at the same source. The same canonical client may map to separate actual node sources.

- [x] Freeze current-schema6 policy-authority source and build a standalone older-writer refusal probe outside retained old workspaces. Add owning tests for immutable exact retry/conflict, reversed collisions, wrong original role/source, unknown/deleted/consumed account, reopen, corruption and bounded records.
- [x] Obtain meaningful behavioral RED; preserve compilation/setup failures separately. Run `go test -race -count=1 -v ./internal/policyauthority -run '^TestClientMapping'`.
- [x] Add schema7 mapping bucket and transactional original-role/account validation. Metadata retains `mappingBaseSchema`4/5/6; reset helpers validate that underlying floor and advance it only when real capture/progress is committed. Do not manufacture empty reset histories. All reset operations must retain schema7, never downgrade it. Reopen validates every record/reverse index/source/anchor and the matching account's monotonic version without demanding its later usage remain0.
- [x] GREEN all mapping parents, full journal tests and old schema4/5/6 writer refusal with exact source/probe hashes. Commit and record task completion.

**Completion:** every named `TestClientMapping*` parent passes, zero FAIL/SKIP/no-tests, full journal tests pass, three actual old-writer probes refuse mapped journals without changing bytes.

## Task 2: Owned policy proof and production enrollment

**Create:** `internal/web/runtime/node_client_mapping.go`, `node_client_mapping_json.go`, `internal/web/service/node_client_mapping.go`, `node_client_mapping_test.go`, `internal/web/controller/node_client_mapping.go`, `node_client_mapping_test.go`.
**Modify:** existing node-authority route registration, exact node-sync inventory, `frontend/src/pages/api-docs/endpoints.ts`, owning catalog contract tests and both generated artifacts.

**Owned core prerequisite:** extend the existing private GetClient `ClientState` with additive bool `authority_grant_history = 7`, derived from retained current/previous persisted grant. Add `Snapshot.AuthorityGrantHistory`, current/previous grant inspection under existing client lock, `client-authority-history-v1` capability and owning core/RPC tests. Regenerate the existing protobuf source with the pinned tools and build a current-source paired native core for service/HTTP acceptance. Old peers without the capability refuse enrollment; other controls remain compatible. No new RPC or business service.

**Interfaces:** `NodeClientMappingRequest` has the existing five-field binding plus explicit canonical/local UUIDs, canonical/local version pair and expected effective-policy digest. `ClientPolicyNodeService.EnrollClientMapping(ctx, request)` returns committed evidence with the actual source/boot/role. Production POST `/server/clientPolicyAuthority/enroll` uses exactly this DTO; it never edits credentials or policy. `EffectiveClientPolicyDigest(*clientpolicy.PolicyConfig)` deterministically clears client ID/version before hashing, retains enable/multiplier/quota/expiry/rates/burst/baselines, and validates the original complete policy.

- [x] Add actual-core owning parents `TestNodeClientMappingRequiresOwnedFreshPolicy` and `TestNodeClientMappingPreservesOriginalEvidence`, HTTP parent `TestNodeClientMappingHTTPAuthenticationAndBounds` and catalog/schema RED. Use the existing independent-journal fixture, fresh stable UUID, complete valid policy and canonical version7/local version1.
- [x] Core history RED/GREEN proves zero-used installed/sealed grants remain nonfresh across restart; private RPC field is true and capability present. Older actual core cannot supply fresh proof. Freeze actual core inputs and binary SHA before using it for all owning service/HTTP tests.
- [x] Strictly decode every required field and canonical string integer, reject aliases/duplicates/null/unknown/oversize. Retain SQL/owner admission across GetClient and journal commit, revalidate after both; require desired SQL policy and real core policy/version agree. Initial fresh evidence must include all hidden usage/reset/grant history, not only visible traffic0.
- [x] Commit exact node evidence once; identical retry can return it after consumption if original tuple/policy remain valid. A different mapping, original role/source mismatch, deleted or changed policy refuses. SQL restore can recover a disposable projection from original evidence but cannot retarget it. Test post-RPC loss and retained SQL boundary within owning parents.
- [x] Add the production fixed route under existing TLS/token/CSRF/node-sync chain, complete source schemas/examples, generation/equality/typecheck and contract tests. Verify both SQLite/PostgreSQL exact parents and native local-issuer refusal. Commit/task completion.

**Completion:** three literal parent tests pass on both backends with actual core, zero FAIL/SKIP/no-tests; both backend markers present, catalog contracts/generation/equality/typecheck and affected vet pass.

## Task 3: Canonical mapped adapter and coordinator evidence

**Create:** `internal/web/runtime/remote_client_mapping.go`, `mapped_remote_authority_api.go` and owning tests; `internal/web/service/client_policy_node_mapping.go` and tests; `internal/database/model/client_policy_node_mapping.go`.
**Modify:** `internal/database/db.go` model registration.

**Interfaces:** `Remote.EnrollClientMapping` speaks strict verified transport. `NewMappedRemoteAuthorityAPI(pinnedAPI, committedMappings)` implements the existing structural authorityDemandAPI. Coordinator enrollment persists the exact returned mapping in its independent journal before publishing a disposable SQL projection; factory accepts journal evidence rather than arbitrary SQL rows.

- [x] Add parents `TestRemoteClientMappingRequiresVerifiedProof`, `TestMappedRemoteAuthorityAPITranslatesCanonicalIdentity` and `TestCoordinatorClientMappingPinsOriginalJournal`. RED includes canonical/local UUID and version inequality, unknown demand before known demand, altered source/role/boot/policy, nil/canceled context, result/input clone ownership and SQL rollback/retry.
- [x] Clone and translate client IDs and the proven version pair in demands, install/state, get/pause/seal and renewal; preserve exact grant/window/sequence/challenge/shares/capacity/usage/remainder. Always fetch all128 bounded pending local requests before filtering and returning at most the requested limit; unknown clients receive no allowance and cannot hide mapped clients.
- [x] Retain immutable mapping snapshots; check pinned source/node/coordinator identity before use and reject unknown/changed versions. Persist coordinator evidence before SQL projection, require canonical account/current version, and use atomic uniqueness for local reverse mapping. No default allocation or scope-guard relaxation.
- [x] GREEN full runtime and owning coordinator tests on both backends, existing exact transport parents, vet and current source/probe hashes; commit/task completion.

**Completion:** exact three parents pass with zero FAIL/SKIP/no-tests, runtime suite and both backend coordinator parents pass; real transport request bodies contain the local identity/version while coordinator results contain canonical identity/version, with all other fields unchanged.

## Task 4: Actual mapped TLS grants, exact billing and current gates

**Create:** `internal/sub/node_client_mapping_http_runtime_test.go`; mapping testing/decision documentation.
**Modify:** `.github/workflows/custom-core.yml`, original implementation plan checkpoint.

**Interfaces:** Production enrollment + mapped adapter + independently journal-issued canonical grant execute against the real retained native node. Existing transport/native helpers may be reused; no invented grant success or direct business-counter injection.

- [x] Add literal parent `TestNodeClientMappingHTTPActualCanonicalGrant`, actual SQLite/PostgreSQL markers, canonical UUID different from local UUID and canonical version7 different from local version1. Prove fresh real effective2x policy before enrollment; obtain meaningful failure before mapped support or retain prior-correct characterization honestly.
- [x] Actual demand → canonical Issue → mapped Install → literal TCP8/UDP8 echo → canonical raw16/16/billed64 → duplicate cumulative report/seal conservation. Confirm only the canonical coordinator account is charged, node mapping stays immutable, no second multiplier, no local allocator and no grant0bytes.
- [x] Cover successful enrollment with lost HTTP reply/idempotent recovery, SQL projection deletion/restore recovery, conflicting enrollment, actual node restart/stale adapter refusal, real policy version change refusal, current mapping reopen and held uncertain capacity. Required actual local-issuer complete valid enrollment refusal. Faults retain original source anchors; source/role change never auto-rebinds.
- [x] Add exact backend CI parents, reject nested skips/no-tests/missing/duplicate parents/wrong markers, retain native5 and all transport gates. Freeze every changed source/module/generated/core/probe hash; both backend mapping+transport and native5, full Go with three old-writer probes, vet/frontend/YAML/current hashes pass.
- [ ] Commit/task completion. One sole fresh phase review evaluates all tasks/ledger/rulings, one meaningful author correction pass if required, then normal authorized feature push and independent exact SHA. Continue original global/node model/API/UI, real two-node aggregate quota/rates and consumed-source handoff immediately after publication; no phase-only final.

**Completion:** literal actual mapping parent and all owning mapping parents pass on both backends with zero FAIL/SKIP/no-tests and exact backend markers; both native5 gates/full Go/old-writer refusal/vet/catalog/typecheck/generated equality pass on the final frozen snapshot. Review findings and every declined scope explicitly ruled, normal publication independently verified.

## Following original parent work

The next product phase implements explicit global/node policy scopes in model/API/UI and managed coordinator ownership, with at least two actual isolated node processes. Global quota/rate/burst shares never overlap; node accounts and display remain independent. It must also implement policy update version proofs and consumed-local-source sealed handoff, multi-node historical seed deduplication, fractional settlement, deletion/restore/outage semantics and durable reset acknowledgements. Original protocol, distribution/platform, remaining sidecar migration, subscription/export/notifications, scale/performance and release acceptance remain required.
