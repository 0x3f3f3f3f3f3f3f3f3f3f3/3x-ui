# Unified client policy and backend implementation plan

> For agentic workers: use superpowers:executing-plans inline. Keep this plan
> and validation evidence current; use a fresh whole-branch reviewer at the end.

**Goal:** Deliver every item in requirements.zh-CN.md with runnable integration,
real data-path acceptance evidence and verified feature-branch pushes.

**Architecture:** Existing ClientRecord/Runtime management with immutable policy
identity, exact accounting and shared data-path shaping. Authenticated backend
adapters preserve destination and identity into existing Xray routing.

**Tech stack:** Go 1.27.1, Gin/GORM, SQLite/PostgreSQL, React/Ant Design/Node 26,
pinned Xray and protocol backends.

**Spec:** [design.md](design.md), [semantics.md](semantics.md), and the complete
[user requirements](requirements.zh-CN.md) take precedence over this plan.

## Global constraints

- Preserve baseline 17d7dd46b512d0a9c22921a6094f30c672e436c9 and all existing features.
- Work on feat/unified-client-policy-backends; conventional logical commits;
  push only to the supplied fork; verify each remote SHA; no forced updates.
- Follow repository layering, code generation, i18n and two-line comment rules.
- Per-client aggregate rates use raw bytes/s, 0 unlimited; live updates ≤2s.
- Multiplier default 1×, positive [0.001,1000], precision 0.001; integer units.
- Missing required capability fails protected; no direct/unlimited fallback.
- Real clients and privileged tests cannot be replaced by mocks or skips.
- Never modify host management networking or deploy/reboot production.

## Review focus

1. Same email recreated after deletion must not inherit old source reports.
2. Late pre-change counters must not use a new multiplier; test policy epochs.
3. Disable/expiry/quota races with renewal must not re-enable another reason.
4. Hidden fast paths (Vision splice, UDP, backend egress) must not bypass policy.
5. Snapshot hierarchy/restores must not bill a source twice or issue extra credit.

## Task 1: Audit and reproducible baseline

Files: this directory; read existing models, Runtime, services, jobs, build/CI.

- [x] Preserve full requirement file; clone, inspect clean status and instructions.
- [x] Verify upstream release and exact newer fork baseline without downgrade.
- [x] Source-derived capability matrix and architecture alternatives/data paths.
- [x] Identify credential locally and verify SSH with pinned official host keys.
- [x] Execute baseline Go/frontend/build checks and record failures/skips.
- [ ] Probe official backend binaries, licenses and isolated network capabilities.
- [x] Commit audit and push branch; verify remote SHA (3e226aea includes audit).

The official Snell artifact audit and [isolated Linux networking probe](linux-network-prerequisites.md)
now provide scoped prerequisite evidence. nft TPROXY preserves real TCP/UDP
targets on IPv4/IPv6 and its tested batch/cleanup operations retain unrelated
fixture resources. Real Snell interoperability, production firewall coexistence
and full backend deployment remain open.

## Task 2: Exact accounting arithmetic

Files: new `internal/clientpolicy/accounting.go` and `accounting_test.go`.
Produces `ParseMultiplier(string) (Multiplier,error)`, `Multiplier.String()`,
`Charge(raw int64, multiplier Multiplier, remainder int64) (whole,carry int64,err error)`,
`RawAllowance(remaining, remainder int64, multiplier Multiplier) (int64,error)`.
Consumes the fixed-point contract in semantics.md; no DB or clock dependency.

- [x] Write failing tests for 0.5/1/1.5/2/10, three-decimal precision, invalid
  zero/negative/NaN/Infinity/exponents, and exact round-trip decimal output.
- [x] Test one-byte batches versus unsplit 1,000,001-byte usage, carried
  fractions across 1×→2×, and 10 GiB + 5 GiB segment example (20 GiB billed).
- [x] Test int64 extremes and intermediate product overflow; rejected input
  must return an exact typed sentinel and no usable partial charge.
