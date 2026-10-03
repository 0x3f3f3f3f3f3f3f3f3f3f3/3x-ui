# Stable inbound resource identity acceptance

This increment gives inbound resources a private, persistent canonical UUID. It supports subsequent exact reset-effect preparation without treating a reused numeric ID, tag, port or credential as the same resource. Native Snell/mieru/SSH and shared multiplier, directional limits and TCP/UDP Tunnel remain the product priority.

Actual lifecycle and historical migration tests failed before implementation: no persisted identity, missing/empty migrated identities, and admission of corrupt retained identifiers. Public-copy tests then reproduced a unique-index failure, and ordinary edits returned the caller's replacement UUID while SQL retained the original. Logs retain both meaningful failures and the separately corrected SSH-password/import-payload fixtures.

The model generates and validates UUIDs on creation. Before general index migration, a transaction reads historical identities in 256-row pages, fills missing values, preserves valid values and refuses invalid or duplicate values. JSON/form payloads omit the UUID. Public new-resource admission discards a copied UUID; ordinary edits recover the original identity from SQL, including the returned Go object.

Seven required parents pass with race detection on both private backends:

| Parent | SQLite | PostgreSQL |
| --- | --- | --- |
| TestInboundStableIdentityRestoreStaging | PASS | PASS |
| TestInboundStableIdentityDatabaseExport | PASS | PASS |
| TestInboundStableIdentityHistoricalMigration | PASS | PASS |
| TestInboundStableIdentityRejectsCorruptMigration | PASS | PASS |
| TestInboundStableIdentityLifecycle | PASS | PASS |
| TestInboundStableIdentityPublicCopyAndImport | PASS | PASS |
| TestInboundStableIdentityPublicNativeEdits | PASS | PASS |

Historical missing/nullable/partial fixtures each contain 1001 rows, cross several migration pages, retain valid UUIDs and exact settings with an integer greater than 2^53, preserve counters and configuration, and reopen without changing identities. Corrupt retained UUIDs fail initialization and roll back preceding identity writes. Deleting and recreating a resource with the same numeric ID produces a new UUID.

Public ordinary edits cover VLESS, Snell, mieru, SSH and TCP/UDP Tunnel. They preserve canonical client identity, native credentials, owner links, SSH host-key identity and inbound counters across port/tag/remark changes. Portable JSON export/import and Go-object copying allocate independent resource UUIDs. These are actual SQL/public-service contracts; they do not independently prove wire interoperability or login middleware.

Final required gate times: SQLite database 21.818s/service 7.250s; PostgreSQL database 32.121s/service 14.058s, zero skips/failures. Broader affected races passed 28 SQLite parents with one explicit PostgreSQL-only skip (13.672s), and 29 PostgreSQL parents with no skips (62.174s). Database/model/service/authority vet, diff checking and both CI named-parent requirements pass locally; no hosted CI result is claimed.

Evidence is retained under `/root/task-evidence/restore-authority-execution-bridge/inbound-effect-identity-*`, including JSONL RED/GREEN outputs, backend summaries, exact source hashes and full Go output. The full regression uses the verified paired checkpoint's Custom Xray from source361e981a and the retained original schema4/schema5 writer executables. The core source is unchanged by this increment. Current panel source also passes five actual Custom Xray native/shared HTTP parents on each backend, including Snell TCP/UDP/QUIC, mieru, OpenSSH/reverse/strict outbound and shared bulk policy/Tunnel accounting: SQLite73.435s, PostgreSQL94.507s, zero skips/failures. These fixtures supply an authenticated HTTP user context; they are not independent login-middleware, proprietary-device or multiplatform proof.

Actual whole-database export additionally reproduced map insertion bypassing the new hook. Export now invokes inbound identity generation/validation before constructing maps, as it already does for client identity. Missing/partial historical SQLite and PostgreSQL sources export into SQLite with independent UUIDs and unchanged exact business data; valid retained UUIDs survive and invalid values refuse export. The source remains read-only. The UUID remains in full SQL backups and model-based database migration. Exact legacy/group/global/node/calendar/manual inbound/remote effects, durable execution acknowledgements and complete restore/cloning tests still require subsequent work. This increment does not complete the parent project, coordinated nodes, remaining sidecar migration, platform/device acceptance or final distribution. Previous packages, fixtures and workspaces remain retained.

The first full Go run failed a retained lost-ack test fixture because an OS-free sampled port matched its saved but inactive Tunnel port. The corrected fixture holds that socket while selecting another port, preserves all business-flow assertions, and passes separately with race detection on both databases (SQLite11.265s/PostgreSQL17.629s, all four children, zero skips/failures) before the final full rerun. The initial failed log and fixture remain retained.

Final `make test-go` passes all 52 tested packages, including service278.417s, database44.386s, authority30.225s, subscription53.920s and the additional AmneziaWG device package1.085s. Both original historical writer probes and the verified Custom Xray are supplied. This is local full Go verification; ordinary full-run output is not a zero-skip protocol/platform claim.

The sole fresh bounded review found one Important restore-staging regression and no Critical/Minor findings. `PrepareSQLiteForMigration` previously attempted index creation before partial identities were backfilled. The author reproducer failed (2.291s); the single correction pass shares an explicit-database transaction helper and runs it on the staged SQLite file before AutoMigrate. Staging missing/nullable/partial schemas, retained identities, exact business bytes, repeated preparation, corrupt/duplicate refusal and rollback pass; a sentinel proves the active SQLite or PostgreSQL database is untouched. The staged file remains SQLite in both environments because both upload paths consume that format.

After correction, seven identity parents and affected preparation/restore gates pass with race detection in both environments: database23.684s/service22.557s under SQLite, database29.405s/service37.593s under PostgreSQL. Each broad run has21 passing parents and one explicit actual-core restore skip; that skipped parent was then supplied the verified core and passes separately (7.965s/10.489s), leaving22 distinct passing parents per environment. The existing owned-restart test internally selects SQLite in both environments; it is real SQLite backup/import/Tunnel/ledger proof, not end-to-end PostgreSQL restore proof. CI now requires all seven names. The six reviewer-declined parent boundaries are ruled individually in the retained ledger; no second review occurs.

The single correction pass ends with fresh `make test-go` exit0 and all52 tested packages passing, including service275.275s, against the ten unchanged final source/test/CI hashes. Fresh vet/diff and seven required-name checks pass. The sole Important finding is addressed by the observed restore-staging RED→GREEN and full green suite; no second review was requested.

Milestone source `71cbc84252951c4a1ca81a7ab61984a07363b921` was normally pushed to the authorized fork `feature/custom-xray-unified-policy`; independent `ls-remote` exactly matches local HEAD. Publication evidence is retained as `inbound-effect-identity-feature-push-20261003.log` and `inbound-effect-identity-feature-remote-sha-20261003.txt`. No force, default merge, release or deployment occurred. The full parent objective continues.
