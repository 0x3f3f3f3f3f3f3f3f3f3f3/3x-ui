# PostgreSQL owned restore implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans inline. One fresh bounded final review and one author correction pass. Original autonomous engineering authorization applies; retain evidence and workspaces.

**Goal:** Exercise genuine PostgreSQL backup/import while preserving protected client billing and owned core recovery.

**Architecture:** Existing PostgreSQL tool path and private random databases, equivalent URI/keyword tool connections, retained protected journal and boot-owned core startup.

**Tech Stack:** Go1.27.1, pinned pgx/GORM, PostgreSQL16 pg_dump/pg_restore, verified Custom Xray binary.

**Spec:** docs/custom-core/postgres-owned-restore-design.md.

## Global Constraints

- Native Snell/mieru/SSH, shared multiplier/directional limits/TCP-UDP Tunnel and the full original project stay required.
- Actual restore tests create a new random private database; never restore the shared fixture database or production state.
- Password/DSN never appears in process arguments, errors or logs. Preserve TLS and Unix socket semantics; reject malformed/NUL or unsupported connection inputs explicitly.
- Real tools/archive/core and immutable protected accounting prove integration; fake executables and skipped prerequisites do not count.
- Preserve workspaces/archives/fixtures. Normal authorized fork feature push with exact remote/local equality; no force/default merge/release/deploy.

## Review Focus

- Ambiguous/escaped credentials, inherited PG variables and multiple-host/TLS settings must not connect tools to a different database or disclose secrets.
- A dump containing a restored old source, policy/reset window or execution store cannot reduce protected consumption or return uncertain allocation.
- Failed import/reopen/restart and outstanding writers must retain the correct process/database ownership and refuse uncertain listeners.
- New private test databases and required CI gates must prove actual PostgreSQL tooling without restoring shared/public schemas.
- Native credential/identity and current desired state must survive restoration without moving billing history to reused email/numeric identities.

## Task 1: Equivalent PostgreSQL tool connection

Files: create internal/web/service/postgres_connection.go and postgres_connection_test.go; move pgConnEnv from server.go; existing server_pg_restore_test.go remains compatible.

Consumes: `pgConnEnv(dsn string) ([]string, string, error)` used by exportPostgresDB/restorePostgresDump. Produces the same signature with equivalent safe environment for URI and keyword DSNs; no new public API.

- [x] Step1: Add TestPostgresToolConnectionSettings and TestPostgresToolConnectionRejectsUnsafeInput. Literal fixtures assert keyword Unix socket/database/user/port/sslmode; URI query socket; escaped synthetic password; unique configured overrides; runtime options; multiple host behavior; malformed/NUL errors exclude a synthetic secret marker. Run these parents. Expected RED: keyword DSN rejected and query override/unsafe diagnostic behavior exposed.
- [x] Step2: Implement focused parser/environment mapping and remove the duplicate server helper/import. Run new parents plus existing PG restore diagnostics/import preflight tests. Expected PASS without credential output or changed SQLite behavior.
- [x] Step3: Add TestPostgresToolBackupUsesActualPrivateDatabase with actual configured PG fixture/tool opt-in, random database and sentinel rows; exercise real exportPostgresDB, inspect PGDMP and list archive. Expected PASS against the created database; shared schemas untouched. Commit and task-done with the named connection/actual backup parents.

## Task 2: Actual owned PostgreSQL restore

Files: create internal/web/service/client_policy_authority_postgres_restore_test.go and focused test fixture helpers; modify restore implementation only for demonstrated failures; update both backend CI steps and postgres-owned-restore-testing.md.

Consumes: equivalent pgConnEnv; existing actual-core activation/restore lease/protected journal/reset preparation/completion; private database/tool fixture. Produces real dump/import recovery acceptance and required named CI parents.

- [x] Step1: Add TestManagedAuthorityActualPostgresRestorePreservesConsumption and TestManagedAuthorityActualPostgresRestorePreservesPreparedReset with literal actual payload/usage/window/grant assertions before and after old SQL/core restoration. Include current/later policy and canonical credential/membership preservation. Run actual PG/core cases first. Expected RED for any missing restoration invariant; PASS already-correct behavior is characterization, not an invented feature RED.
- [x] Step2: Add TestManagedAuthorityActualPostgresRestoreFailureKeepsOwnedRuntime for real invalid/archive transaction failure and observable listener/database ownership. Fix only reproduced restore defects under TDD. Expected safe original runtime for preflight rejection; closed uncertain execution.
- [x] Step3: Require exact actual PG parents in CI with installed tools/private fixture DSN; run affected both-backend races, actual native/shared HTTP regressions, full make test-go with exact writer probes and fresh vet/diff/input checks. Expected observed required PASS with no hidden skips; record original failures honestly.
- [x] Step4: Commit/task-done with the seven exact actual connection/tool/restore parents. Expected observed required PASS without skip.

## Phase finishing

Run one sole fresh review, one author Critical/Important TDD/full-suite correction, then normal authorized feature push and independent SHA verification. Close bounded ledger and continue the original parent. Expected no bounded blockers and remote equality; no whole-project final claim.