- [x] Test quota allowance for fractional remaining bytes and all multipliers.
- [x] Run `go test ./internal/clientpolicy` and observe missing behavior RED.
- [x] Implement checked quotient/remainder operations without floating point.
- [x] Run package test, race and fuzz/property boundary checks.
- [x] Push after explicit approval; remote SHA matches 3e226aeaca84392dd3b534b1c341baa955cdbd0f.

## Task 3: Durable accounting and identity

Files: `internal/database/model/client_policy.go`, `database/db.go`,
`database/migrate_data.go`, `web/service/client_policy.go` and their tests;
extend model/model.go conversions, portable exports and API generator.
Consumes Task 2; produces stable UUID policy identity and atomic ledger API.

Identity substep pushed as f2d23a46: internal create-only ClientRecord.PolicyID,
legacy backfill, new-client generation; SQLite and PostgreSQL checks pass.
The internal ledger now atomically persists usage, fractional billing, source
cursors and revisions, with raw totals projected into client_traffics. Its
activation seeds existing local usage at 1×. Runtime/API activation and global
node-history migration remain pending; existing collectors still use their
old path and cannot yet enable this ledger from the panel.

- [ ] Write SQLite/Postgres migration tests retaining old raw usage at 1×.
- [x] Implement transactional raw/billed/remainder/cursor/revision updates.
- [ ] Red→green replay/out-of-order/concurrent/reset/restart/deletion/restore tests.
- [ ] Integrate existing local and remote traffic paths exactly once per source.
- [ ] Implement multiplier boundary settlement before accepting a policy change.
- [ ] Carry new fields through all CRUD/bulk/import/export/backup/node paths.
- [ ] Verify DB backends and recovery; commit/push with evidence.

Internal ledger boundaries are verified, but the unchecked multiplier item
also requires stopping/draining live producers and resuming the new revision.
The ledger rejects incomplete boundaries and untracked legacy writes; that is
a protection against incomplete integration, not a completed runtime adapter.

Existing panel reset, bulk reset and automatic-renewal services now recognize
admission-ledger ownership. They reset accounts and raw projections together,
retire old sources, preserve operator disable/expiry and avoid raw-byte quota
auto-disable for these accounts. An unsettled observed source rejects the
whole reset transaction. This prerequisite was pulled forward from the SSH
vertical because the old jobs would otherwise undo its policy semantics.
Public activation and collection of native/remote usage remain open.

## Task 4: Shared live rate and quota data path

Files: `internal/clientpolicy` engine/stream/datagram files and E2E tests;
core dispatcher adapter plus Runtime apply/reconcile path.
Consumes immutable policy/ledger; produces cancellable per-client flow budget.

The stream scheduler substep is implemented with bounded FIFO waits, separate
instances per client/direction, live updates and a stream writer. Real loopback
TCP tests cover two client groups, four connections each, three rate settings,
an unlimited baseline and live decreases/increases. The shared flow controller
now attaches this to durable admission and real bidirectional TCP forwarding,
with quota/state cutoff, source fencing and restart persistence. Protocol
integration, public controls, UDP and distributed enforcement remain open.

- [ ] Test two rates + unlimited, >1 connection, same-IP separate clients,
  bidirectional transfer, live changes, cancellation and bounded buffering.
- [ ] Implement shared token scheduling, durable quota reservation and reasons.
- [ ] Integrate actual Xray authenticated dispatcher; test Vision/splice/mux/UDP.
- [ ] Reconcile observed capability/revision and protect failed-policy services.
- [ ] Allocate global rate shares/byte credits with bounded node leases.
- [ ] Prove existing-flow cutoff and restart persistence; commit/push.

## Task 5: SSH vertical integration

Files: new `internal/sshtunnel`, existing protocol model/Runtime/service,
frontend protocol schemas/forms, translations, sub/export, deployment.

