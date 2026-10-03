# Durable managed reset execution implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` inline; no implementer agents. One fresh final review and one TDD correction pass.

**Goal:** Connect original source-owned managed/no-op reset preparation and actual core completion to ordinary reset execution and cold recovery.

**Architecture:** Existing schema6 journal holds strict semantic preparation before SQL commit and a completion only after successful core apply/resume. Existing lifecycle serialization, pinned SQL transactions and protected account history retain exact boundaries through interruptions.

**Tech Stack:** Existing Go/GORM/bbolt and real Custom Xray; no dependency changes.

**Spec:** `docs/custom-core/reset-operation-managed-execution-design.md` and the existing requirements/architecture/accounting parent documents.

## Global constraints

- One Custom Xray, Snell v4/v5/v6 including v5 QUIC, mieru and SSH; common multiplier/directional limits and TCP/UDP Tunnel priority retained.
- Preserve source/business/evidence/valid archives/retained fixtures and ignored plan workspace.
- Existing cardinality100000, snapshot64MiB, record16KiB, bounded header pages128 and journal256MiB limits remain unchanged.
- No capture/SQL-only completion; no SQL transaction over RPC; no fake retained owner or email identity substitution.
- Legacy/inbound/remote effects acquire no false completion; remaining original project scope stays active.
- Normal feature push already authorized; no force, default merge, release or deployment in this local plan.

## Review focus

- Later desired policy/reset changes between interruption and retry retain newer configuration/windows.
- Missing/pending operation after SQL rollback preserves exact prepared boundary and active/no-op membership.
- Source/database replacement and stop/close must not permit completion against another owner.
- Deleted/renamed UUIDs and SQL surrogate-ID collisions preserve semantic identities and original result.
- Mixed legacy/calendar inbound/remote effects must never be falsely acknowledged as completed.

### Task 1: Connect real managed preparation and core completion

**Files:** Create `internal/web/service/client_policy_authority_reset_execution.go` and `internal/web/service/client_policy_authority_reset_execution_test.go`; modify `client_traffic_reset_batch.go` and `client_policy_reset_runtime.go` in the same directory.

**Interfaces:** Consume journal preparation/completion APIs, original `authorityResetCaptureSnapshot`, managed checkpoint/apply/resume and pinned serialized SQL. Produce strict managed preparation envelope and private locked application helper. Preserve existing public signatures.

- [ ] Step1: Write real public all and calendar no-op witness tests, with exact account/billing and immutable retries. Run `go test -race -count=1 -run '^TestManagedAuthorityResetExecution' ./internal/web/service`. Expected: functional missing preparation/completion RED.
- [ ] Step2: Implement lifecycle-pinned managed application, typed preparation before SQL commit, completion only after successful core application. Run the same command. Expected: all witness/live/no-op scenarios PASS, zero skips.
- [ ] Step3: Add real core-application failure and mixed-effect refusal assertions; source/pool/cancel invariants. Run same selection. Expected: retained preparation, absent completion, stopped core on failed managed application; no false mixed completion.
- [ ] Step4: Run journal/service vet and diff checks and commit. Run task-done with the same real witness selection. Expected: PASS.

### Task 2: Recover exact prepared effects through interruption

**Files:** Modify execution helper, `client_policy_authority_reset_capture.go`, `client_policy_authority_evidence.go`; create `client_policy_authority_reset_execution_recovery_test.go` for focused interruption/recovery tests.

**Interfaces:** Consume Task1 payload and current account history. Produce preparation-aware source-owned retry/cold projection with semantic reset identity; preserve current history APIs.

- [ ] Step1: Write actual deferred SQL commit-failure test after durable preparation and old SQL restore/cold metadata recovery. Run targeted new test. Expected: original boundary/membership missing or recomputed functional RED.
- [ ] Step2: Restore exact semantic rows and operation progress before core activation/application; validate malformed/conflicting/foreign witnesses. Run targeted tests. Expected: original boundary/result retained, finite funded account/grant unchanged by metadata recovery.
- [ ] Step3: Add later desired configuration and later acknowledged reset, no-op eligibility changes, renamed/recreated/deleted UUID, surrogate ID collision and source/database/cancellation regressions. Expected: no rollback/rebinding/recreation/false completion.
- [ ] Step4: Run affected execution/capture/history/calendar cases on SQLite and isolated PostgreSQL, then vet/diff and commit. Task-done runs focused exact recovery tests. Expected: PASS with explicit backend provenance and zero unexpected skips.

### Task 3: Final regression, evidence and sole review

**Files:** Update `docs/custom-core/issuance-journal-foundation.md`, this plan and plan ledger; correct the deliberately restricted lower pipeline fixture in `internal/web/service/client_policy_poll_test.go` and factor the unchanged private calendar resumption loop if final gates expose admission assumptions.

- [ ] Step1: Run affected reset/authority/polling/import race coverage on both backends, actual writer probe compatibility, full `make test-go`, journal/service vet and diff. Expected: all pass; deliberately forced SQLite/optional cases labeled accurately.
- [ ] Step2: Record actual results, failed fixture logs and unchanged native/core source provenance; commit evidence and task-done cheap final public witness gate. Expected: final source green.
- [ ] Step3: Dispatch one fresh review with the complete plan diff, design, ledger rulings and focus above. Re-grade findings by user effect. One author TDD fix pass for Critical/Important, green suite; no second review. Preserve workspace. Expected: no unaddressed blocking findings within this bounded plan; parent remains active.
