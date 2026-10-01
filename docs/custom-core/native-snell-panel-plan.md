# Native Snell panel implementation plan

> Execute inline with superpowers:executing-plans and test-driven-development.

**Goal:** Complete native Snell4/5/6 canonical accounts, exclusive local services,
outbounds, existing forms and truthful native exports through the managed core.

**Architecture:** One SQL canonical client owns each Snell listener. An independent
snellPsk projects into its typed native account, immutable owner UUID and flat
core config. Existing policy/runtime controls provide shared accounting and
revocation; no additional business process is introduced.

**Tech Stack:** Go1.27.1, pinned native Snell sources, SQLite/PostgreSQL,
React/AntD/Zod and Node26.10.0.

**Spec:** [native-snell-panel-design.md](native-snell-panel-design.md).

## Global constraints

- Explicit versions4/5/6; PSK UTF-8 <=255bytes; version6 minimum12bytes.
- Exactly one immutable SQL owner per listener; disabled owners reserve identity.
- Initial random32-byte URL-safe PSK; omissions resolved only in the SQL writer.
- v4/v5 off/http obfs; v6 default/unshaped, no legacy obfs; unsafe-raw rejected.
- v5 reserves TCP+UDP on the same port; v4/v6 UDP travels through native TCP.
- No TLS/REALITY/global mux wrappers, panel relay, sidecar or fabricated share URI.
- One final whole-panel review and one correction pass; no per-task reviewer seats.
- Never read/reuse/change Git management keys. Preserve all old data/artifacts.
- Publish only the authorized fork feature branch; no default merge/deployment.

## Review focus

- Public update identity remains fixed under rename/filter/old-label reuse.
- Omitted PSK cannot revert concurrent rotation; all linked version constraints apply.
- Concurrent attach cannot introduce a second owner or transfer a listener.
- Disabled, removed and last-detached owners cannot retain usable business traffic.
- Text exports preserve commas/quotes/backslashes/UTF-8 and refuse unsupported wrappers.

### Task 1: Canonical PSK and exclusive native listener lifecycle

Files: model/model.go and client_persistence.go; database/db.go and migrate_data.go;
web/service/{inbound.go,client_crud.go,client_link.go,client_inbound_apply.go,
client_bulk.go,client_policy_config.go,password_proxy_owner_update.go,port_conflict.go,
tunnel_owner.go}; new service/snell_accounts.go and snell_runtime_test.go;
xray/{hot_diff.go,api_managed.go,api.go}; new xray/snell_managed_test.go;
core/xray/app/clientpolicy/command/command.go and new snell_capability_test.go.
New model/model_snell_test.go, database/snell_credentials_migrate_test.go and
service/snell_accounts_test.go own behavioral regressions.

Interfaces: model.Snell protocol and Client/ClientRecord.SnellPSK json snellPsk;
prepareSnellInbound(*model.Inbound) error; resolveSnellInboundCredentials(*gorm.DB,
*model.Inbound) error; resolveSnellClientCredentials(*model.Client,*model.ClientRecord,
bool) error; validateLinkedSnellCredentialChanges(*gorm.DB,int,[]model.Client,
map[string]*model.ClientRecord) error; bindManagedSnellIdentity(*xray.InboundConfig,
[]model.ClientRecord) error. Native snell userDiff/buildUserAccount consumes flat
psk/clientId/email/level; capability trusted-snell-client-id-v1 is mandatory for
add/change/removal and startup, including disabled/empty removal paths.

- [ ] Write TestSnellPanelCanonicalIndependentPSK and
  TestSnellPanelExclusiveOwnerAndLastDetach. Public AddInbound(version4/5/6)
  must persist snellPsk independently, reject two owners before writes, preserve
  stable UUID and disable/release an empty resource on final detach/delete.
- [ ] Run the tests with -race; expect RED unsupported protocol/lost PSK.
- [ ] Add canonical field/merge/migration/projection and transaction owner fencing;
  test omitted rotation and HTTP/Mixed owner update paths, duplicate membership,
  rename/reused label, version6 short-key rotation and local/remote fences.
- [ ] Add pure native settings/port checks; TestSnellPanelVersionFiveReservesUDP
  and TestSnellPanelRejectsUnsupportedOptionsBeforeWrites must reject collisions,
  raw runtime owner IDs, wrappers and invalid version/obfs/mode combinations.