- [x] Real OpenSSH -L/-D/-R tests first; independent keys and channels.
- [x] Dedicated server; deny shell/exec/PTY/SFTP/agent and default -R.
- [ ] Target/listener restrictions; strict SSH upstream host key verification.
- [ ] Unified routed outbound bridge, counters/policy/live cutoff/health.
- [ ] Complete UI/API/backup/node/export/install; test faults; commit/push.

The internal SSH server now implements authenticated TCP forwarding, bounded
authorized reverse listeners, live key revocation and shared policy flows.
Real OpenSSH verifies duplex shaping, quota at four multipliers and server/
controller restart. Existing panel creation/edit/reset services now drive a
production manager; real OpenSSH → panel-managed Xray tests run on SQLite and
PostgreSQL. Public policy API, persisted runtime controls and the existing-client policy tab
are verified. Client-list billing integration is also verified: exact list/info
balances, SQL filters/sorting/summary and depleted cleanup candidate selection
on SQLite/PostgreSQL, plus a real browser and SSH/Xray quota-reduction check.
Depleted cleanup now rechecks immutable identity and eligibility under canonical
and traffic row locks, commits membership and data removal together, and applies
runtime updates after commit. Concurrent reset, quota increase, identity
replacement, rollback and unrelated-edit isolation have SQLite/PostgreSQL
regressions. Creation/bulk policy, node dashboard and other usage consumers
remain open.
The concrete private SOCKS bridge now carries authenticated identity, inbound
tag, transport source and original destination into real Xray. Tests verify
user/domain/IP/port/source routing, two observable exits, block priority and
no duplicate billing. Runtime now handles canonical credentials, delayed expiry,
selective revocation, reset recovery, listener failures and router process exit.
Membership/rename isolation, remaining management UI and multi-node
wiring remain open; narrower upstream Runtime evidence is recorded below.
Standard -R does not reveal the client-side target to the server; listener ACLs
do not claim to enforce that target. See semantics.md for the open constraint.

The [SSH upstream connector increment](ssh-upstream-connector.md) now implements
a bounded authenticated TCP backend with strict host-key selection, cancellation,
half-close and active-operation deadlines. Real isolated OpenSSH tests observe
payloads at IPv4/domain and IPv6 targets; wire-peer tests cover capacity,
encrypted keys and failed/stalled transport cleanup. The authenticated bridge
now has a [configuration and Runtime caller](ssh-upstream-runtime.md), with
installed-core validation, staged application, failure recovery and exit cleanup.
Real double-SSH tests cover selected exits, blocking, no fallback, shared rates,
quota mutations and single billing. The existing probe service now verifies real
SSH HTTP routes through an isolated bridge, preserving applied flows. The dedicated
outbound editor now passes real browser create/edit/reload/probe and wrong-pin
traffic checks. Anonymous and restricted API scopes cannot access its secrets;
SQLite and PostgreSQL native backup restoration recover the actual SSH route
after panel restart. Node distribution, deployment acceptance and remaining public
health/status integration still belong to this same Task 5. API-less SSH upstream
activation is explicitly unsupported.

The [runtime observability increment](ssh-runtime-observability.md) now exposes
local SSH listener/protected state and authenticated transport counts through
an owner-filtered, read-only API. Actual SQLite/PostgreSQL lifecycle and HTTP
authorization tests cover the backend. The existing desktop/mobile inbound list
now displays listener state and authenticated transport counts. Real browser
acceptance covers owned-port collision/recovery, counts 1/2/1 and UI disable.
The [client presence increment](ssh-client-presence.md) connects admitted local
SSH transports to existing online, last-online, active-inbound and source-IP
views. Real idle OpenSSH, SQLite/PostgreSQL lifecycle and collector tests verify
identity and cleanup; Chromium verifies Online before payload and real aging
after disconnect. Node, global policy, IP/device-limit execution and deployment
remain open. SSH source observation bypasses native host-wide fail2ban bans.

## Task 6: mieru vertical integration

