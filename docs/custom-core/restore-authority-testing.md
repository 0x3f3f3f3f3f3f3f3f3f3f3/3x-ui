# Restore lifecycle and authority evidence

Original tasks 11/12 remain open. This record distinguishes executed lifecycle corrections from pending authority/global allocation. The preceding bounded restore/CI stage is published at `749d7d9f435a00725823c99fd8aae5cd20154b61`; its source product is `5ef27753`, its single review found no code findings, and the full original PostgreSQL set passed 186 cases in 17 batches, including all 144 mandatory names exactly once with zero skips.

## Pool publication and active transactions

Working source based on plan commit `f45cc847` reproduced both a Go race between DB replacement and GetDB/dialect readers and publication of a pool before accounting-table migration finished. Atomic publication now occurs after migration/seed completion. Migration helpers use the candidate pool's dialect. Close/replacement and SQLite backup participate in the lifecycle lock.

A paused real serialized SQL transaction reproduced CloseDB returning before that transaction committed. Serialized transactions now retain a lifecycle read guard through commit; replacement/close waits for them. Closed/replaced/unready pools reject old queued work with the same exported error. This is a database lifecycle boundary, not the whole import/runtime or durable snapshot authority protocol.

Executed checks, logs under /root/task-evidence:

- Publication RED: `restore-authority-publication-red.log`; premature schema and race. Same race GREEN: `restore-authority-publication-green.log`, 5.178 seconds.
- Active transaction RED: `restore-authority-active-transaction-red.log`; early successful close. Combined publication/active/queue/delayed regressions GREEN: `restore-authority-publication-and-active-transaction-green.log`, database 4.000 / service 5.300 seconds.
- Actual private PostgreSQL active transaction and queued replacement, with the intentionally SQLite delayed-reply case: `restore-authority-active-transaction-postgres-green.log`, service 9.556 seconds.
- Complete database race: `restore-authority-database-race-suite.log`, PASS 102.553 seconds. Optional PostgreSQL cases absent from that command are not counted as zero skips.
- Complete root `make test-go` with unchanged real core SHA256 `5c2a251ce758dd77efc5a6cb94551fe312d90ebc23ec31a9b5df423fd858dd14`: `restore-authority-root-suite.log`, 50 project packages plus the extra AmneziaWG device package passed; service 153.720 seconds.
- Actual private PostgreSQL identity/snapshot/policy-receipt/owner migrations: `restore-authority-postgres-source-migration-green.log`, five top-level PASS, database 24.053 seconds. Its service invocation matched no tests and is not service test evidence.
- Accurate native/legacy PostgreSQL recovery selections: `restore-authority-native-legacy-postgres-migration-green.log`, five top-level PASS, 15.956 seconds. Combined with the preceding selection, eight cross-database preservation cases cover policy/receipts, password ownership, unknown/config-source traffic, Snell, mieru and SSH business trust/canonical identity.
- Database/service vet and diff check passed; `restore-authority-db-service-vet.log`.

## Failures retained for subsequent implementation

`TestDatabaseRestoreConcurrentImportKeepsFirstStagedDatabase` reproduced overlapping imports replacing/removing the first `.temp` database, the second request succeeding, the first failing to rename its missing stage, and two stop/restart paths. `restore-authority-concurrent-import-red.log` records the actual behavior. Imports now have one runtime owner and private staging and fallback directories. Existing `.temp` and `.backup` artifacts are preserved.

Ten restore tests pass with race detection and the unchanged actual core: `restore-authority-private-staging-runtime-green.log`. They cover concurrent import rejection, forced restart fencing, retired-owner rejection, unrelated artifact preservation, three failed-stop routes, three failed-reopen routes, usable fallback recovery and successful real TCP Tunnel restoration. The real test preserves canonical UUID and requires exact additional raw upload/download 5/5 and billed usage 20 at multiplier 2. Previous real-test fixture failure was a 108-byte Unix socket address; relocating its initial SQL snapshot to the managed fixture's existing short runtime directory fixed the fixture without changing deadlines or payload assertions.

The failed-reopen RED log proves all three routes previously attempted activation after reopening failed: `restore-authority-reopen-failure-red.log`. Existing fallback deletion RED: `restore-authority-existing-fallback-red.log`. Corrected runtime tests and service vet passed; `restore-authority-import-runtime-vet.log`.

`TestConfiguredCopiedStateCannotAdmitWithoutFreshAuthority` uses the actual registered configured feature constructor and Start on two independent copies. Both retained source `node-1`, epoch 2 and independently admitted/billed 100 bytes against the original quota of 100, total 200 with no fresh authority grant. Logs `restore-authority-copied-core-state-{red,retained-red}.log`; retained original/copies in `/tmp/policy-authority-node-clones-3692493143`. This is real policy admission, not an encrypted protocol/client interoperability test. The future grant regression remains intentionally failing/uncommitted; no authority/core grant implementation or clone-safety success is claimed.

Complete mutation admission, authority-aware phase reconciliation, crash-persistent authority outside restored SQL/core state, boot-bound finite grants, coordinated nodes and external whole-authority cloning exclusion remain pending. The in-process runtime owner and successful SQL reopen checks do not establish durable allocation authority. Existing managed remote-scope guards remain active.

## SQL admission checkpoint

Published pools now guard ordinary ORM, raw execution, row queries and complete SQL transactions. Imports carry an explicit owner context for reconciliation; stale contexts cannot borrow a later import's privileges. Pool closure and owner release join direct as well as serialized transactions. Canceled transactions finish rollback and release their guards. The known SQLite deferred-connection snapshot path is guarded for its complete callback. Managed reset/reconciliation rejects the active restore before runtime work; compiled managed candidates retain their originating database handle for policy preparation.

Actual evidence: `restore-authority-admission-generation-green.log` has 25 top-level race PASS across database/service; private PostgreSQL seven-parent gate passed 25.804 seconds, with owner-release transaction joining separately passing 5.01 seconds. Complete database race passed 129.689 seconds. The existing lost-commit-ack fixture now accepts both supported GORM transaction-begin interfaces and retains all exact accounting assertions; complete job race passed.

The complete backend gate passed after resolving environmental disk exhaustion: `restore-authority-admission-root-suite-space-recovered.log`, 51 project packages including the initial unused journal foundation, plus the additional AmneziaWG device package; service 155.106 seconds. Receipt `restore-authority-full-validation-receipt.json` records that the journal's later retired-boot correction has a separate package race gate. An initial receipt parser incorrectly excluded the root module path; the command itself passed. Database/service/job/journal vet and diff check passed.

Retained RED evidence includes unowned writes, reset-runtime entry, stale managed candidate, direct transaction closure and owner-release without joining. Earlier ENOSPC compilation failures are environmental evidence only. Old compiler cache objects were preserved as byte-verified durable gzip archives, with a restoration manifest; business files, source, logs, worktrees and paired distributions were preserved.

This is an in-process SQL admission checkpoint. Durable restore phase/authority reconciliation, asynchronous operations outside these checked entrypoints, core boot grants and coordinated enforcement remain open.