- [ ] Add typed hot user adapters and trusted capability. Actual preceding core
  must refuse before mutation; public CRUD/rotation with the new core preserves
  listener, sibling, exact shared Tunnel ledger and disabled/expiry/quota behavior.
- [ ] Verify SQLite/actual isolatedPG credentials upgrade/backup/portable export,
  runtime parents, root Go/static/gen and retained native SSH/mieru checks.
  Expected: named actual PASS, no applicable native SKIP; commit backend deliverable.

Run focused: go test -race -count=1 -v ./internal/database/model ./internal/database
./internal/web/service ./internal/xray github.com/xtls/xray-core/app/clientpolicy/command
-run '^TestSnell|^TestCapabilitiesAdvertiseVerifiedNativeSnell'.

### Task 2: Existing client, service and outbound forms

Files: frontend/src/schemas/{client.ts,primitives/protocol.ts,
primitives/outbound-protocol.ts,forms/inbound-form.ts,forms/outbound-form.ts}; new
protocols/{shared,inbound,outbound}/snell.ts; lib/xray/{inbound-defaults.ts,
inbound-form-adapter.ts,outbound-defaults.ts,outbound-form-adapter.ts}; existing
ClientFormModal/ClientBulkAddModal and protocol registries; new inbound/outbound
snell.tsx forms. Existing all13 translation bundles and generated5 API contracts.
Tests: new snell-forms.test.tsx, snell-form-adapters.test.ts, snell-json-form.test.tsx,
snell-bulk-form.test.tsx alongside existing SSH/mieru/i18n/shared workflow suites.

Consumes Task1 snellPsk, protocol/options metadata and owner rules. Produces
SnellInboundSettingsSchema and SnellOutboundSettingsSchema native wire values
through the existing form adapters; defaults explicitly version4/native stream.

- [ ] Write actual component/adapter RED: independent PSK, omitted preservation,
  version4/5/6 options, strict wrapper/mux rejection, explicit source endpoint,
  one new owner per service/bulk quantity1, attach occupancy and disabled reservation.
- [ ] Implement those existing workflow fields and native DU/default/JSON adapters;
  backend remains authoritative for races. Do not encode implementation internals
  into user flows. Keep ordinary protocol and SSH/mieru behavior intact.
- [ ] Run native forms plus shared account/attach/tunnel/json/i18n checks,
  typecheck/lint/format/Vite and generated-contract checks. Expected: PASS and
  frontend5 generated API files stable on repeat; commit the form deliverable.

### Task 3: Native exports and complete public acceptance

Files: new web/service/snell_export.go, sub/snell.go and native export/runtime tests;
existing sub controllers/service raw/json/clash paths and ClientInfoModal downloads.
New tools/verify-native-snell-panel.py and missing-marker fixture builder;
custom-core CI and native-snell-panel-testing.md. Strengthen the previously
recorded v6 official first-large fixture reply assertion where this gate touches it.

Consumes canonical Task1 credentials/owner and Task2 existing download mappings.
Produces native Surge profile and Custom Xray client JSON only; public formats
snell-surge and snell-json. Unsupported formats fail explicitly. Native text
quoting follows official profile docs and states escaped-value minimum versions.

- [ ] Write export RED for canonical PSK, multiple endpoints, IDN/IPv6, comment/
  comma/quote/backslash/Unicode fidelity, no fake URI, inactive owner/resource,
  repeated endpoints and unsupported host wrappers. Implement native exports.
- [ ] Write and run real public HTTP -> native core SnellTCP/UDP/v5QUIC + shared
  Tunnel tests across4/5/6. Verify exact bytes/billing/rates, live disable/expiry/
  quota/rotation, sibling survival, last-owner removal, released port and restart.
  Use pinned native and actual official-server references; proprietary Surge
  device inbound remains a separate unverified boundary.
- [ ] Required SQLite/actualPG names must PASS without applicable SKIP; preserve
  SSH/mieru native guards, pinned Snell42/officialQUIC and frontend/shared gates.
- [ ] Perform the single whole-panel review and one verified correction pass;
  integrate logical commits, build clean artifacts with preserved old hashes,
  publish only the authorized feature branch and verify exact fork HEAD.
  Update scoped matrix rows and original remaining work; commit final provenance.