Files: new `internal/mieru`, protocol registry/model/Runtime and existing UI.
Public integration follows the [ordered implementation plan](mieru-integration-plan.md).

- [x] Pin v3.38.0 API/config and official client/server; test TCP and UDP.
- [ ] Native multiuser identity preserved into policy-aware dispatch/routing.
- [ ] Disable conflicting rolling native quotas; single panel billing owner.
- [ ] Complete CRUD/API/UI/export/lifecycle/logs/deployment/node/recovery paths.
- [ ] Run real-client route/rate/quota/auth/failure matrix; commit/push.

The [native data-path increment](mieru-data-path.md) embeds the official wire
engine and carries authenticated TCP/UDP into the existing shared policy
controller. Actual official-client tests exercise all four underlay/payload
combinations, same-IP identities, fixed-point charging, quota cutoff, rates,
partial startup and backpressure cleanup. These are internal adapter results.
The required dial callback preserves routing context. Tests now also connect
official clients through the private managed Xray bridge; the earlier direct
connector tests alone were not evidence of core routing.

The [core datagram prerequisite](xray-datagram-bridge.md) now supplies a pinned,
independently built source patch with real empty/maximum UDP payload and finite
queue tests. The private policy-ID TCP/UDP bridge now has authenticated health,
actual direct UDP peer metadata, real route/exit tests and official mieru client
single-billing/revocation evidence. Public model/Runtime activation and the full
outbound/platform/failure matrix are separate acceptance scopes: local public
activation now has actual TCP/UDP data-path evidence, while the full matrix
remains open before the routing item is done.

Native credential hot replacement now uses separate authentication generations
over the official hashed-password interface. It preserves unchanged users,
retires existing and delayed sessions of changed users, cancels pending target
dials and releases their idle TCP underlays. Actual native tests cover both
underlays and policy-ID reassignment without inherited accounting. The maintained
native protocol extension now bounds pre-allocation session admission and native
payload queues, retires session metadata with its workers and disables retained
per-generation diagnostic groups. Official-client overload/recovery tests and
the complete copied upstream suite exercise the change. Local public Runtime,
canonical CRUD/bulk/portable restore, API/UI status, and native exports now have
scoped evidence in validation.md. Public shared-rate performance, node policy,
deployment, and the remaining acceptance matrix are still required.

Public actual UDP payload rates now have scoped receiver-side evidence for
both databases and both native underlays. Concurrent durable admission is
batched without releasing payload before commit; bounded queues, rollback,
cancellation and exact billing have targeted coverage. This does not close the
full public-rate item: unrestricted buffering, natural client-exit cleanup and
the earlier unexplained short stream-rate window remain open. See validation.md
for the unchanged acceptance bounds, failed diagnostics and mutation results.

Native session completion now cancels adapter policy and first-use waits.
Managed UDP idle maintenance also runs during quiet/invalid-packet reads and
avoids waiting on expired graceful-close queues. Real official-client socket
loss reclaimed both payload associations within the predeclared 67s idle bound,
with stable usage and listener reuse. Linux physical TCP FIN/RST detection now
also observes kernel shutdown while full native queues block the wire reader.
Official-client TCP/UDP payload tests retain the two-second cleanup bound and
verify unrelated-user continuity, stable accounting and fresh admission. FIN
network-delivery delay, non-Linux blocked-read detection and unrestricted public
senders remain outside this scoped result.

Public IPv6 loopback acceptance now covers both databases and native transports,
actual TCP/UDP targets, preserved source and UDP reply peer, exact independent
billing, disable isolation and core restart. The production path required no
change; a source-loss mutation demonstrates that the new route test fails if
the bridge replaces the original IPv6 source. External IPv6/export coverage and
the remaining public routing and fault matrix stay open.

Public preview/preflight acceptance also has both-database/both-underlay
evidence: saved previews do not replace the runtime, invalid routing is rejected
before replacement, existing and fresh flows retain the working configuration,
and restoring the template preserves exact billing. Destructive-preview and
skipped-validation mutations fail. Post-stop rollback remains a separate
acceptance requirement; actual panel-process recovery is recorded below.

