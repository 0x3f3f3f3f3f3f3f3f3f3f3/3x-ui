# Managed renewal operation implementation plan

> Use superpowers:executing-plans inline; no implementer agents. One fresh final review and one author RED/GREEN correction pass. Existing autonomous engineering authorization applies.

**Goal:** Preserve complete original managed renewal expiry/count/window effects through interruption and SQL restoration.

**Architecture:** Strict typed renewal captures/preparations in the existing schema6 journal; existing lifecycle/source/pinned SQL and actual core application; canonical derived expiry mirrors. No database/journal schema or core API change.

**Tech Stack:** Existing Go1.27.1, GORM SQLite/PostgreSQL, Custom Xray private control API and policy authority journal.

**Spec:** docs/custom-core/renewal-policy-operation-design.md, original requirements/accounting/restore-authority-lifecycle-plan.

## Global Constraints

- Preserve native Snell/mieru/SSH, shared multiplier/directional limits and TCP/UDP Tunnel priority.
- Preserve raw/billed/fraction lifetime, manual disable, current credentials and later legitimate desired edits.
- Max1000 clients per renewal cohort; bounded1000-ID SQL queries and128-header recovery pages.
- Preparation is durable before SQL commit; completion follows actual core execution. No SQL transaction spans RPC or grant allocation during metadata recovery.
- Preserve every source/evidence/fixture/workspace; normal stage pushes only to the authorized fork feature/custom-xray-unified-policy, never force/default merge/release/deploy.

## Review Focus

- Delayed unprepared intent versus an immutable prepared boundary, overlapping new due cohorts, and exhausted prepaid caps must not spend the original renewal twice.
- Business SQL tuple restoration versus later legitimate expiry/rule/multiplier edits must preserve the intended current setting; contradictory mixtures refuse.
- Deleted/reused UUID/email/numeric ID and changed attachments must never transfer old effects to another identity or overwrite credentials.
- Harmless cancellation/admission failure must retain healthy traffic, while uncertain applied/completion failure remains closed.
- Bounded queries and accurate restricted/backend/native/scale/distribution provenance must remain explicit; parent scope stays required.

## Task 1: Source-owned complete renewal execution

Files: create internal/web/service/client_policy_authority_renewal.go and client_policy_authority_renewal_test.go; modify client_policy_renewal.go for guarded capture-first orchestration and canonical mirror stamps; adapt restricted renewal fixtures only if guarded public admission requires it.

Consumes: authorityResetExecutionStateLocked, existing journal capture/preparation/completion APIs, applyLocalClientPolicyResetLocked and catchUpClientRenewal. Produces: strict typed trigger/capture/preparation decoding; source-owned renewal capture/apply helper and exact effect projection usable by ordinary retry.

Interfaces: `authorityRenewalTrigger` contains ClientID, ExpiryTime, ResetCount, Reset, ResetDay, ResetWeekday and ResetMax. `authorityRenewalCaptureSnapshot` contains Schema, At, Zone and sorted Triggers. `authorityRenewalEffect` contains ClientID and before/after expiry/count/update stamp/desired version/fingerprint. `authorityRenewalPreparationSnapshot` contains Schema, RequestID, ResetAt, Effects, OmittedIDs and normalized Resets. `authorityRenewalClientRequest(source,zone string,trigger authorityRenewalTrigger) string` derives the per-client semantic request. `applyAuthorityClientRenewalBatch(ctx context.Context,process *xray.Process,ids []string,now int64,location *time.Location) error` owns each lifecycle sequence. `resumeAuthorityClientRenewals(ctx context.Context,process *xray.Process) error` resumes source-owned incomplete cohorts before new due selection. Retain the existing lower preparation signature for restricted fixtures; factor a request-map variant rather than changing historical random requests.

- [x] Step1: Add TestManagedAuthorityRenewalRetainsCompleteOriginalWitnessAndWindow with real owned polling and2x warm316/next332/stay348, original expiry/count/request/witness and repeated immutable retry. Expected RED: no renewal operation witness on current source.
- [x] Step2: Implement bounded capture-first trigger identity, first actual preparation time, exact expiry/count/version/fingerprint/reset snapshot before SQL commit and completion after actual application. Resume original incomplete captures before fresh selection. Expected GREEN: exact original effects/window, no second allowance.
- [x] Step3: Add actual core/completion failure and queued cancel/unknown/source/owner/handle refusal, cap-exhausted expiry-only and unrelated/later desired edits. Expected: true failures retain preparation/no completion and stop execution; harmless rejection preserves healthy traffic; expired catch-up does not open a window.
- [x] Step4: Add ordinary/password/empty-Tunnel current canonical mirror preservation and original capture-only retry/changed-trigger cases. Run focused actual races, vet/diff, commit and task-done with TestManagedAuthorityRenewal execution selection. Expected PASS/no unexpected skip; restricted provenance explicit.

## Task 2: Exact cold and interrupted renewal recovery

Files: add internal/web/service/client_policy_authority_renewal_recovery.go and client_policy_authority_renewal_recovery_test.go; modify client_policy_authority_reset_capture.go typed dispatcher and Task1 projection helper as needed.

Consumes: Task1 strict original triggers/effects and retained account history. Produces: recoverAuthorityPreparedRenewalTx(tx,journal,source,capture) error with no metadata acknowledgement or funding mutation.

- [x] Step1: Actual deferred SQL commit failure after preparation and old SQL expiry/count/reset-row restoration after later payload. Expected RED: original effects/window missing; retained funded40 account/grant unchanged before projection.
- [x] Step2: Integrate bounded strict namespace recovery before compilation/due selection; restore justified original before/after tuples/count/semantic rows and preserve later desired fields. Expected GREEN: lifetime348/base316/window32, exact original expiry/count; no new window or metadata completion.
- [x] Step3: Cover expiry-only cap exhaustion, later acknowledged renewal/manual reset and desired expiry/rule/multiplier, rename/delete/reused email and numeric ID, changed mirrors, malformed/membership/source/usage/tuple contradictions, cancellation and stale handle. Expected: correct identity/order or fail-closed refusal; full account/funded40 grant retained during metadata-only recovery.
- [x] Step4: Run affected renewal/direct/batch/history/calendar/lifecycle races on both real backends, vet/diff, commit and task-done with recovery selection. Expected PASS with backend provenance explicit.

## Task 3: Regression, sole review and stage push

Files: evidence in issuance-journal-foundation.md, this plan/ledger and required actual-core CI tests in .github/workflows/custom-core.yml.

- [x] Step1: Require every new owned renewal parent in both CI jobs; run broad affected races, full make test-go with actual core/original writer probes, journal/service vet/diff. Expected: all mandatory checks pass; every failure/skip and restricted fixture is reported honestly.
- [x] Step2: Record evidence/remaining parent scope, commit/task-done with real owned renewal witness. Expected: final product source green; no whole-parent completion claim.
- [x] Step3: One fresh entire bounded diff review; re-grade findings and rule every declined boundary; one author Critical/Important RED/GREEN correction pass plus green suite, no second review. Expected: no bounded blockers; workspace preserved.
- [x] Step4: Normal authorized feature push and independent remote SHA verification after milestone checks. Expected: remote/local SHA identical, no force/default merge/release/deploy; continue remaining parent work.
