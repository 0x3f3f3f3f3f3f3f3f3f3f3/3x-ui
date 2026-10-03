# Protected reset preparation and completion implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` inline; do not dispatch separate implementers.

**Goal:** Preserve exact preparation/completion witnesses and fence schema-5 writers without changing accounting or enabling service callers.

**Architecture:** A dedicated progress bucket binds preparation to an immutable capture digest and completion to an immutable preparation digest. First preparation atomically upgrades storage to schema 6. Reuse bounded streaming snapshot validation and keep original capture interfaces compatible.

**Tech Stack:** Go, existing bbolt 1.5.0, UTF-8/JSON validation and SHA-256; no new dependencies.

**Spec:** [reset-operation-progress-design.md](reset-operation-progress-design.md), [requirements.md](requirements.md), [restore-authority-lifecycle-plan.md](restore-authority-lifecycle-plan.md).

## Global constraints

- Preserve one Custom Xray business core and Snell/mieru/SSH/shared billing/rates/TCP/UDP Tunnel behavior.
- Preserve identity/source, migration records/digest, complete accounts, policies, windows, grants and held capacity.
- Snapshot maximum 64 MiB, chunk maximum 8 KiB, JSON record maximum 16 KiB, journal file maximum 256 MiB, progress bucket maximum 100,000 records; never reduce production limits for a test.
- Preserve exact capture/calendar retry and bounded header pages on schema 6; never downgrade metadata on later capture.
- No service caller activation, missing journal recreation, history trimming, dependency change or external publication.
- Apply the user's autonomous engineering instruction and accepted inline execution.

## Review focus

- Completion must not exist without its exact original preparation and capture dependency.
- A later capture must retain schema 6 so an old writer cannot ignore progress.
- Rejected/failed/uncertain preparation or completion must retain the correct original transaction boundary and all accounting.
- Valid JSON large numbers and UTF-8 across chunk boundaries must remain reopenable; unknown/orphan/corrupt records must refuse opening.
- Original schema-5 and schema-4 executables must reject actual closed schema-6 fixtures before any write; no full panel rollback claim.

## Task 1: Immutable dependency-bound progress

**Files:** Create `internal/policyauthority/reset_operation_progress.go` and `internal/policyauthority/reset_operation_progress_test.go`; modify `internal/policyauthority/reset_operation.go` and `internal/policyauthority/validate.go` only for the shared reader/validator and schema rules.

**Interfaces:** Consume the capture API/header/chunk helpers, `Journal.update`, `metadata`, `put/get`, existing source/identity ownership and real storage bounds. Produce the four methods and two request types with the exact signatures in the spec. Keep all existing capture interfaces unchanged.

- [ ] Write `TestResetOperationProgressRetainsExactDependenciesAcrossReopen`: funded source-bound journal, original capture and >16 KiB preparation with Unicode and valid large JSON numbers; completion; later capture; close/reopen and exact lookup/retry. Compare whole original account/grant, capture, calendar lookup and sorted bounded pages. Expected initial RED: missing API.
- [ ] Implement the smallest complete preparation/completion transactions, immutable dependencies and schema-6 reader checks. Rerun the initial parent; expected GREEN with exact values and no accounting mutation.
- [ ] Add `TestResetOperationProgressRejectsConflictingDependencies`: absent capture/preparation, wrong source/identity/key/digest, changed payload, invalid UTF-8/JSON/object, oversized preparation and changed completion. Assert typed errors and original witnesses/account/grant preservation.
- [ ] Add `TestResetOperationProgressCommitFailurePreservesPreviousState`: real read-only file descriptor through existing `openJournal`, with first preparation and later completion failures. Compare retained file bytes/schema/witness absence; reopen and retry original valid calls. Add an own subprocess exiting after durable preparation/completion before replying; retain exact reopened outcomes.
- [ ] Add `TestResetOperationProgressRejectsCorruption`: missing/altered/orphan chunk, unknown entry, invalid UTF-8 with matching digest, mismatched capture dependency, missing preparation with completion, altered completion dependency, missing/empty progress bucket and forbidden progress bucket under schema 4/5. Reopen must refuse with unchanged retained fixture.
- [ ] Exercise actual progress-record exhaustion and actual bbolt file allocation exhaustion using valid retained original witnesses and the established test-only pressure bucket method. Expected: typed bound error, no new witness and exact original capture/preparation/completion/account/grant.
- [ ] Run full journal race with 25-minute timeout, existing old-writer probe enabled, journal/service vet and diff check. Expected: all assertion parents pass; subprocess helper entries identified; no probe skips. Commit this bounded interface before compatibility acceptance.

## Task 2: Actual schema-5 old writer and panel compatibility

**Files:** Extend `internal/policyauthority/reset_operation_progress_test.go`; preserve original-source build script/closure under this plan's ignored workspace. Update `docs/custom-core/issuance-journal-foundation.md` with exact evidence and limits.

**Interfaces:** Consume Task1 storage and the pre-change schema-5 source checkpoint. Build ordinary `Journal.Open/Close` using its unchanged original package/module/license and selected dependency closure. Preserve the earlier schema-4 probe and all fixtures.

- [ ] Build/hash the actual schema-5 probe. It opens closed schema-5 captures before preparation and after rejected/read-only-failed preparation; it rejects copied committed schema-6 preparation and completion fixtures before writing. The actual schema-4 probe also rejects the same schema-6 copies. Compare fixture bytes and reopened exact accounting/witnesses.
- [ ] Run full journal race/static gates, appropriate authority migration/history/reset selections on SQLite and private isolated PostgreSQL, and `make test-go` with current clean core/probes. Identify PostgreSQL-only tests and explicitly forced SQLite paired imports; do not equate skips with acceptance.
- [ ] Commit final bounded evidence. Perform one fresh final plan review and one correction pass. Retain all source/probe/log/fixture artifacts. Continue the separately designed service capture/prepared/completed integration; do not claim the parent restore task or whole project complete.
