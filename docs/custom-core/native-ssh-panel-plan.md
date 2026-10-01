# Native SSH panel implementation plan

> Execute inline with superpowers:executing-plans and test-driven-development.

**Goal:** Complete usable SSH panel accounts, services, outbounds and OpenSSH
exports through existing workflows and the merged native core.

**Architecture:** Canonical SQL credentials and dedicated persisted business
host keys compile into native SSH users; private controls preserve existing
identity, policy, statistics and listener lifecycle.

**Tech Stack:** Go1.27.1, pinned x/crypto SSH, SQLite/PostgreSQL, React/AntD/Zod.

**Spec:** [native-ssh-panel-design.md](native-ssh-panel-design.md).

## Global constraints

- One Custom Xray business process; no sshd, OS users or panel-side bridge.
- Never access/reuse/change Git private keys or host management SSH.
- Separate sshUsername/sshAuthorizedKeys/sshPassword from other credentials.
- Native limits: username256bytes; password1024bytes;16keys;16384bytes/key.
- Host keys: database-owned Ed25519; UUID references; private0600 materialization.
- Validate omitted/cleared credentials inside the serialized SQL writer.
- Password/reverse forwarding off by default; strict outbound host pin required.
- Publish verified logical commits only on feature/custom-xray-unified-policy.
- One final whole-panel review; one correction pass; no per-task review seats.

## Review focus

- Concurrent omitted updates must not restore revoked authentication.
- Duplicate usernames remain reserved by disabled linked identities.
- Restore/file recovery preserves host trust; missing SQL keys cannot rotate it.
- Empty listeners/removals reject missing capability before preparation.
- Key export, reverse permissions and unknown targets must stay truthful.

### Task 1: Canonical SSH accounts and business host-key lifecycle

Files: internal/database/model/{model.go,native_ssh_host_key.go}; database
{db.go,migrate_data.go}; web/service/{client_crud.go,client_link.go,
client_inbound_apply.go,client_policy_config.go,inbound.go,xray.go}; new
service/{ssh_accounts.go,ssh_host_key.go}; xray/{api.go,api_managed.go,hot_diff.go};
core/xray/app/clientpolicy/command/command.go; new SSH account/migration/runtime
and managed-capability tests alongside the corresponding mieru tests.

Interfaces: keep existing ClientService and InboundService APIs; introduce
prepareSSHInbound(*model.Inbound) error, resolveSSHInboundCredentials(*gorm.DB,
*model.Inbound) error and bindManagedSSHIdentity native projection. Provision
dedicated host-key records/files through current Runtime preparation after
capability checks. No public standalone user-management endpoint is needed.

- [x] Write behavioral RED tests for independent SQL credentials, canonical
  ownership, omitted/explicit-clear updates, duplicates and invalid public keys.
- [x] Persist/validate native fields inside existing CRUD/attach/bulk transactions;
  pass the named account tests without changing other protocol credentials.
- [x] Write host-key first-use/restart/file-loss/backup/missing-reference and
  private-path RED tests; implement SQL authority and guarded materialization.
- [x] Write native user/hot-diff/missing-marker RED tests; add typed accounts,
  capabilities and preparation fences; verify current/markerless actual cores.
- [x] Run SQLite/actualPG migration/restore and real public service lifecycle;
  affected race, generation/lint/vet and logical backend commit.

### Task 2: Existing native SSH forms and options

Files: frontend/src/schemas/{client.ts,primitives/protocol.ts,
primitives/outbound-protocol.ts,protocols/inbound/index.ts,
protocols/outbound/index.ts}; new protocol SSH schemas/forms; lib/xray
{inbound-defaults.ts,inbound-form-adapter.ts,outbound-defaults.ts,
outbound-form-adapter.ts}; existing ClientFormModal/ClientBulkAddModal,
InboundFormModal/OutboundFormModal; EN/ZH translations and generated contracts.

Consumes Task1 SQL/client/options contracts; produces unchanged existing API
payload envelopes with SSH-native settings and client authentication fields.

- [ ] Write real component/adapter RED tests for create/edit/attach/bulk, native
  credential limits, explicit clear controls, reverse controls and host trust.
- [ ] Implement native fields/defaults/validation, real outbound pin and key-path
  controls; reject UDP/security/global-mux wrappers rather than losing settings.
- [ ] Verify reopen/clone/rename and unrelated forms; affected frontend tests,
  typecheck/lint/generation/build; logical UI commit.

### Task 3: OpenSSH export and combined public acceptance

Files: existing internal/sub services/controllers; new SSH export service/tests;
existing ClientInfoModal and link/export helpers; actual-child public HTTP SSH
lifecycle tests; tools/verify-native-ssh-panel.py and custom-core CI; testing docs.

Consumes persisted service public host key and canonical account, producing
OpenSSH config/instructions and known_hosts text through existing downloads.

- [ ] RED tests for host/IPv6/Unicode-safe quoting, strict host pin, user-owned
  private-key instructions, enabled reverse controls and explicit format errors.
- [ ] Implement exports; verify existing download/copy controls and reimport
  only where a native representation exists; no invented share protocol.
- [ ] RED combined API → real core/OpenSSH -L/-D/controlled -R plus Tunnel
  lifecycle tests; implement missing glue and verify exact shared usage,
  policy restrictions, rotation, port release, restart and restored host key.
- [ ] Complete required SQLite/PG/core/frontend gates, one whole-panel review
  and one correction pass; integrate, publish clean artifacts and verify fork
  HEAD. Update only verified matrix rows; other project requirements stay open.
