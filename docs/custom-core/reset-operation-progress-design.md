# Protected reset preparation and completion

This continues restore-authority Task 4 for the same Snell, mieru, SSH, multiplier/rate and TCP/UDP Tunnel clients. A captured selection proves which original identities belong to an operation. It does not prove a reset boundary was prepared or applied. The current SQL `ClientTrafficResetBatch.Applied` is written during preparation, before `managedAuthority.ApplyPolicies` and before legacy runtime effects. It cannot serve as a durable completion acknowledgement.

The next bounded storage interface records two distinct immutable witnesses. Service activation follows a separate integration design covering exact prepared SQL effects, core acknowledgement, legacy effects and source-owned recovery. No ordinary reset caller changes in this storage plan. This boundary prevents an incomplete witness from being mistaken for permission to reset a client's window again.

The binding user instruction to make ordinary engineering decisions from source and experiments authorizes the established inline workflow. The existing journal/source/identity and bounded snapshot contract provide the architectural base. No new dependency, database owner, remote action or deployment is introduced.

## Decisions

Use a dedicated `reset-operation-progress` bucket in the existing journal. Preparation binds to the SHA-256 digest of the immutable original capture. Completion binds to the SHA-256 digest of that preparation. Neither witness changes an account, policy, grant, held capacity, quota window or migration record.

The alternative of encoding preparation as another schema-5 capture request cannot safely introduce acknowledgement semantics: the schema-5 writer does not understand their dependency or completion meaning. The alternative of placing progress only in SQL loses it during the restoration this feature must survive. A second file introduces another ownership and atomic publication problem. Use an atomic schema-6 writer fence on the first preparation instead.

## Interfaces

```go
type ResetOperationPreparation struct {
    Identity Identity
    SourceID string
    RequestID string
    CaptureDigest string
    Snapshot string
}
type ResetOperationCompletion struct {
    Identity Identity
    SourceID string
    RequestID string
    PreparationDigest string
}
func (j *Journal) PrepareResetOperation(request ResetOperationPreparation) error
func (j *Journal) CompleteResetOperation(request ResetOperationCompletion) error
func (j *Journal) LookupResetPreparation(requestID string) (ResetOperationPreparation, error)
func (j *Journal) LookupResetCompletion(requestID string) (ResetOperationCompletion, error)
```

Preparation requires an existing capture with the exact same request, identity and migration source, and the exact lowercase 64-character capture digest. Its snapshot is an exact UTF-8 JSON object, including valid arbitrary-sized JSON numeric literals. The maximum is 64 MiB, chunks are 8 KiB, encoded records remain within the existing 16 KiB bound. The digest is SHA-256 over the exact snapshot bytes, including whitespace. An existing preparation accepts only an identical retry. A different snapshot or dependency returns `ErrRequest`; a missing original returns `ErrNotFound`; a wrong identity/source returns `ErrIdentity`.

Completion requires that exact prepared request and preparation digest. It is a small immutable JSON record; it has no caller-controlled timestamp or acknowledgement flags that could drift on retry. Completion without preparation returns `ErrNotFound`. A wrong dependency returns `ErrRequest`. Exact retries have no second effect. These storage calls do not determine whether external execution completed: the service must supply a witness only after its separate functional contract succeeds.

Lookups reject invalid request keys with `ErrRequest`, missing witnesses with `ErrNotFound`, and damaged storage with `ErrJournal`. Closed journal access returns `ErrJournal`. Existing capture/calendar lookup and header-only pages keep their interfaces and exact behavior on schema 6. Recovery can traverse capture summaries and load at most one preparation payload at a time; it must not collect 128 maximum-sized payloads in memory.

## Layout and bounds

Preparation header `h/<requestID>` contains the existing bounded snapshot-header fields plus `CaptureDigest`; its calendar key is empty. Chunks use the existing deterministic `c/<hex-requestID>/<eight-hex-index>` shape in the progress bucket. Completion `d/<requestID>` contains `ResetOperationCompletion`. There are no progress calendar indexes. Original calendar uniqueness remains in the capture bucket.

Reuse the streaming chunk reader with an explicit bucket and reuse snapshot hashing/UTF-8/JSON-object validation. Retain the established UTF-8 cross-chunk handling and `Decoder.UseNumber`. Validate the expected exact record count to detect orphan chunks, unknown keys and extra completion records without a map of all chunk payloads. Every preparation header must reference a real capture digest; every completion must reference a real preparation digest. Corruption, missing/truncated/altered chunks, source conflicts and completion dependency damage refuse opening without modifying the retained file.

The existing 256 MiB prospective bbolt allocator bound applies to the whole file. The progress bucket has at most 100,000 records. Exhaustion rejects the new operation/witness while preserving every previously committed capture, preparation, completion, account and funded grant. Completion can fail for exhausted storage; callers must preserve the prepared witness and retry, rather than recapture its boundary or claim completion.

## Compatibility and transaction boundaries

Schema 4 requires both capture and progress buckets to be absent. Schema 5 requires the existing complete capture bucket and no progress bucket. Schema 6 requires both a complete capture bucket and a nonempty complete progress bucket with at least one preparation. Creation remains schema 4. Capturing a later operation on schema 6 preserves schema 6; it must never downgrade the writer fence.

First preparation writes its complete header/chunks and upgrades metadata from 5 to 6 in the same journal transaction. Invalid requests or precommit failure preserve schema 5 and do not leave an empty progress bucket. Completion adds only its exact witness in the same transaction. An uncertain reply is resolved by reopening retained storage and querying the original request. Existing synchronous commit and exclusive file ownership remain in force; no new fsync or whole-authority cloning guarantee is implied.

Build the actual original schema-5 implementation from the committed pre-change source, retaining its source/module/license closure. The same executable must open unprepared/preparation-rejected schema-5 fixtures and reject committed schema-6 fixtures before writing. The earlier original schema-4 probe remains preserved and must also reject schema 6. Acceptance supplies `RESET_OPERATION_SCHEMA5_WRITER_PROBE` for the new original-source executable, keeps `RESET_OPERATION_OLD_WRITER_PROBE` for schema 4, and retains closed copies under `RESET_OPERATION_PROGRESS_FIXTURE_DIR`. Probe-dependent tests explicitly skip when their required executable is absent; final acceptance must supply both. These are journal writer compatibility checks, not complete old panel distributions or proof of historical schema-4 policy-evidence rollback safety.

Existing panel compatibility acceptance must also open a real manifest-owned migrated schema-6 journal with a finite retained grant, restore its original migration-era SQL operation/time projection and compare the exact account/grant/witnesses on both backends. Its opaque storage-only progress fixture does not prove prepared effect interpretation or post-migration service activation.

## Service integration requirements retained for the next stage

A protected preparation must eventually carry the original operation, eligible managed identities, exact first reset rows and effective times, credential-free intended policies, and a validated plan for legacy SQL/runtime effects. Persist it before an uncertain SQL commit can erase that intent. Preserve the original capture even when no clients remain eligible. Complete only after all required owned core and legacy effects have acknowledgement; SQL `Applied` alone is insufficient. Missing SQL projections replay the exact witness and never recapture usage from a later receipt. Partial application, deleted/renamed clients, recreated emails, new client exclusion, cancellation, pool replacement and SQL/core response loss require functional SQLite/PostgreSQL and real-flow acceptance before activation.

The exact legacy preparation/replay/acknowledgement contract is not established by this storage interface. Mixed operations must not be declared fully recoverable from managed per-client evidence. Whole panel-owner loss, global coordinated nodes, legacy business-path migration, scale/platform/device acceptance and a clean paired distribution remain outstanding in the parent plan.
