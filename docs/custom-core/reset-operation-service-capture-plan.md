# Source-owned reset capture integration plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` inline; no implementer agents. One fresh final plan review and one TDD correction pass.

**Goal:** Keep the original manual/calendar reset selection and SQL metadata through restoration without recapturing later clients or guessing ambiguous legacy effects.

**Architecture:** Existing source-owned journal captures a typed original operation inside the pinned selection transaction. Startup and retry restore immutable SQL metadata from bounded service-namespaced captures. Separate exact preparation/completion activation follows this metadata stage.

**Tech Stack:** Existing Go/GORM/bbolt; no dependency changes. Existing clean Custom Xray acceptance binary and private SQLite/PostgreSQL fixtures.

**Spec:** [reset-operation-service-capture-design.md](reset-operation-service-capture-design.md); parent [restore-authority-lifecycle-plan.md](restore-authority-lifecycle-plan.md).

## Global constraints

- Keep Snell v4/v5/v6 and v5 QUIC, mieru, SSH, multiplier/directional limits and TCP/UDP Tunnel on the one Custom Xray business core.
- Preserve original request/scope/calendar/UUIDs/inbounds/creation time; a changed caller selection refuses.
- Capture/recovery never changes account, policy, window, grants, held capacity or immutable migration records.
- Lifecycle -> active owner mutex -> pinned serialized SQL -> journal; no RPC inside SQL. No journal recreation or partial activation.
- Capture <=64 MiB, chunks8 KiB, record16 KiB, bucket100000records/file256 MiB; page128headers and at most one payload in memory.
- Other journal namespaces remain supported; recognize only exact traffic-reset prefixed keys and validate their typed envelope.
- Pending mixed legacy effect ambiguity refuses; SQL Applied is not execution completion. Preserve remote-scope guards and all broader parent requirements.
- Preserve source/fixtures/logs/workspaces; no publication, push, merge, release or deployment in this plan. Use the user's accepted autonomous inline workflow.

## Review focus

- A request/calendar exists in protected storage but its SQL row is missing, stale or conflicting: exact original selection wins or refuses; no new selection.
- Original identities renamed/deleted and emails recreated: follow UUID, exclude replacements and later clients.
- Journal capture commits but SQL fails or caller is cancelled: original snapshot survives; metadata retry has no accounting effect.
- Ownership/source/database/process changes during capture/recovery: pin and refuse, never fall back to fresh authority.
- Missing legacy acknowledgement and no-op calendars: preserve original metadata, refuse ambiguous effects, never report storage capture as completion.

## Task 1: Public manual/calendar capture

**Files:** Create `internal/web/service/client_policy_authority_reset_capture.go` and `client_policy_authority_post_migration_capture_test.go`; modify selection transactions only in `client_traffic_reset_batch.go` and `client_traffic_reset_schedule.go`.

**Interfaces:** Produce `authorityResetCaptureSnapshot` (Schema, Operation, OriginalManagedIDs), deterministic request/calendar key helpers, strict typed decoder, and `runAuthorityResetCapture(ctx context.Context, requestKey, calendarKey string, operation *model.ClientTrafficResetBatch, selectOperation func(*gorm.DB) error, validateSelection func(model.ClientTrafficResetBatch) error) error`. Consume existing managed owner/manifest/database pinning and journal capture API. Keep public reset signatures unchanged.

- [ ] Adopt independently observed `TestManagedAuthorityPostMigrationAllRetryRetainsOriginalSelection` and run it on SQLite against unchanged product source. Expected RED: later client's7/11 becomes0/0. Add exact protected managed lifetime/window and capture comparisons after successful retry; existing flow echoes warm/next/stay.
- [ ] Implement source-owned capture wrapper, namespace/schema validation and owner pinning. Wrap existing manual selection, persist exact final SQL row before commit; original lookup precedes selection. Expected GREEN: later7/11 remains, original managed reset baseline316 and post-reset usage preserved, existing flow stays.
- [ ] Add live bulk/inbound/calendar original-selection cases and no-op calendar capture. Expected initial calendar RED: SQL loss changes original request/membership. Wrap calendar selection using exact scope/window key; preserve original UUID/request/time and keep normal eligibility behavior.
- [ ] Add source/selection immutable conflict and mixed pending replay refusal with unchanged original witness/legacy counters. Add required missing-journal refusal (do not delete original: rename/retain test-owned file and restore it) and cancelled capture. Run targeted SQLite/private PostgreSQL race cases with clean core, vet and diff. Expected all parents pass without core-dependent skips. Commit.

## Task 2: Cold metadata recovery and fault boundaries

**Files:** Extend capture implementation/tests; modify `client_policy_authority_evidence.go` recovery ordering; extend actual paired-snapshot fixture in `client_policy_authority_service_test.go` minimally for batch reset acceptance.

**Interfaces:** Consume Task1 envelope/key decoder and produce `recoverAuthorityResetCaptures(ctx context.Context, expected *gorm.DB, journal *policyauthority.Journal, source string) error`. Reuse recoverAuthorityResetBatchTx for immutable projection and current-owned serialized transaction. Generic opaque storage fixtures stay unrelated.

- [ ] Write `TestAuthorityResetCaptureRecoveryPreservesOriginalMetadataAndFundedState`: actual migrated journal, source-owned service capture, finite40-byte retained grant, erase disposable SQL row, recovery, exact original operation/capture/full account/grant. Initial RED: missing recovery API, then GREEN with pages and real source validation. Invoke after migration-history recovery before account recovery/compilation.
- [ ] Add original renamed/deleted/recreated identity, empty/no-op calendar, malformed service envelope, immutable SQL/calendar collision, cancelled context and replaced isolated pool cases. Expected typed refusal and no account/capture/destination changes; generic schema6 compatibility case still passes.
- [ ] Inject SQL failure after durable capture before commit through test-owned GORM callback on commit boundary; assert original witness retained, no SQL row, exact original retry/new-client exclusion. Inject projection failure then retry. Preserve failed logs and retained SQL/core/journal fixtures. Expected RED before implementation and exact GREEN afterward.
- [ ] Add actual paired old SQL/core snapshot restore with a public batch all-reset: original batch capture survives; create later7/11 client after restore, retry original, retain original lifetime/reset window; real new flow advances lifetime once. Preserve existing three dedicated SQLite paired cases. Mark actual paired route as SQLite, not PostgreSQL acceptance.
- [ ] Run targeted journal-owned recovery plus real public-flow selection on SQLite and private isolated PostgreSQL; source/pool failures remain isolated. Commit.

## Task 3: Final regression/evidence and review

**Files:** Update `docs/custom-core/issuance-journal-foundation.md`; retain logs/fixtures under existing evidence root and ignored plan workspace.

- [ ] Run appropriate complete reset/batch/calendar/authority history/recovery selections on SQLite and private isolated PostgreSQL with clean core; explicitly count forced SQLite paired cases. Run journal/service vet, diff and full make test-go. No unchanged native heavy rerun unless changes/failure/concern warrants it; prior native103 and core107 remain retained.
- [ ] Record exact real RED/GREEN values, test/package counts/skips/backend scope, retained authority/probe/core provenance and the pending mixed/preparation/completion limits. Commit.
- [ ] Run one fresh final plan reviewer over the whole committed plan range, then fix Critical/Important findings in one TDD pass with appropriate green suite; no second reviewer. Retain artifacts under user's preservation instruction.
- [ ] Continue exact prepared-effect/core-and-legacy-completion integration, owner-loss/node/legacy-migration/scale/platform work and eventual clean paired authorized feature push. Do not mark the parent or original project complete.
