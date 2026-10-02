# Protected reset operation capture implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` inline. This continues restore-authority Task 4; do not dispatch separate implementers.

**Goal:** Store exact original reset operation snapshots durably without mutating accounting, and prevent an old schema-4 writer from ignoring them.

**Architecture:** Add bounded, immutable capture records to the existing journal with an atomic schema-5 fence. Existing schema-4 journals remain unchanged until first successful capture. Service activation of this storage interface is a subsequent stage.

**Tech stack:** Go, existing bbolt journal and SHA-256; no new dependencies.

**Spec:** [reset-operation-capture-design.md](reset-operation-capture-design.md), [requirements.md](requirements.md), [restore-authority-lifecycle-plan.md](restore-authority-lifecycle-plan.md).

## Global constraints

- Preserve one Custom Xray business core and Snell/mieru/SSH/shared billing/rates/TCP/UDP Tunnel behavior.
- Preserve authority identity, source, migration digest, accounts, policies, quota windows, grants and held capacity exactly.
- Snapshot maximum 64 MiB; chunk maximum 8 KiB; existing JSON record bound 16 KiB; existing file bound 256 MiB and bucket bound 100,000 records.
- Header pages contain 1 through 128 summaries and omit snapshot bytes.
- Never rewrite migration records, recreate an unavailable journal, trim history or enable ordinary reset callers in this storage stage.
- Follow the user's autonomous engineering instruction and existing inline execution; no additional design approval is required.

## Review focus

- A rejected or rolled-back first capture must not partially modify a schema-4 journal; an uncertain commit must reopen as either the original state or the exact complete capture.
- An older writer must reject the real new fixture before writing, rather than relying on ignored JSON fields.
- Calendar keys must identify exactly one original request and survive reopening.
- Missing, duplicated, altered or orphaned chunks must refuse opening without changing accounting.
- Record/file exhaustion must preserve existing captures and all held capacity.

## Task 1: Durable immutable captured snapshots

**Files:** Create `internal/policyauthority/reset_operation.go` and `internal/policyauthority/reset_operation_test.go`; modify `internal/policyauthority/validate.go`. Keep the existing create path and base bucket list unchanged.

**Interfaces:** Consume `Journal.update`, `metadata`, `put`, `get`, `key`, `privatePath` and existing bbolt ownership. Produce `ResetOperationCapture`, `ResetOperationSummary`, `CaptureResetOperation`, `LookupResetOperation`, `LookupResetCalendar` and `ResetOperationPage` with the exact signatures in the spec.

- [ ] Write `TestResetOperationCaptureRetainsSelectionAcrossReopen`: create a source-bound journal with a funded account, capture a JSON selection larger than 16 KiB, close/reopen, retrieve by request/calendar and retry exactly. Assert original bytes and the entire account/grant are unchanged. Add request/snapshot/source/calendar conflicts, absent lookup and sorted bounded header pages.
- [ ] Run `go test -count=1 -run '^TestResetOperationCapture' ./internal/policyauthority` before production code. Expected: capture API missing. Implement the typed requests and minimal chunked transaction, schema-5 validator and lookup/page APIs, then rerun. Expected: original selection and accounting remain exact.
- [ ] Add `TestResetOperationCaptureFailureLeavesOldJournalUnchanged` for invalid UTF-8/JSON/object shape, invalid source/identity/calendar key, oversized snapshots and a real commit failure using the existing openJournal seam with a read-only file descriptor. Compare schema and accounts, and compare file bytes for validation/read-only-write rejection; retry a valid capture after the injected failure. Expected: errors, unchanged schema-4 state, then exact successful capture.
- [ ] Add `TestResetOperationCaptureRejectsCorruptChunks` with deleted/altered chunks, missing index, duplicate calendar association, orphaned chunks and unknown entry keys. Close before mutation; reopen must return `ErrJournal` and preserve the fixture. Add bounded record exhaustion without reducing the production limits.
- [ ] Run `go test -race -count=1 ./internal/policyauthority` and `go vet ./internal/policyauthority`. Expected: all parents pass with no skip; all existing migration/grant/change validation remains green. Commit this independently testable storage interface and its evidence.

## Task 2: Actual old-writer rejection

**Files:** Extend `internal/policyauthority/reset_operation_test.go`; add a retained local probe build script under this plan's ignored workspace. Document evidence in `docs/custom-core/issuance-journal-foundation.md`.

**Interfaces:** Consume Task1's source-bound capture API and schema fence. Build the pre-change `internal/policyauthority` implementation from Task1 BASE, retaining source/module/license closure and an ordinary `Journal.Open` probe. No Git checkout/history rewriting or management keys.

- [ ] Build and hash the original-source probe. Before first capture, it must open the closed schema-4 fixture successfully. After first capture, the same unchanged executable must reject opening a copied closed fixture with `ErrJournal`. It must leave the captured copy and all accounting unchanged.
- [ ] Verify a rejected/read-only-write-failed first capture still permits old schema-4 opening, while a committed schema-5 capture never does. Retain and reopen an ambiguous commit reply; accept only the exact complete capture or the original uncaptured state. Test new opening/retry against both successful and failed-transition fixtures. Expected: exact transaction boundary, no partial fence.
- [ ] Run the full journal race/vet gate and the appropriate panel authority migration/recovery selection against the current clean core on SQLite and isolated PostgreSQL. Expected: storage remains compatible while callers are unchanged; backend-only skips are explicitly identified.
- [ ] Commit the verified probe/evidence update. Conduct the plan's one fresh final review, then continue designing service capture and prepared/completed acknowledgement before enabling callers. Do not claim full post-migration operation recovery.
