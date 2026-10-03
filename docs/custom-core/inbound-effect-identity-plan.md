# Stable inbound effect identity implementation plan

> Use superpowers:executing-plans inline; no implementer agents. One fresh final bounded review and one author Critical/Important RED/GREEN correction pass. Existing autonomous authorization applies. Preserve all source/evidence/workspaces.

Goal: give original inbound resources a persistent UUID so protected business effects cannot transfer to a new resource reusing its numeric ID.
Spec: docs/custom-core/inbound-effect-identity-design.md, original requirements/accounting and restore-authority-lifecycle-plan.

## Global Constraints

- Native Snell/mieru/SSH, shared multiplier/directional limits and TCP/UDP Tunnel remain the priority. Frozen361e981a paired acceptance runs independently.
- Private UUID stays out of ordinary JSON/forms; existing credentials, owner/key identity, configuration and statistics retain exact bytes/values.
- Bounded256-row SQL migration, private SQLite/isolated PostgreSQL only; no core API/journal schema/production database action.
- Normal milestone pushes only to authorized fork feature/custom-xray-unified-policy; independently verify remote SHA. No force/default merge/release/deploy.

## Review Focus

- Nullable/partial existing schemas, duplicate/noncanonical identifiers and pagination must never replace a retained valid UUID or alter business data.
- Create-only GORM permission plus public create/copy/import/update must distinguish a new resource from an ordinary edit even when numeric ID/tag/port/credentials are reused.
- Hidden JSON identity and SQL backup behavior must fit current node/import/config/resource-binding paths without implying exact legacy/remote effects already exist.
- Backend/private/native/build provenance remains explicit; public scale and whole-parent acceptance remain required.

## Task 1: Persist and migrate stable inbound UUIDs

Files: model.go, new model/inbound_identity.go and database/inbound_identity.go plus identity tests; database/db.go pre-AutoMigrate hook.
Produces: private create-only Inbound.StableID, BeforeCreate canonical nonzero UUID generation/validation, migrateInboundStableIDColumn/migrateInboundStableIDs bounded backfill.

- [x] Step1: Actual persisted create/update/delete/numeric reuse and historical missing/nullable/partial1001-row migration tests. Expected RED: current source cannot persist a stable inbound UUID; no compile-only missing-field claim.
- [x] Step2: Implement model identity and transactional bounded migration before unique-index creation; validate retained values and preserve exact business fields. Expected GREEN: same UUID across updates/reinitialization, new UUID for new resource, no data drift.
- [x] Step3: Exercise retained invalid/duplicate IDs and real PostgreSQL missing/partial schemas, vet/diff, commit and task-done. Expected: exact refusal/rollback or correct migration; backend provenance explicit.

## Task 2: Preserve identity through public resource edits

Files: inbound.go and focused public create/import/update/copy tests. Consumes Task1 stable storage.

- [x] Step1: Reproduce public AddInbound copying a saved object and ordinary UpdateInbound preservation across port/tag/config changes. Expected RED: new copied resource must not reuse old UUID; existing identity survives edits.
- [x] Step2: Clear source identity on new resource admission and preserve stored identity during ordinary edits; retain native credentials/SSH key ownership/client links/config/statistics. Expected GREEN: new resources receive new UUIDs, ordinary updates keep the original.
- [x] Step3: Run affected migration/import/native-owner races on both real backends, vet/diff, commit and task-done. Expected PASS; no native wire or production deploy claim from SQL-only contracts.

## Task 3: Regression, sole review and verified stage push

- [x] Step1: Full make test-go with actual core/original writer probes; require new named migration/resource parents in both CI database jobs and verify actual final name coverage. Expected: corresponding checks PASS with every skip/failure honestly recorded.
- [x] Step2: Record evidence/remaining parent, commit/task-done. Expected: complete bounded source/test contract; exact legacy/inbound/remote effect preparations remain next parent work.
- [x] Step3: Sole fresh entire bounded diff review; regrade/rule every declined boundary; one author Critical/Important TDD fix pass and green suite. Expected: no bounded blocker, no second review; preserve workspace.
- [x] Step4: Authorized normal feature push and independent remote/local SHA comparison. Expected: identical SHAs, continue the full parent objective.
