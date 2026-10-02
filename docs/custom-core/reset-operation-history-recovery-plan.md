# Reset operation history recovery implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` inline. Continue the existing restore-authority plan; do not start a separate implementation agent for each task.

**Goal:** Restore protected migration-era batch/calendar membership and reset times after SQL restoration, without opening another quota window or including newly created clients in an old request.

**Architecture:** The retained journal already stores immutable migration records for `reset-batches` and `reset-times`, and validated later policy evidence stores each acknowledged reset's original timestamp. Restore this metadata through bounded journal pages before cold-start policy compilation. SQL remains a projection: incompatible immutable fields stop activation; timestamps only advance; an acknowledged operation cannot become pending again.

**Tech stack:** Go, the existing bbolt issuance journal, SQLite/PostgreSQL, the existing serialized SQL writer and cold-start lifecycle fence.

**Spec:** [requirements.md](requirements.md), [accounting.md](accounting.md), Task 4 of [restore-authority-lifecycle-plan.md](restore-authority-lifecycle-plan.md).

## Global constraints

- Keep one Custom Xray business process per node. Preserve Snell, mieru, SSH and shared multiplier/rate/TCP/UDP Tunnel acceptance.
- Do not modify migration records, seed hashes, authority identity, current quota windows, grant capacity or known delivered usage when restoring metadata.
- Page at most 128 migration records at a time. Preserve original request IDs, canonical members, selection hashes, scope, calendar time, creation time and acknowledged managed membership.
- Follow the existing source/database replacement fence. Missing or contradictory protected evidence must not grant traffic.
- This bounded continuation does not protect operations first created after migration. That requires a separately designed durable operation witness and explicit old-writer compatibility. Do not claim complete batch/calendar history until that part is implemented and tested.
- The user's original instruction authorizes ordinary engineering decisions and continued inline development; record decisions and continue without another approval handoff.

## Review focus

- An old applied operation restored as pending must retain its original acknowledgement and membership.
- A retried `all` or inbound request must exclude clients added after its captured selection, including renamed and recreated identities.
- A stale SQL reset-time row must not override a later protected reset and make an old calendar eligible again.
- Conflicting immutable SQL operation fields must stop cold activation before business admission.
- A SQL failure, cancellation or pool replacement must preserve the journal and permit an exact projection retry.

## Task 1: Restore migration operation metadata

**Files:** Create `internal/web/service/client_policy_authority_history.go` and `internal/web/service/client_policy_authority_history_test.go`; modify `internal/web/service/client_policy_authority_evidence.go`.

**Interfaces:** Consume `Journal.MigrationPage(kind, prefix, after string, limit int)`, `runSerializedTxContextForDatabase`, and the existing reset target/selection validation. Produce `recoverAuthorityMigrationHistory(ctx context.Context, expected *gorm.DB, journal *policyauthority.Journal) error`, called once before per-account recovery.

- [ ] Write `TestAuthorityMigrationHistoryRestoresOriginalBatchMembership`: create an applied migration-era `all` operation and reset-time row, migrate through the production entrypoint, remove only SQL metadata, create a new client, recover, and retry the original capture. Assert the original targets/request/scope/selection/calendar/creation/managed acknowledgement are exact; the new UUID is absent; journal account, window and held capacity are unchanged.
- [ ] Run that test before implementation. Expected: original operation metadata remains missing and the retry includes the newly created client.
- [ ] Implement bounded decoding, validation and idempotent SQL projection. Existing immutable fields must match; protected applied state may advance a matching pending SQL row, and a contradictory applied managed membership is rejected. Original pending state must not roll a matching later SQL acknowledgement backwards.
- [ ] Add `TestAuthorityMigrationHistoryRejectsConflictingOperation` covering scope, calendar, selection/targets and acknowledged membership conflicts, and `TestAuthorityMigrationHistoryRetriesProjectionFailure` covering rollback, context cancellation and replaced SQL handles. Assert that journal bytes/accounting remain unchanged and no core is activated.
- [ ] Run the targeted history tests with `-race -count=1` on SQLite and the private isolated PostgreSQL test database. Expected: every parent and child passes, with no integration skip.
  Run `go test -race -count=1 -run '^TestAuthorityMigrationHistory' ./internal/web/service`; repeat with the isolated PostgreSQL environment described in [restore-authority-testing.md](restore-authority-testing.md).
- [ ] Commit the verified bounded change with its evidence and remaining limitations.

## Task 2: Preserve monotone reset-time projections

**Files:** Modify `internal/web/service/client_policy_authority_history.go` and `internal/web/service/client_policy_authority_evidence.go`; extend `internal/web/service/client_policy_authority_history_test.go`.

**Interfaces:** Consume `recordClientTrafficResetTimes(tx, ids, at)`, protected `reset-times` migration records and `recoverAuthorityResetTx`. Raise the SQL effective time from validated historical evidence; never recapture the wall clock during recovery.

- [ ] Write `TestAuthorityHistoryRejectsRepeatedOlderCalendar`: retain a protected effective time newer than restored SQL, recover it, and evaluate an older calendar through `scheduledResetEligibleClients`. Assert the canonical client is ineligible and its current quota window/accounting are unchanged. Include a later reset whose protected original `CreatedAt` advances an existing older stamp, and a SQL stamp already ahead that stays ahead.
- [ ] Run before implementation. Expected: restored older SQL time can make the already covered calendar eligible.
- [ ] Project original migration timestamps and each successfully verified reset timestamp with the existing monotone upsert. Keep immutable reset validation and journal/SQL error boundaries intact.
- [ ] Run all targeted history, calendar, reset and authority recovery regressions on SQLite and PostgreSQL. Expected: exact retry/new-member exclusion, timestamp monotonicity and accounting remain correct; no skipped backend acceptance.
  Run `go test -race -count=1 -run '^Test(Authority(MigrationHistory|History)|ClientPolicyReset|ClientTrafficReset|ManagedAuthority)' ./internal/web/service`; repeat with the isolated PostgreSQL environment and current `XRAY_E2E_BINARY`.
- [ ] Commit the verified change, then continue designing protection for post-migration operation membership and completion.
