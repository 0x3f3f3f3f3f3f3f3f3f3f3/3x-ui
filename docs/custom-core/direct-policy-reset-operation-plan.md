# Direct managed reset operation implementation plan

> Execute inline with superpowers:executing-plans. No implementer agents; one fresh final review and one author TDD correction pass. The user's existing autonomous instruction covers ordinary engineering and local design/plan decisions.

**Goal:** Preserve original direct/single managed reset identity and exact quota boundary through SQL interruption/restoration, with durable preparation and actual core completion.

**Architecture:** Separate strict typed `policy-reset:` envelopes in the existing schema6 journal; existing lifecycle/pinned SQL and managed core apply/resume; shared validated semantic recovery. No database/core API/dependency changes.

**Spec:** docs/custom-core/direct-policy-reset-operation-design.md and original requirements/architecture/accounting/restore-authority-lifecycle-plan.

## Task 1: Real direct capture, preparation and completion

Files: create internal/web/service/client_policy_authority_direct_reset.go and client_policy_authority_direct_reset_test.go; modify client_policy_reset_runtime.go, client_policy_reset.go and client_policy_authority_reset_execution.go to extract the existing common semantic-row projection needed by direct ordinary retries.

Consumes: existing source-owned authority resolver, journal capture/progress APIs and private managed reset helper. Produces: direct typed key/capture/preparation decoders and application wrapper; original raw policy request is unchanged.

- [x] Step1: Write actual public single/direct boundary witnesses and inspect missing-capture functionalRED. Include2x warm316/later348/window32, repeated immutable retry and raw request identity. Expected: current implementation has no journal operation.
- [x] Step2: Implement source/handle-owned direct lifecycle flow and strict capture/preparation before SQL commit, completion after real apply/resume. Make fresh reset CreatedAt equal supplied effect time. Expected: all exact witnesses/timestamps/payloads pass, no RPC under SQL transaction.
- [x] Step3: Add actual core failure/cancel/foreign owner/source/pool and old acknowledged compatibility/partial historical overlap acceptance. Expected: failure retains preparation with no completion and stops business core; original rows/history remain exact.
- [x] Step4: Run focused real races, vet/diff and commit; task-done with direct execution selection. Expected: PASS with zero unexpected skips.

## Task 2: Cold and interrupted direct recovery

Files: add client_policy_authority_direct_reset_recovery_test.go; modify direct helper, client_policy_authority_reset_execution.go and client_policy_authority_reset_capture.go.

Consumes: Task1 strict envelope and current retained account/history. Produces: common semantic reset projection and bounded direct capture recovery, without metadata acknowledgement.

- [x] Step1: Actual deferred SQL commit failure after durable preparation then cold recovery. Expected: missing semantic reset boundary functionalRED before recovery integration.
- [x] Step2: Reuse validated projection, include direct namespaces in bounded recovery, retain pending/acknowledged version ordering. Expected: exact account/grant/capture/preparation unchanged; original rows/time restored; completion absent until actual application.
- [x] Step3: Cover later desired multiplier/reset, old raw-request row loss, rename/delete/email reuse, surrogate IDs, malformed/foreign/usage/cancel/SQL handle refusal and original direct subset overlap. Expected: no new boundary, rollback, recreated identity or invented ack.
- [x] Step4: Run affected direct/batch/history/calendar/entrypoint races on both backends, vet/diff and commit/task-done. Expected: PASS with backend provenance explicit.

## Task 3: Current regression and sole review

Files: evidence in docs/custom-core/issuance-journal-foundation.md and this plan/ledger; adapt the deliberately unowned polling fixture in client_policy_poll_test.go to its private direct-reset pipeline while asserting public refusal; require all28 direct/batch/calendar/lock regression parents explicitly in both jobs of .github/workflows/custom-core.yml.

- [x] Step1: Run broader affected races including polling and legacy lock ordering, explicit actual old writers, full make test-go, journal/service vet and diff. Expected: all pass, backend-only/optional fixtures described accurately.
- [x] Step2: Record final evidence and remaining parent scope; commit/task-done with real public witness. Expected: final source green.
- [ ] Step3: One fresh complete bounded diff review; re-grade every finding and rule all declined boundaries; one author RED/GREEN correction pass for Critical/Important and green suite, no second review. Expected: no unaddressed bounded blockers, parent remains active. Preserve workspace.

## Review Focus

Check raw-request compatibility and canonical UUID-set identity, including overlapping historical cohorts and later desired/reset edits. Verify capture/preparation ordering around actual SQL commit failure, core failure and completion; no SQL transaction across RPC or metadata-only acknowledgement. Check source/owner/database pinning, strict typed namespace recovery, usage/version/fingerprint bounds, deleted identities and numeric-ID/email reuse. Inspect the common batch projection and explicit fresh CreatedAt change for regressions outside direct reset. Check that the deliberately unowned 1001-client fixture retains its original lower-pipeline assertions while public admission still refuses, and that both CI jobs require actual-core passes. Distinguish existing bounded evidence from unverified public scale, independent process/database/journal replacement, automatic renewal and complete parent distribution/platform acceptance.
