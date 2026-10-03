# Protected inbound reset stamp implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans inline. One fresh whole bounded review; no implementer agents. Original autonomous engineering authorization supplies execution method and ordinary decisions.

**Goal:** Preserve the original managed inbound reset resource and timestamp across SQL rollback, restoration and retry.

**Architecture:** Strict schema2 capture/preparation in the existing reset journal; stable inbound UUID selection and monotonic timestamp projection. Preserve schema1 history and unsupported-effect guards.

**Tech Stack:** Go1.27.1, GORM SQLite/PostgreSQL, existing custom-core policy API and journal.

**Spec:** docs/custom-core/inbound-reset-stamp-design.md.

## Global Constraints

- Native Snell/mieru/SSH and shared multiplier/directional limits/TCP-UDP Tunnel remain the priority.
-100000 captured resources maximum;1000-ID SQL batches and128 journal header pages.
- Preparation before SQL commit; completion after actual core execution; no RPC in SQL transactions or metadata funding/acknowledgement.
- Canonical nonzero resource UUID; retain schema1 compatibility and all original evidence/workspaces.
- Push only normally to authorized feature/custom-xray-unified-policy; independently verify SHA; no force/default merge/release/deploy.

## Review Focus

- Capture-only retry after numeric ID reuse or newly added resources cannot expand original membership.
- SQL restore and actual commit failure preserve exact original stamp and usage while metadata recovery leaves funded accounts/grants unchanged.
- Later legitimate stamp/configuration edits and changed attachments remain current; absent UUIDs cannot acquire old effects.
- Historical schema1 and unsupported legacy/calendar effects remain readable and cannot gain a false completion.
- Malformed, oversized, duplicate, foreign-source or inconsistent capture/preparation refuses before projection.

## Task 1: Original resource capture and owned execution

Files: modify client_policy_authority_reset_capture.go, client_policy_authority_reset_execution.go, client_traffic_reset_batch.go; create internal/web/service/client_policy_authority_inbound_reset.go and client_policy_authority_inbound_reset_test.go; update old unsupported-effect test only for the newly supported managed manual scope.

Consumes: model.Inbound.StableID, authorityResetExecutionStateLocked and existing journal APIs. Produces: authorityResetInboundIdentity{ID int, StableID string}, authorityResetInboundStamp{StableID string, ResetAt int64}, strict original membership validation and protected stamp application/preparation.

- [x] Step1: Add TestManagedAuthorityInboundResetRetainsOriginalStamp with actual owned Tunnel warm traffic, private journal preparation/completion and exact stamp/reset boundary. Run focused race. Expected RED: public managed manual inbound reset lacks preparation.
- [x] Step2: Implement schema2 manual inbound captures, original UUID-only stamp updates and schema2 prepared effects; preserve schema1 and no false legacy/calendar completion. Expected GREEN: exact original execution stamp and immutable retry.
- [x] Step3: Add TestManagedAuthorityInboundResetCaptureRetryPreservesResourceSet for capture-only single/all scope after deletion/reused ID, addition, rename and attachment changes. Expected: only original surviving UUIDs stamped; original client selection retained. Run both tests and existing unsupported-effect compatibility tests, commit/task-done. Expected PASS with actual core.

## Task 2: Exact recovery and strict refusal

Files: same focused service file/tests and reset execution recovery integration.

Consumes: Task1 original capture/prepared stamps. Produces: recoverAuthorityInboundResetStampsTx(tx *gorm.DB, stamps []authorityResetInboundStamp) error, called by prepared reset metadata recovery.

- [ ] Step1: Add TestManagedAuthorityInboundResetColdRecoveryPreservesOriginalResources with actual original reset/payload then coherent SQL restoration, later/renamed/deleted/reused resources, repeated metadata projection and full funded account/grant invariance. Expected RED: original timestamp missing after restoration.
- [ ] Step2: Implement strict UUID monotonic timestamp projection; add TestManagedAuthorityInboundResetCommitFailureRecoversOriginalStamp with actual deferred commit failure and no false completion. Expected GREEN: exact original stamp/billed boundary restored and no acknowledgement or funding mutation.
- [ ] Step3: Add TestManagedAuthorityInboundResetRejectsInvalidPrograms for unknown schema/field, invalid/duplicate UUID, wrong membership/time/source and contradictory capture; keep historical compatibility. Run both real backends, vet/diff, commit/task-done. Expected PASS with provenance explicit.

## Task 3: Regression and publication

Files: CI required parent loops, inbound-reset-stamp-testing.md and this plan/ledger.

- [ ] Step1: Add all five owned parents to both CI gates; run affected resets/renewals/restore/identity, five native/shared HTTP parents, fresh vet and full make test-go with actual core and original writer probes. Expected PASS; all failures/skips recorded.
- [ ] Step2: Record evidence/parent boundaries, commit/task-done, one fresh whole bounded review. Expected: no bounded blockers after one author Critical/Important RED/GREEN correction and full suite; no second review.
- [ ] Step3: Normal authorized feature push, independent SHA check and ledger closure. Expected remote/local equality; continue remaining full parent work.
