# Actual startup operation acknowledgement implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans inline. One fresh bounded final review and one author correction pass. Original autonomous engineering authorization applies; retain workspaces/evidence.

**Goal:** Close interrupted protected operation preparations after actual owned core activation.

**Architecture:** Existing typed preparations, boot-bound core state queries and pinned source/SQL completion writes;128-header pages. Startup failures stop uncertain execution; metadata recovery remains non-acknowledging.

**Tech Stack:** Go1.27.1, GORM SQLite/PostgreSQL, existing policy command API/protobuf and journal.

**Spec:** docs/custom-core/startup-operation-ack-design.md.

## Global Constraints

- Native Snell/mieru/SSH and shared multiplier/directional limits/TCP-UDP Tunnel stay the priority.
-128 journal header pages;1000-ID SQL batches;100000 clients maximum per operation.
- Actual core RPC outside SQL transactions; immutable completion follows actual activation and repeated source/handle validation.
- No completion or grants from metadata-only projection; preserve later policies/windows, deleted identities and original usage.
- Preserve artifacts/workspaces; only normal authorized fork feature pushes with independent SHA verification; no force/default merge/release/deploy.

## Review Focus

- Prepared intent without an actual successful current incarnation cannot gain completion, even when restored SQL says applied.
- Later desired edits/windows and deleted/reused UUID/email/numeric ID cannot be overwritten or substituted for original execution.
- Replaced process/socket/owner/source/SQL handle and cancellation must prevent uncertain acknowledgement and retain closed listeners.
- Historical, capture-only, already-completed and unsupported effects retain explicit compatibility and cannot acquire invented witnesses.
- Paged progress and interrupted completion keep original preparation/billing unchanged; private fixtures must not be claimed as public scale/platform proof.

## Task 1: Actual source-owned startup completion

Files: create internal/web/service/client_policy_authority_startup_completion.go and client_policy_authority_startup_completion_test.go; modify client_policy_activation.go to complete only after StartManagedProcess success; use existing operation decoders and socket/owner/source guards.

Consumes: managedAuthority retained state/API/socketBoot/db, typed direct/batch/renewal preparations and current compiled config. Produces: `(*managedAuthority).CompleteStartupOperations(ctx context.Context) error`, typed pending operation description and actual-core/current-policy verification; no new core RPC or journal schema.

- [ ] Step1: Add TestManagedAuthorityStartupCompletesPreparedOperations for direct/batch/renewal/inbound actual interruption, metadata recovery without completion, actual restart then completion and subsequent exact payload/billing. Expected RED: actual startup leaves preparation uncompleted.
- [ ] Step2: Implement known typed preparation traversal and boot-bound exact core-policy/usage proof with source/SQL validation, immutable completion after successful activation and stopped process on failure. Expected GREEN: original completed records, no second reset or allowance.
- [ ] Step3: Add TestManagedAuthorityStartupAcknowledgementPreservesLaterState for later multiplier/window, deletion/reused identifiers and current inbound configuration; TestManagedAuthorityStartupAcknowledgementRefusesUncertainExecution for cancellation, socket/owner/source/pool/core state and completion failures. Expected: current legitimate state retained; uncertain startup closed and no completion.
- [ ] Step4: Add TestManagedAuthorityStartupAcknowledgementKeepsMetadataAndCompatibilityBoundaries for metadata-only/capture-only/schema1/unsupported/already-completed cases. Run four new parents and affected owned lifecycle/reset/renewal/restore races both backends, vet/diff, commit/task-done. Expected PASS with genuine backend scope.

## Task 2: Paged interruption, regression and publication

Files: focused startup completion tests, CI required parent lists, startup-operation-ack-testing.md and plan/ledger.

- [ ] Step1: Add TestManagedAuthorityStartupAcknowledgementContinuesPagedProgress with129 private no-op prepared operations spanning two pages, interrupted completion and actual restart; preserve every original digest and account. Expected RED if paging or durable continuation is absent; private workload scope explicit.
- [ ] Step2: Require all five startup parents in both CI gates; run affected real-core races, five actual internal/sub native/shared HTTP parents and full make test-go with exact original writer probes; fresh vet/YAML/name/diff/hash checks. Expected PASS; record every failure/skip honestly.
- [ ] Step3: Record complete evidence/remaining parent boundaries, commit/task-done and sole fresh whole bounded review; one author Critical/Important TDD correction plus full suite if required. Expected no bounded blockers; no re-review.
- [ ] Step4: Normal authorized feature push and independent SHA verification, close ledger and continue original parent work. Expected remote/local equality; no final whole-project claim until original brief is handled.
