# SSH upstream bridge and runtime integration plan

> Continue native/inline `superpowers:executing-plans`. The user authorizes
> ordinary engineering decisions; this implements Task 5 of the full plan.

**Goal:** Make authored SSH outbounds usable through the existing router,
configuration services, editor, probes and lifecycle, with real traffic evidence.

**Spec:** [Full requirements](requirements.zh-CN.md), especially III, V, VII,
IX and X; [connector contract](ssh-upstream-connector.md).

**Architecture:** Xray selects an outbound by its existing tag and sends the
original TCP destination through a password-authenticated loopback SOCKS5 bridge.
The bridge selects a strictly pinned SSH connector. Authentication and per-client
policy accounting remain at ingress; this egress does not add a billing source.
New connector generations are staged alongside working generations and retired
only after core application succeeds. Rejection rolls back staged resources.

**Stack:** existing Go/Xray/x/crypto, existing settings JSON and Runtime adapter,
existing React/Zod/Ant Design editor; no new binary or dependency.

## Decisions and constraints

- Use one fixed loopback listener, default `127.0.0.1:64901`, with a validated
  `XUI_SSH_UPSTREAM_BRIDGE_PORT` override. A conflict fails visibly; no automatic
  fallback port. This makes preview rendering accurate without opening a socket
  or changing the endpoint when the OS allocates a different ephemeral port.
  The existing AWG bridge uses 64900. No bridge is started without SSH outbounds.
- Rejected alternatives: an ephemeral listener allocated during preview creates
  runtime side effects; shelling out to host sshd/ssh couples lifecycle to external
  keys/processes. The embedded connector already supplies the required data path.
- A process-random secret derives stable per-generation SOCKS credentials using
  HMAC over tag and typed SSH settings. Do not persist or log this secret. Changed
  settings change credentials; unchanged settings retain connections and endpoint.
  Compiled Xray config has bridge credentials, never upstream private keys.
- At most 32 configured SSH outbounds and 512 accepted bridge connections total,
  including unauthenticated handshakes; each connector retains its 128-flow cap.
  SOCKS negotiation deadline 5s, upstream dial deadline 10s. Accept rejects capacity
  immediately. Relay buffers are bounded; standard TCP half-close is retained.
- SOCKS5 requires RFC1929 user/password, only CONNECT, IPv4/IPv6/domain targets.
  Reject no-auth, bad credentials, BIND, UDP ASSOCIATE, malformed addresses and
  reserved/version errors before upstream dialing. Never resolve targets locally
  or fall back directly. Authentication comparison uses constant-time equality.
- Authored shape: `{tag,protocol:"ssh",settings:{address,port,user,privateKey,
  privateKeyPassphrase?,hostKey}}`. Tag is nonempty, at most 128 bytes, no control
  characters or surrounding whitespace. Reject unsupported envelope/settings
  fields instead of silently applying transport/mux/chain options to loopback.
  Duplicate routing tags involving SSH and more than 32 SSH entries are errors.
- Preview uses only parsing/validation and pure credential/config rendering.
  Runtime prepare starts the listener and staged connectors; commit revokes old
  generations, rollback removes only staged ones. Stop/core exit closes bridge
  clients and connectors; restart recreates desired state. Keep a held-back reason
  when desired and applied states diverge. A failed replacement core must restore
  the previous core config and retain its matching bridge generations.
- Structural validation, listener binding and core application failures preserve
  prior applied state. Upstream reachability is not probed during save/preview;
  a syntactically valid but incorrect host pin fails subsequent traffic and route
  probes without retaining access through the superseded pin or a direct fallback.
  Applied configuration alone is not evidence of upstream health.
- Run applicable real Xray/OpenSSH, race, SQLite/PostgreSQL, frontend and build
  checks. Do not equate backend primitive tests with public feature completion.

## Review focus

Pin rotation must revoke changed streams while unrelated streams survive;
rejected configuration and port collision must retain working traffic; preview
must not bind or revoke anything; UDP and invalid auth must never reach an
upstream; a failed core apply must not pair old credentials with new connectors.
Tests below explicitly cover these cases.

## Task 1: Authenticated bridge and staged generations

