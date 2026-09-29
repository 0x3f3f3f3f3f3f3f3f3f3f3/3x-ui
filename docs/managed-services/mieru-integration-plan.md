# Public mieru integration plan

> Execute inline under Task 6 of [plan.md](plan.md). The full original
> [requirements](requirements.zh-CN.md) remain the completion contract.

## Architecture and decisions

Expose the existing embedded native mieru adapter through the canonical inbound,
client, Runtime and policy APIs. Its authenticated TCP/UDP bridge enters the
existing Xray routing configuration. Native rolling quotas remain disabled;
the durable panel ledger owns admission and charging. Credentials are canonical
client passwords with the existing email as external username. Inbound settings
select TCP, UDP or both underlays; they never pretend to be Xray protocols.

SSH and mieru must acquire leases on one actual `policyflow.Controller` per
database handle, using the existing `local/managed-services` accounting source.
Separate controllers with matching source names would fence each other's meters
and provide independent rate budgets. A reference-counted lease keeps surviving
protocols active when another manager stops. Last release closes the controller;
reacquisition claims a fresh source epoch without resetting durable usage.

The imported Xray Go module predates the private managed Trojan extension and
uses a byte-sized JSON user level. Hot installation therefore needs an explicit
serializer that preserves uint32 levels and the pinned protobuf managed flag.
Only private generated bridges may use this path, through `runtime.Runtime`.
The authenticated capability handshake must still reject stock cores.

The pinned official `appctl` package provides both protobuf `mieru://` and
human-readable `mierus://` encoders/decoders. Raw subscription/share output will
use `ClientProfileToMultiURLs` and verify it with the official parser, preserving
native TCP/UDP port bindings and canonical credentials. Native JSON export will
use the official client profile/config shape, keep the local proxy on loopback,
and omit server-side rolling quotas and private routing bridge ports. Existing
host address/port overrides remain applicable; Xray TLS fields do not apply.
Unsupported client representations must stay explicit rather than emit fabricated
Xray nodes. This is part of the existing export/subscription scope.

These are ordinary engineering choices within the user's autonomous execution
instruction. They do not change the specified limits, units or acceptance scope.

## Ordered implementation and evidence

- [x] Shared ownership: add `internal/web/service/managed_policy.go` with
  `acquireManagedPolicy(*gorm.DB) *managedPolicyLease` and idempotent `Close()`;
  migrate `ssh_runtime.go` to acquire/release it. Test actual combined flow
  admission, byte charging, aggregate stream shaping, concurrent owner release,
  peer survival, final shutdown and fresh acquisition. Observe RED before code;
  use a per-owner-controller mutation to prove the aggregate test catches the
  original design error. No public mieru completion claim from this step.
- [x] Hot core bridge: extend the private bridge/API serializer and exercise
  real gRPC insertion into the pinned running core, TCP/UDP routing, capability
  health, original client continuity and stock-core protection. Preserve the
  existing ordinary inbound path. Record actual RED/GREEN results.
- [x] Canonical model and service: add the protocol and validated transport
  settings; use canonical email/password credentials, stable policy IDs and
  billing ownership. Add migrations for any new persisted fields. Cover
  create/edit/attach/detach/delete, bulk operations, invalid credentials, port
  conflicts, portable backup/restore and SQLite/PostgreSQL transactions.
- [ ] Runtime lifecycle: integrate compilation, apply, client hot replacement,
  disable/re-enable, listener health, first use, presence, usage, core failure
  and recovery. Route every mutation through Runtime, including node transport.
  Use shared controller leases; stopping one protocol must preserve the other.
- [x] Public API and UI: reuse inbound creation, client editing, policy and
  status views; validate matching units and limits; generate API schemas and
  documentation and add all required locale keys. Run real browser CRUD and
  policy changes against actual official mieru clients.
- [ ] Public shared-rate acceptance: extend the existing production SSH duplex
  harness through the same canonical creation, attachment, `UpdatePolicy` and
  Xray lifecycle with one SSH plus two mieru listeners. Use two clients sharing
  loopback, two channels per listener/client, real OpenSSH and official mieru
  TCP and UDP underlays. Retain its unlimited baseline (>8x 128 KiB/s), asymmetric
  64/128 KiB/s policies, live 32/64 and 128/128 KiB/s changes, 1.5s measurement
  windows, 80% lower bound and 106% + configured 100ms burst upper bound. Confirm
  existing streams survive rate changes within 2s, independent client budgets,
  exactly 2x payload billing, and saved policy after core restart. Run both DB
  dialects. This is stream payload performance; sustained native UDP payload
  and the remaining route/quota/fault matrix stay separate required checks.
- [x] Native configuration export/subscription: emit the official client
  profile format, cover host overrides and supported formats, reject unsupported
  representations clearly. Verify exported profiles with the official client.
- [ ] Deployment and acceptance: embedded backend provenance, managed core
  capability requirements, fork-safe package/install/upgrade, logs, restore and
  node recovery. Execute the remaining real route/rate/quota/auth/failure matrix
  for both underlays and both payload types, IPv4/IPv6 and supported DBs.
- [ ] Run applicable complete backend/frontend/static/build checks, record skips
  separately, commit logical milestones, push the approved feature branch and
  independently verify its remote SHA. Keep unexecuted acceptance items open.

Review especially partial apply rollback, a client attached to both protocols,
credential rotation while throttled, last-owner release racing acquisition,
database replacement, duplicate metering across the internal bridge and node
policy scope. The later whole-branch review is still required by the main plan.

Mihomo v1.19.30 supports native mieru TCP and UDP underlays with username/password
([configuration](https://wiki.metacubex.one/en/config/proxies/mieru/),
[pinned adapter](https://github.com/MetaCubeX/mihomo/blob/v1.19.30/adapter/outbound/mieru.go)).
The Mihomo subscription therefore receives native nodes, with separate TCP/UDP
choices when the server enables both. Legacy Clash and Xray JSON cannot represent
this protocol and must not emit a substitute direct-only profile. Real official
Mihomo validation and data-path checks accompany the official mieru client tests.
