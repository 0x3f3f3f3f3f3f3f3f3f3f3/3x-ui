# SSH inbound runtime observability implementation plan

> Continue Task 5 through inline `superpowers:executing-plans`; the full task
> already authorizes ordinary engineering decisions. This is a bounded detail
> of the existing plan, not a replacement of its unfinished requirements.

**Goal:** Let administrators distinguish configured SSH inbounds from actual
listeners and protected failures, and see authenticated connection counts.

**Architecture:** Read a synchronized snapshot of the existing server/manager;
join it to the requesting user's inbound metadata, without creating a manager,
opening a socket, reconciling configuration or parsing private keys. Expose that
view through the existing inbound API group and list. No stored status or new
database table is needed.

**Stack:** Go/Gin, existing managed SSH runtime, generated Go→Zod contracts,
React Query and the existing Ant Design inbound list.

**Spec:** [Full requirements](requirements.zh-CN.md), sections II, III, V, IX
and X; [main plan](plan.md), Task 5.

## Decisions and constraints

- Running means the SSH listener is serving against the applied router. It does
  not prove a target is reachable or override a client's quota/expiry restriction.
- Count authenticated SSH transports; raw handshakes do not count and multiple
  channels on one transport do not inflate the connection count. Do not label
  this as device count, unique people, or individual client online status.
- States: `disabled`, `idle` (no enabled canonical clients), `pending` (awaiting
  configuration), `running`, `protected`, `unsupported` (remote SSH row). Retain
  the manager's existing sanitized reason; never include key/account payloads,
  bridge credentials, client identity, targets or raw backend diagnostics.
- Desired enable state is distinct from applied state. Full-change suspension
  reports pending; a missing or failed listener cannot report running. Existing
  reconciliation runs every 250ms. The list refreshes status every 3s while it
  contains SSH rows; a failed fetch displays unavailable, not a fabricated state.
- Existing node transport does not distribute outbound templates; its scoped
  token explicitly receives 403 for those settings. Automatic node provisioning
  and global policy execution remain under their original open requirements.
  This local observability increment must not pretend remote runtime support.
- Use the logged-in user's ID to filter metadata, matching other inbound reads.
  The new status route is admin/session-only under the existing scope rules;
  monitor/node-sync tokens remain denied until separately justified integration.
- Preserve lock order (runtime state → manager → server), query the database
  outside those locks, and return copies. Reading before first application,
  after Stop, or after a database replacement must not resurrect resources.
- New Go/TS comment blocks remain at most two lines. Translate EN/ZH and add
  all locale fallback keys in the same commit. Generate contracts; no hand edits.

## Review focus

- Stalled unsigned handshakes must not appear as authenticated connections.
- Listener failure/core exit must clear running/counts; later reconciliation
  must replace the protected reason after recovery.
- Deleted/recreated inbound identity and a replaced DB handle must not inherit
  another runtime's connection count or old protected reason.
- Authentication and metadata editing can race with snapshots without exposing
  mutable maps, deadlocking or starting a service during the read.
- Missing status and HTTP failure must remain visibly unknown in the list;
  the configured enable switch must not imply successful runtime application.

## Task 1: Server lifecycle snapshot

Files: `internal/sshtunnel/server.go`, new `status.go`, `status_test.go`.

Interface: `Server.Status() Status`, with `Listening bool` and
`AuthenticatedConnections int`. Track actual Serve lifetime independently of
the existing single-start guard. Snapshot under the server mutex.

- [x] Write `TestStatusTracksAuthenticatedTransports`: real listener, unsigned
  connection, two authenticated transports for one client, two TCP
  channels on one transport with real echoes. Counts are 0, 2, 2; close one
  transport → 1; revoke remaining credentials → 0; Close → not listening/0.
- [x] Write `TestStatusStopsAfterListenerFailure`: close the owned listener
  while the server context remains live; Serve returns and Listening is false.
- [x] Run `go test ./internal/sshtunnel -run '^TestStatus' -count=1 -v`; observe
  missing snapshot/lifecycle behavior, implement it, then require both tests pass.
- [x] Run the full SSH package with `-race -count=1`; require actual cases to run
  with no race reports, recording any conditional skip separately.

## Task 2: Read-only, authorized manager/API view

Files: new `internal/web/service/ssh_runtime_status.go` and tests; existing
`ssh_runtime.go`, `internal/web/controller/inbound.go`, controller tests,
`tools/openapigen/main.go`, `frontend/src/pages/api-docs/endpoints.ts`.

Interface: `InboundService.GetSSHRuntimeStatuses(userID int)
([]SSHRuntimeStatus, error)`. DTO fields: `inboundId`, `state`, `reason`,
`authenticatedConnections`. GET `/panel/api/inbounds/ssh/status` returns the
existing success/object envelope with an array. An empty result is `[]`.

- [x] RED-first: cold configured listener remains unbound and manager remains
  nil after status read; disabled, no-client and remote rows show their states;
  native/other-user rows are absent. Database errors propagate.
- [x] Extend real Xray/SSH lifecycle acceptance on SQLite/PostgreSQL: running
  listener/real sessions, occupied port protected, recovery, core stop, full-edit
  suspension, deletion and DB replacement. Serialized status contains no secrets.
- [x] Implement snapshot and filtering without calling managedSSHRuntime or
  build/prepare/reconcile. Follow existing sanitized failure reasons.
- [x] Register route and DTO; controller authorization tests cover anonymous,
  valid monitor/node-sync tokens and owner filtering. Generate types/OpenAPI and
  copy/regenerate API documentation as required by the repository contract.
- [x] Run affected/full Go suites, real lifecycle tests and race checks;
  record exact skips, then commit the independently verified backend increment.

## Task 3: Existing inbound list and real browser acceptance

Files: `frontend/src/pages/inbounds/useInbounds.ts`, `InboundsPage.tsx`,
`list/useInboundColumns.tsx` and list props/types; new local status component
and tests; all locale JSON files; existing SSH browser fixture; docs/matrix.

- [x] RED-first UI: enabled/protected remains visibly protected with its reason;
  pending never displays running; two authenticated transports display 2;
  refetch failure displays unavailable; non-SSH rows retain existing rendering.
- [x] Add conditional 3s status query and pass statuses/error to the existing
  list. Place runtime state beside the configured enable switch with a tooltip
  explaining listener readiness and authenticated transport count.
- [x] Actual browser creates SSH listener/client and sees running only after
  application; an owned listener collision shows protected, recovery shows
  running, real SSH connects/disconnects update counts, disable clears runtime.
- [x] Full frontend suite, typecheck/lint/build, fresh embedded panel and actual
  browser acceptance. Update matrix/operations/validation, commit and approved
  feature push, then independently verify the remote SHA. Full task stays open.
