# Direct managed reset operation implementation plan

> Execute inline with superpowers:executing-plans. No implementer agents; one fresh final review and one author TDD correction pass. The user's existing autonomous instruction covers ordinary engineering and local design/plan decisions.

**Goal:** Preserve original direct/single managed reset identity and exact quota boundary through SQL interruption/restoration, with durable preparation and actual core completion.

**Architecture:** Separate strict typed `policy-reset:` envelopes in the existing schema6 journal; existing lifecycle/pinned SQL and managed core apply/resume; shared validated semantic recovery. No database/core API/dependency changes.

**Spec:** docs/custom-core/direct-policy-reset-operation-design.md and original requirements/architecture/accounting/restore-authority-lifecycle-plan.

## Task 1: Real direct capture, preparation and completion

Files: create internal/web/service/client_policy_authority_direct_reset.go and client_policy_authority_direct_reset_test.go; modify client_policy_reset_runtime.go and client_policy_reset.go.

Consumes: existing source-owned authority resolver, journal capture/progress APIs and private managed reset helper. Produces: direct typed key/capture/preparation decoders and application wrapper; original raw policy request is unchanged.

- [ ] Step1: Write actual public single/direct boundary witnesses and inspect missing-capture functionalRED. Include2x warm316/later348/window32, repeated immutable retry and raw request identity. Expected: current implementation has no journal operation.
- [ ] Step2: Implement source/handle-owned direct lifecycle flow and strict capture/preparation before SQL commit, completion after real apply/resume. Make fresh reset CreatedAt equal supplied effect time. Expected: all exact witnesses/timestamps/payloads pass, no RPC under SQL transaction.
- [ ] Step3: Add actual core failure/cancel/foreign owner/source/pool and old acknowledged compatibility/partial historical overlap acceptance. Expected: failure retains preparation with no completion and stops business core; original rows/history remain exact.
- [ ] Step4: Run focused real races, vet/diff and commit; task-done with direct execution selection. Expected: PASS with zero unexpected skips.

## Task 2: Cold and interrupted direct recovery

Files: add client_policy_authority_direct_reset_recovery_test.go; modify direct helper, client_policy_authority_reset_execution.go and client_policy_authority_reset_capture.go.

Consumes: Task1 strict envelope and current retained account/history. Produces: common semantic reset projection and bounded direct capture recovery, without metadata acknowledgement.

- [ ] Step1: Actual deferred SQL commit failure after durable preparation then cold recovery. Expected: missing semantic reset boundary functionalRED before recovery integration.
- [ ] Step2: Reuse validated projection, include direct namespaces in bounded recovery, retain pending/acknowledged version ordering. Expected: exact account/grant/capture/preparation unchanged; original rows/time restored; completion absent until actual application.
- [ ] Step3: Cover later desired multiplier/reset, old raw-request row loss, rename/delete/email reuse, surrogate IDs, malformed/foreign/usage/cancel/SQL handle refusal and original direct subset overlap. Expected: no new boundary, rollback, recreated identity or invented ack.
- [ ] Step4: Run affected direct/batch/history/calendar/entrypoint races on both backends, vet/diff and commit/task-done. Expected: PASS with backend provenance explicit.

## Task 3: Current regression and sole review

Files: evidence in docs/custom-core/issuance-journal-foundation.md and this plan/ledger.

- [ ] Step1: Run broader affected races including polling and legacy lock ordering, explicit actual old writers, full make test-go, journal/service vet and diff. Expected: all pass, backend-only/optional fixtures described accurately.
- [ ] Step2: Record final evidence and remaining parent scope; commit/task-done with real public witness. Expected: final source green.
- [ ] Step3: One fresh complete bounded diff review; re-grade every finding and rule all declined boundaries; one author RED/GREEN correction pass for Critical/Important and green suite, no second review. Expected: no unaddressed bounded blockers, parent remains active. Preserve workspace.
