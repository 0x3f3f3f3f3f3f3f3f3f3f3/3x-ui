# Verification record

No new protocol or whole-system policy feature has passed acceptance yet.
This record distinguishes source review, arithmetic tests, integration and
real-client data-plane evidence. Skips are not passes.

## Environment

- Linux arm64, kernel 6.17.0-1018-oracle, UTC, isolated development checkout.
- Official Go 1.27.1 linux/arm64 SHA-256:
  `3450b45a3f9ee8568792736a5c5e70a1f2e9b36c35a8f74958c03e51d7d92bec`.
- Official Node v26.10.0 linux/arm64 SHA-256:
  `7a6353f63eb3d04765004b4adf172616243e4522434635cb1d26288658b04ab5`.
- `nft`, `tc`, `ip` are present; availability is not a privileged capability test.
- Docker and Go were absent initially. Go and Node installed under
  `/root/toolchains`, without changing host services.
- Surge Mac/iOS client and license are not present. Snell real interoperability
  remains unexecuted even after any future server/config test.

## Commands and observed evidence

| Command / check | Result |
|---|---|
| `git status --short --branch` on initial clone | clean main, no user changes |
| GitHub fork API / release API / `git ls-remote` | provenance and SHAs in audit.md |
| `git rev-list --count v3.8.5..HEAD` at baseline | 64 |
| Safe local credential classification | SSH private key, mode 0600; content never printed |
| SSH strict host check (first attempt) | failed: no known ED25519 host key |
| SSH with keys pinned from official HTTPS `/meta` | authenticated as fork owner; exit 1 is GitHub's expected no-shell response |
| `go mod download` | exit 0 |
| `make test-go` | restricted socket run failed; authorized rerun exit 0, 46 packages passed |
| `npm ci` | exit 0, 621 packages installed, npm reported 0 vulnerabilities |

Network access from the restricted shell failed DNS resolution initially;
authorized network tool runs succeeded. No host-key-check bypass was used.
No kernel networking changes, deployment, public release or default-branch
merge were performed.

## Acceptance not yet executed

All real-protocol bandwidth, quota, routing, authentication, recovery and
multi-node tests from requirements sections A–D remain open. Full static,
race, frontend, PostgreSQL and packaging checks also remain open until their
actual results are recorded. The implementation must establish pre-test burst,
sampling, cutoff and overshoot bounds; none is claimed for unbuilt adapters.

## Exact arithmetic milestone (not runtime billing integration)

- `go test ./internal/clientpolicy` first failed on missing arithmetic symbols.
- `go test -race -count=1 ./internal/clientpolicy`: passed, 1.244s.
- `go test -run '^$' -fuzz FuzzChargeMatchesArbitraryPrecision -fuzztime=10s ./internal/clientpolicy`:
  passed, 258,440 executions; checked against independent arbitrary-precision arithmetic.
- `go vet ./internal/clientpolicy`: executed before the arithmetic commit.
- Coverage includes exact multipliers 0.5/1/1.5/2/10, fractional carry across
  1,000,001 single-byte events, different batch sizes, segmented 10 GiB at 1×
  then 5 GiB at 2× = 20 GiB, quota allowance and checked int64 limits.
- No database persistence, data path, UI or backend feature is claimed from
  these arithmetic tests. Identity/migration and runtime integration follow.
- Frontend baseline `npm run typecheck`: exit 0. Full `npm test` still running.
- Git hook initially used host Node18 and failed on util.styleText; rerunning
  the commit with the repository-required task-local Node26 succeeded.
- First push of 4e2ff8c6 was rejected by automatic approval review because it
  did not recognize authorization for external data transfer/remote branch
  creation. No push occurred. Explicit user approval requested; local work
  continues. No authentication secret was copied into the repository.

## Push verification

The user subsequently explicitly approved this task's feature-branch pushes.
`git push -u origin feat/unified-client-policy-backends` succeeded, and
`git ls-remote origin refs/heads/feat/unified-client-policy-backends` returned
`3e226aeaca84392dd3b534b1c341baa955cdbd0f`, exactly matching local HEAD.
This includes audit commit `4e2ff8c6` and arithmetic commit `3e226aea`.

## Immutable local client identity milestone

Implemented: create-only, internal UUID `ClientRecord.PolicyID`; existing rows
are backfilled before index creation. Public JSON cannot choose that identity.
Renaming/editing credentials preserves it; deletion and recreation generates
a new identity. Raw counters/quota/manual disable are untouched by migration.
This is local identity infrastructure, not yet node identity synchronization,
persistent billed usage or authenticated backend enforcement.

- `go test ./internal/database -run TestClientPolicyIdentity -count=1`:
  RED before implementation (missing PolicyID), then PASS.
- A 1,003-client nullable legacy fixture crosses the 500-row migration batch
  boundary, retains existing UUIDs, and survives DumpSQLite/RestoreSQLite with
  every identity preserved. Focused SQLite run passed in 10.919s.
- An isolated PostgreSQL 16.15 instance was unpacked into `/tmp/3x-ui-pg-tools`
  and run as `nobody`, listening only on 127.0.0.1:55432 with a private temp
  socket/data directory. No system PostgreSQL service was installed or changed.
- `XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' go test ./internal/database -run TestClientPolicyIdentityMigration_Postgres -count=1 -v`:
  PASS, 4.370s; 1,003 legacy rows, UUID uniqueness, disable preservation,
  reopen, duplicate rejection and actual SQLite→PostgreSQL migration.
- `npm run gen`: PASS; no generated API diff because PolicyID is internal.
- `npm run build`: PASS, Vite built the production bundles in 6.39s.
- Full frontend baseline initially exited 1: Chromium could not load libatk.
  Official Playwright runtime dependencies were then installed and the suite
  rerun. That run has component test timeouts; final details are still pending.

Additional checks for this milestone:

- Temporarily removing the migration call produced a behavioral failure:
  legacy policy identity was not a UUID. Restoring the call returned GREEN
  (1.461s); this proves the test detects a missing migration, not just a type.
- `go test -race -count=1 ./internal/database ./internal/database/model`:
  PASS (159.473s and 1.648s); PostgreSQL-gated tests are not counted from this run.
- `go test -shuffle=on -count=1 ./internal/database ./internal/database/model ./internal/web/service ./internal/web/controller ./internal/sub`:
  PASS (47.443s, 0.256s, 128.656s, 13.025s, 35.013s).
- `go build ./...`: exit 0 with real Vite bundles embedded.
- `git diff --check`: clean.

- `go vet ./internal/clientpolicy ./internal/database/...`: exit 0.
- `golangci-lint run ./internal/clientpolicy/... ./internal/database/...`:
  exit 0, 0 issues; formatter diff is empty with the Go toolchain on PATH.
- Full frontend baseline: 169 test files passed, 5 failed; 1,735 tests passed,
  7 timed out at the unchanged 5-second limit. An unhandled React scheduler
  `window is not defined` occurred during happ-settings-presets cleanup.
- The failed files were happ-routing-editor (3), client-bulk-calendar-renewal,
  client-calendar-renewal, client-qr-modal-qr-capacity, calendar-expire-setting.
  Isolated rerun of all five files passed (23 tests), without code or timeout
  changes. The original full-suite failure remains recorded; cleanup needs
  verification during the next complete frontend run.

The complete request's unresolved requirements in plan.md and matrix.md have
not been removed or reclassified as complete.