Public real routing now has 80 route-case observations across both databases,
both native transports and both payload types: separate same-IP user exits,
domain/IP/source/inbound/network/port matching, deny priority and round-robin
balancing. Independent target source addresses identify the selected outlet;
blocked cases also require a healthy allowed control. Other outbound types,
established-flow route migration and the remaining failure matrix stay open.

Actual Linux panel-process recovery now has eight real-executable/API/client
cases across both databases, both native transports and SIGTERM/SIGKILL.
The original SIGKILL case exposed an orphaned core listener. A dedicated locked
creator thread plus Linux parent-death signaling now ties the core to the panel
without killing it when the API caller's thread exits. Exact saved policy/usage,
depleted-user denial, healthy-user recovery and bounded port reuse are verified.
This does not close the remaining buffering, node, packaging or full fault matrix.

## Task 7: Snell v4/v5/v6 vertical integration

Files: new `internal/snell`, backend asset manifest/installer, namespace bridge,
protocol registry/model/Runtime/UI and export paths.

- [ ] Pin 4.1.1, 5.0.1, 6.0.0rc2 beta and matching real Surge combinations.
- [ ] Verify asset provenance/hash/architecture/license; no proprietary assets in Git.
- [ ] Per-client process/listener/egress isolation with actual target preservation.
- [ ] TCP, UDP-over-TCP, version-specific QUIC mode; no blanket template.
- [ ] Implement full client/policy/routing/lifecycle/node/deployment/export paths.
- [ ] Real Surge interoperability per target; record unavailable tests, never pass.
- [ ] Failure/cleanup/cost evidence; commit/push.

## Task 8: First-class port forwarding and remaining existing backends

Files: new `internal/portforward`, existing inlet selection/client flow,
port_conflict.go, Runtime, routing bridge; TUIC/MTProto/WG/AWG adapters.

Verified prerequisite: [AWG forwarded-port reservations](awg-port-reservations.md)
covers template/fixed listeners, peer ownership, complete candidate edits and
concurrent public mutations. Forwarded payload policy and first-class forwarding
remain separate open work.

- [ ] Evaluate nft/iptables family, permissions and offload in isolated namespace.
- [ ] Exclusive node+network+address+port client binding, TCP/UDP/combined.
- [ ] Multi-rule shared policy, source ACLs, IPv4/IPv6, DNS refresh behavior.
- [ ] Validate conntrack/return path/established revocation for any kernel mode.
- [ ] Replace TUIC aggregate-only meter with authenticated per-user path.
- [ ] Prove MTProto, WireGuard, AWG, HTTP/SOCKS, TUN/tunnel applicable paths.
- [ ] Full management/export/node/backup integration and real-client tests; push.

## Task 9: Cross-cutting delivery and full acceptance

Delivery progress (2026-09-29): managed bundle assembly, full-source manifests,
bounded fork downloads, candidate managed-core preflight and verified installed
menu/web update entry points have separate evidence. The standalone installer
now uses that preflight before package/service changes and has an isolated first
SQLite installation plus installed-panel HTTP check. Menu installation/refresh/
legacy routes, independent core updates, transactional activation/DB rollback,
real service-manager/container/platform execution and final acceptance remain
open; the broader delivery checkboxes below are intentionally not complete.

- [ ] Audit every matrix row again for every protocol, including groups/LDAP,
  HWID, subscription host overrides, notifications and first-use expiry.
- [ ] Fork-safe install/update; reproducible images/packages; non-Linux build gates.
- [ ] Tested backup/restore, upgrade/rollback and only-owned-resource uninstall.
- [ ] `make verify`, `make race`, live PostgreSQL, parser fuzz and vulncheck.
- [ ] Full request A–E data-plane acceptance with independent observations,
  fixed tolerance, real versions/commands/durations; no skip counted as pass.