Create `internal/sshoutbound/outbound.go`, `manager.go`, `socks.go` and tests.
Interfaces: `ParseOutbounds([]json.RawMessage) ([]Outbound,error)` validates
entries and tag uniqueness; `NewManager(port int) (*Manager,error)` creates no
listener; `Manager.Render(Outbound) ([]byte,error)` emits a SOCKS outbound;
`Manager.Prepare([]Outbound) (*Prepared,error)` stages desired generations;
`Prepared.Commit()` and `Prepared.Rollback()` finish exactly once;
`Manager.Close()` stops owned resources and permits a later prepare/restart.

- [x] First real SOCKS test: no-auth/bad-password refused with no upstream accept;
  authenticated request reaches actual SSH wire peer with unchanged target.
  Observe meaningful RED before implementing bridge/parser/manager.
- [x] Actual OpenSSH through the bridge preserves request/response and half-close;
  wrong pin refuses CONNECT without a direct target connection.
- [x] Pin/config rotation, deleted tags, repeated commit/rollback, port conflicts,
  zero-desired shutdown, manager restart and bounded handshake/capacity tests.
  Stage a new generation, prove old traffic survives rollback, then prove commit
  revokes only changed generations. Pure preview must leave the port bindable.
- [x] Focused tests/race and evidence; concrete SOCKS-to-SSH data path, no stubs.

Task 1 is an internal bridge, not yet a panel-selectable outbound. Task 2 below
must supply the fixed-port override and runtime ownership. Commit/push evidence
is recorded with the full task ledger and branch history after verification.

## Task 2: Configuration services and Runtime application

Modify `internal/web/service/xray.go`, `xray_setting.go`; add
`ssh_outbound_runtime.go` and tests; update lifecycle shutdown callsites.
Expose a builder returning compiled config plus desired SSH outbounds; existing
GetXrayConfig remains a preview without SSH listener mutation. Transform after
subscription merge. RestartXray stages/commits/rollbacks under its existing lock,
with previous-core restoration after failed replacement. All mutations remain
behind Runtime. Add validated port override and operator docs.

- [x] Save/preview real typed SSH config, reject unsupported transport, duplicate
  tags, invalid pins/keys without leaked secrets or database/runtime changes.
- [x] Real Xray selects two observable exits and blocks by rule priority; bridge
  stop, wrong pin and dead upstream fail closed. Confirm expected TCP payload,
  original target and actual upstream traversal. Prove preview does not start it.
- [x] Real running core/config rollback, port conflict, stop/restart and core crash;
  verify unrelated existing flows across key edits and retained applied state.
- [x] Existing managed SSH ingress through upstream: exact raw/billed counts,
  quota rejection/restart and live aggregate shaping. Both database backends.
- [x] Appropriate service/runtime regression, static/build checks and commit.

The runtime increment requires a configured core API for startup verification;
it explicitly refuses API-less SSH activation/removal without enabling an API
behind the administrator's back. Preview alone does not impose that capability on
a later native-only configuration. See [runtime boundaries](ssh-upstream-runtime.md).
Whole-task completion still requires Task 3 and all remaining protocol work.

## Task 3: Existing editor, probes and distribution

Modify existing outbound Zod registry/form adapter/protocol form, API descriptions
as needed, and EN/ZH/all fallback locales. SSH controls expose only real settings;
mask private key/passphrase, retain strict host pin. Add the existing route-probe
path through managed/temporary bridge, not merely upstream TCP reachability.
Keep settings in existing admin-authorized config backup/restore and node paths;
node capability rejection must be explicit until its runtime supports the bridge.

- [ ] Browser creates/edits SSH outbound, saves/loads it, routes an actual request;
  meaningful schema/form tests ensure unsupported controls cannot be emitted.
- [ ] Actual HTTP route probe via SSH, wrong pin rejection, secrets protected in
  logs and non-admin surfaces; backup/restore and supported node application.
- [ ] Regenerate contracts/docs when interfaces change; frontend suite/build,
  backend affected suites, race and packaging checks; update matrix/operations,
  commit/push and independently verify remote SHA.

Other protocols, full multi-node policy execution, deployment/upgrade safeguards
and all remaining A–E acceptance remain under the unchanged whole-task goal.
