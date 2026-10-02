# Protected reset operation capture

This is the next bounded part of restore-authority Task 4. It stores an exact original operation selection after migration. It does not yet acknowledge completed operations or make captured operations available to ordinary reset callers. Snell, mieru, SSH, shared billing/rates and TCP/UDP Tunnel remain the business requirements; this work protects their reset boundaries during restoration.

The existing immutable migration records protect operations present at migration. Later per-client policy evidence protects individual reset boundaries, but cannot reconstruct a batch's original complete selection, calendar request or managed acknowledgement. Missing SQL operation rows must not be replaced by a new selection containing clients created later.

The user's requirement to make and record ordinary engineering decisions authorizes this continuation in the established inline workflow. Capturing operation metadata introduces a storage interface and a writer compatibility requirement; its design and implementation plan must precede code.

## Decision and alternatives

Use a dedicated capture bucket in the existing issuance journal, with an atomic storage-schema fence and bounded chunks. This retains the authority identity/source and the journal's exclusive file ownership without another database or recovery owner.

A separate operation file would need independent ownership, atomic publication and a fence in the main journal; an older writer otherwise ignores it. Per-client reset evidence alone cannot prove original batch membership or completion. Appending to the migration bucket would alter the immutable migration digest. Those approaches are not used for this stage.

## Capture contract

`ResetOperationCapture` contains `Identity` of type `policyauthority.Identity`, and `SourceID`, `RequestID`, `CalendarKey` and `Snapshot` of type `string`. The snapshot is a UTF-8 JSON object. The future service producer must serialize only `ClientTrafficResetBatch` metadata; credentials and client authentication settings are excluded. `CalendarKey` is empty for manual operations or a lowercase 64-character SHA-256 key for a calendar operation. The future service derives it from the exact scope and scheduled time. Identity must match the journal, and source must equal its nonempty migration source. Identity/source mismatches return `ErrIdentity`; invalid keys, UTF-8, JSON or size return `ErrRequest`; damaged stored data returns `ErrJournal`.

`Journal.CaptureResetOperation(request ResetOperationCapture) error` records that exact value once. An exact retry succeeds after a close/reopen; changes to the source, snapshot, request or calendar association fail. A different request cannot claim the same calendar key. `Journal.LookupResetOperation(requestID string) (ResetOperationCapture, error)` and `Journal.LookupResetCalendar(calendarKey string) (ResetOperationCapture, error)` return the original snapshot. Missing records return the existing `ErrNotFound`.

`Journal.ResetOperationPage(after string, limit int) ([]ResetOperationSummary, error)` pages headers in request-ID order. `ResetOperationSummary` contains `RequestID`, `SourceID`, `CalendarKey` and `Digest` strings, and `SnapshotBytes` of type `uint64`. Limits are 1 through 128; summaries omit snapshot bytes. A caller loads and processes one bounded snapshot at a time instead of multiplying its largest operation by a page size.

Snapshots are limited to 64 MiB and split into 8 KiB chunks, each below the existing 16 KiB JSON record bound after base64 encoding. This permits typical 100,000-member selections without placing the whole JSON in one journal record. It does not establish the project's scale acceptance. The existing 256 MiB journal file bound and 100,000-record bucket bound remain enforced. Exhaustion refuses a new capture before any business policy change; it does not discard original records or create another journal.

An internal header binds authority identity, source, request, calendar key, byte count, chunk count and SHA-256 of the original snapshot. Header, chunks, optional calendar index and schema fence commit in one bbolt transaction. This changes no account, policy version, quota window, held capacity, grants, migration record, seed or identity.

## Compatibility and validation

Unmodified schema-4 journals continue to open without mutation. A successful first capture creates the capture bucket and changes the journal metadata schema to 5 atomically. Validation rejection and a rolled-back precommit transaction leave the original logical state intact. An uncertain storage failure is resolved by reopening the retained file: either the original schema-4 state or the exact complete schema-5 capture may exist. Never recreate or discard the file after a lost commit reply. New readers accept schema 4 without the new bucket and schema 5 only with the complete validated capture bucket.

Schema-5 opening verifies every header, exact chunk sequence, byte count, digest, unique request/calendar association and absence of orphan or unknown entries, as well as every existing account/grant/change/migration invariant. Corrupt or incomplete capture history refuses opening. Large snapshots are hashed chunk by chunk during validation.

The actual schema-4 implementation from the committed pre-change source must be built as a retained local probe and used against a closed schema-5 fixture. It must reject opening before any write. A source-string assertion or a simulated old validator is insufficient. This proves this capture format's old-writer fence; it does not retrospectively establish rollback safety for schema-4 policy evidence.

## Subsequent integration

Ordinary reset callers are unchanged in this storage-only stage. A later service integration must pin the existing database and lifecycle owner before capture, use the journal's original selection before inserting a missing SQL row, and preserve calendar uniqueness. It must separately prove the prepared/completed boundary, per-client reset evidence, SQL failures and mixed managed/legacy acknowledgement. Captured metadata alone is not proof that a reset completed. That integration requires its own design and RED-to-GREEN acceptance before activation.

Existing post-migration operations without a protected capture cannot be declared fully recovered from partial policy proofs. Known ambiguity stays closed. Full journal compaction, entire panel-owner loss, authenticated node transport/global scopes, original wider protocol/platform acceptance and paired distribution remain separate outstanding tasks.