- [ ] Fresh whole-branch review; fix material findings with RED→GREEN tests.
- [ ] Requirement-by-requirement completion audit; final remote SHA verification.
- [ ] Final report with versions, complete/incomplete/inapplicable/unverified,
  commits, measured results, operations and remaining risks.

### SSH creation/export increment

The existing inbound form now accepts SSH, and the existing client form edits
independent public keys, explicit target permissions and default-off reverse
listeners. Local SSH-only attachment selection follows the current enforcing
backend capability. Non-applicable transport/credential fields and unavailable
SSH IP/device-limit controls are hidden, with the missing limits stated.
Information and QR/export dialogs produce OpenSSH configuration plus actual
server public-key known_hosts data. EN/ZH text and all other locale fallbacks,
generated API contracts and documentation schemas accompany the change.

The browser fixture now creates the inbound and client through their real
forms, downloads both export files and consumes them with actual OpenSSH. It
also tests host-key mismatch, billed quota reduction and strict denial. Initial
creation follows the existing 30-second pending-config scheduler; the separate
2-second live multiplier meter check is retained. The real browser path has passed;
full regression and static validation are recorded in validation.md.
This increment does not complete SSH upstream, online/IP/device enforcement,
bulk/portable export, remote nodes, deployment or the other backend requirements.

### Portable restore increment

Ruling: restore each imported client and every requested attachment in one
transaction, before any Runtime apply. Existing bulk-create intentionally reuses
identities and commits separate inbound batches; it cannot provide import's
documented skip-existing and restore-before-admission guarantees. Reuse its
inbound preparation, persistence and runtime operations by separating those
phases, rather than creating temporarily enabled or partially restored clients.
The cost is per-client import transactions instead of inbound batch fanout.

- [x] RED: injected traffic failure leaves no client, traffic or attachment;
  failure on a later inbound rolls back earlier bindings; existing matching
  email/subId is skipped without metadata changes; runtime sees restored usage.
- [x] Split `AddInboundClient` into preparation, transactional persistence and
  post-commit apply; preserve its ordinary add behavior and existing tests.
- [x] Import under sorted inbound locks, validate each item, check duplicate
  identity again inside the writer, restore usage/HWID/group baselines before
  commit, and retain attached-before-orphan duplicate precedence.
- [x] Restore managed policy and exact charged history with fresh identity and
  meter lifetimes; reject unsupported attachment combinations and malformed
  values. Export a consistent snapshot including SSH credentials and policy.
- [x] Verify SQLite/PostgreSQL, rollback/concurrency, real SSH quota admission,
  generated API contracts, regression and static checks; document results.

Portable policy format decision: add optional `ClientPortablePolicy` as `policy`
on the existing payload, with formatVersion=1, local up/down B/s, multiplier,
exact billed bytes, remainder and traffic projection quota/enable/expiry.
Client totalGB and traffic up/down serialize as decimal strings; import accepts
the old integer form too. Missing policy keeps legacy 1x semantics. Snapshot
restore is accepted only by the import API; ordinary create/bulk-create reject
the field. Export runs in a read-only repeatable snapshot and rejects inconsistent
owned ledgers instead of silently exporting contradictory counters.

- [x] RED/GREEN: real segmented 0.5x→2x ledger round trip retains 7 billed bytes
  and remainder 500 for raw 3/3; restored narrow quota still denies new bytes.
- [x] RED/GREEN: browser-style JSON decode/re-encode preserves byte values above
  2^53; unknown format, invalid rate/multiplier/carry/bytes and unsupported
  attachments leave no client or binding. Existing identities remain untouched.
- [x] RED/GREEN: import uses fresh policy/meter incarnations and preserves
  manual disable, expiry and retained-traffic ownership. Verify a snapshot taken
  during concurrent accounting is internally consistent on SQLite/PostgreSQL.
- [x] Exercise actual SSH export/delete/import/denial and restart; generate API
  contracts/docs and run regression/static/build gates.
