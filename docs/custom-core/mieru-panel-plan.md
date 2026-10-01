# Mieru panel integration implementation plan

> Execute inline with superpowers:executing-plans and test-driven-development.

**Goal:** Connect existing panel client/inbound/outbound workflows to the tested
native mieru core, sharing canonical identity and policy with Tunnel.

**Architecture:** Existing SQL client records own protocol-specific credentials;
runtime emits canonical native users and negotiates the tested core capability.
Existing React forms and official library serializers serve UI/export.

**Stack:** Go1.27.1, SQLite/PostgreSQL, React19/Zod4, official mieru v3.38.0.

**Spec:** [mieru-panel-design.md](mieru-panel-design.md).

## Global constraints

- One Custom Xray business process; no mita/panel decoder/local SOCKS bridge.
- `mieruUsername`/`mieruPassword` are separate from other protocol credentials.
- Trusted clientId comes from current clients.stable_id and stored links only.
- Native credential limits are UTF-8 and1..64bytes, not JavaScript characters.
- Preserve SQLite/PG backup and existing schema/data; no module-cache edits.
- Publish verified logical commits only on feature/custom-xray-unified-policy.
- Complete scoped increments without implying full protocol/project completion.

## Review focus

- Omitted credentials and email rename must preserve authentication and owner.
- Disabled siblings must still participate in duplicate-username validation.
- Late SQL credential/attachment changes must fail stale config compilation.
- Missing native capability must reject before SQL preparation/handler writes.
- Exact UDP transport, profile and Unicode/IPv6 export must survive round trips.

## Task8B1: Persisted credentials and canonical runtime binding

Files: model/model.go; service/{client_crud.go,client_link.go,xray.go,
client_policy_config.go,inbound.go}; create service/mieru_accounts.go;
xray/{api.go,api_managed.go,hot_diff.go}; core/app/clientpolicy/command/command.go;
create model/model_mieru_test.go, service/mieru_accounts_test.go,
xray/mieru_managed_test.go and database/mieru_credentials_migrate_test.go.
Paths above are relative to internal/database, internal/web, internal, or
core/xray as established by their existing packages.

Interfaces: keep ClientService Create/Update/Attach/SyncInbound and existing
GetManagedXrayConfig; new internal validation/projection helpers accept the
existing model.Client/ClientRecord and xray.InboundConfig types. No new public
CRUD endpoint is needed.

- [ ] JSON credential persistence/record round-trip, omitted update, duplicate
  listener username, independent protocol passwords and saved SQL binding tests
  first fail behaviorally on the current source.
- [ ] Add two SQL/client fields, conversions/merge/defaults and UTF-8 byte-limit
  validation; preserve old fields/backup defaults and pass credential tests.
- [ ] Native users generation and concurrent-rotation tests first fail; add
  immutable SQL identity binding, credential equality and empty users arrays.
- [ ] Missing native capability and account mutations first fail; add typed
  account, negotiated native marker and users-array hot diff, passing real
  private-control tests against current and preceding checkpoint binaries.
- [ ] Verify SQLite/actualPG migration/export/reopen and public CRUD lifecycle
  against the actual native binary. Add required exact test names to CI.
- [ ] Affected race, generation/lint/vet/root Go checks and one read-only review;
  correct findings, logical commit/exact fork push and distinct build provenance.

## Task8B2: Existing forms and attach/bulk flows

Files: frontend/src/schemas/{client.ts,primitives/protocol.ts,
primitives/outbound-protocol.ts,protocols/inbound/index.ts,
protocols/outbound/index.ts}; create both protocols/{inbound,outbound}/mieru.ts;
lib/xray/{inbound-defaults.ts,inbound-form-adapter.ts,outbound-defaults.ts,
outbound-form-adapter.ts}; pages/clients/{ClientFormModal.tsx,ClientBulkAddModal.tsx};
pages/inbounds/form/{InboundFormModal.tsx,protocols/index.ts}; create
protocols/mieru.tsx; pages/xray/outbounds/{OutboundFormModal.tsx,
protocols/index.ts}; create outbound protocols/mieru.tsx;
internal/web/translation/{en-US.json,zh-CN.json}; generated API schema outputs.

- [ ] Real forms create/edit/save, separate credentials, attached shared client,
  transport/mux values and byte-limit errors fail before adding protocol choices.
- [ ] Implement existing-form fields/defaults/projections and EN/ZH copy;
  unsupported transport/security options are absent or explicitly rejected.
- [ ] Save/reopen/clone/bulk controls preserve complete protocol settings and
  existing unrelated protocol forms. Run affected component/unit tests,
  typecheck/lint/generation/frontend build and one scoped review.
- [ ] Commit/exact fork push after gates; full export acceptance remains open.

## Task8B3: Official export and combined public lifecycle acceptance

Files: service/inbound_sublink.go and relevant existing subscription serializers;
create service/mieru_export.go and official-parser tests; existing client
QR/download/link modules and new mieru export tests. Derive exact serializer
dispatch files from source before editing, documenting them in this plan.

- [ ] Official ClientProfile/ClientConfig URL parser round-trip tests first fail
  for TCP/UDP, domain/IPv6 and escaped Unicode credentials; implement public
  appctl serializers, explicit supported-format dispatch and profile mapping.
- [ ] Combined existing HTTP API/form payload→SQL→core→official client→target
  test fails before wiring; verify exact usage/shared Tunnel rate/quota,
  credential rotation/sibling survival, disable/delete/expiry/restart/listener
  removal, export/reimport and backup/restore.
- [ ] Complete combined frontend/backend/core gates and one scoped review;
  publish logical commit and clean-source artifacts. Update capability matrix
  only for actually verified behaviors; other protocols/tasks remain open.
