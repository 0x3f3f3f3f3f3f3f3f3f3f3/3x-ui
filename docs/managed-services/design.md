# Architecture and actual/proposed paths

Status: identity, ledger and shared TCP flow controller implemented; protocol
and panel Runtime integration pending.
The binding scope is [the complete request](requirements.zh-CN.md).

## Choices evaluated

1. Put arbitrary protocol names into Xray config: rejected; the core does not
   implement those protocols and a generated JSON object is no backend.
2. Shape each listener/IP externally: useful only for exclusive ownership;
   cannot classify authenticated clients sharing a listener/public address.
3. Reuse panel management with authenticated backend adapters and one policy
   authority per immutable client: selected. Use core extension points for
   Xray, embedded handlers where feasible, isolated processes otherwise.

## Identity and control plane

Retain `ClientRecord`, `ClientInbound`, `InboundService`, `ClientService` and
`runtime.Runtime`. Introduce immutable client policy UUIDs (new UUID after
deletion/recreation), policy revision, independent restriction reasons, rate
units and fixed-point multiplier. Inbound/credential bindings map to that
UUID. Email remains a user-facing label, not a durable accounting identity.

The DB transaction owns cumulative raw totals, billed bytes plus fractional
remainder, per-source cursor/epoch and policy revision. A durable report is
idempotent by owner/client/source/incarnation/sequence; no in-memory-only
cursor is sufficient. Out-of-order, stale and conflicting reports fail safely.
Rate changes and multiplier changes serialize with the old revision's final
counter snapshot; no retroactive reprice. Readers expose raw and billed totals.

The implemented ledger adds `client_usage_accounts` and `client_usage_meters`
to the existing database migration/backup model lists. It is tied to the
canonical `clients.policy_id`; `client_traffics.policy_id` prevents stale
statistics from being adopted by a recreated label. One transaction writes
raw/billed/remainder totals, the cumulative cursor and the existing raw traffic
projection. A conditional projection write rejects concurrent legacy writers.
The managed SSH service now uses admission accounting in production. Existing
native traffic collectors still need their observed-source integration.

Each source registers a UUID for one counter lifetime. Registration retries
are idempotent; a unique partial index prevents two active lifetimes for the
same client/source. Within a lifetime, older sequences are ignored, exact
replays do not charge, conflicting sequences and regressed counters fail.
A reset requires a new lifetime, not a guessed counter restart. Multiplier
change/reset requires final snapshots from all active sources, closes them
and advances the revision in one transaction. Reset preserves manual disable,
expiry and quota configuration. Native observed-source freeze/drain/restart
remains to build.

Panel reset paths now use `ResetAdmitted` for accounts owned by admission
sources. Canonical-row locks serialize snapshots with concurrent admissions;
every byte granted by such sources is already durable, so their committed
cursors form a complete reset boundary. Any active observed source instead
rejects the reset until it supplies a final snapshot. Bulk resets wrap all
clients, projections, source closures and group baselines in one transaction.
Canonical locks follow sorted IDs; batch lookup preserves the existing efficient
legacy reset path instead of querying ownership separately for every client.
Automatic renewal uses the same boundary and updates the existing expiry
fields without changing the operator's enable choice. Legacy raw-quota jobs
exclude ledger-owned accounts; the policy controller enforces their billed
quota and expiry. Initial production ownership activation must be coordinated
with the serial traffic writer before enabling a backend.

The internal Admit operation checks current enabled/expiry/quota state and
commits exact billing before granting forwarding rights. Its source must have
one exclusive, serialized owner: two independent senders cannot share a meter
and independently choose sequence numbers. Runtime ownership/lease enforcement
and attachment to actual protocol flows are prerequisites for activating this
path. An accounting report replay is not a reusable permission to send a new
payload. Apply remains the settlement path for already observed usage.

`policyflow.Controller` now owns one admission-only meter and two shared rate
limiters per configured client. Before forwarding each bounded payload grant,
it serializes and commits the cumulative report. A stable runtime source name
can claim a new incarnation after restart; this atomically closes the previous
incarnation. Its old owner cannot obtain more grants or open idle flows. An
observed counter cannot be retired this way: its final snapshot is required.
The AdmissionOnly column defaults false when migrating existing meters.

The controller checks current policy before dialing a target and closes all
tracked flows for an affected client on quota/disable/expiry or source fencing.
An independent monotonic-time watchdog closes stale flows even if a database
operation stalls. Stream cancellation does not abandon an in-flight shared
cursor transaction; an ambiguous transaction result instead protects the
client until an explicit source takeover recovers the durable cursor.

This is tested with real TCP sockets, SQLite and PostgreSQL. The dedicated SSH
listener now calls it through the existing panel Runtime and service lifecycle.
Native Xray and other protocol adapters remain unintegrated.
Rates are currently supplied to the controller in memory; persistence and
node distribution remain required before public controls can enable them.

Policy application validates backend capabilities, stages configuration,
applies it, confirms observed revision and then reports success. Failed apply
retains the last safe config or blocks the affected service; never direct
fallback. Reconcile desired/observed revisions after crashes and reboot.
Subprocesses use private config files, bounded logs, exit supervision and
owned-resource cleanup. All node mutations still dispatch through Runtime.

## Data paths (targets to prove)

| Service | Identity, meter, shaper and route location |
|---|---|
| Xray protocols | client → native auth → client-aware dispatcher wrapper → shared policy budget/meter → existing Xray router → selected outbound → target |
| SSH -L/-D | SSH public-key auth → direct-tcpip channel mapped to client UUID → shared policy stream → authenticated internal routing bridge → Xray outbound → target |
| SSH -R | authorized bounded listener → forwarded-tcpip channel → same client policy; listener and remote target allowlists; disabled by default |
| SSH outbound | Xray-selected loopback bridge → pinned upstream SSH host key → direct-tcpip channel → target; no host management sshd changes |
| mieru | native authenticated user → policy-aware server dispatch adapter → same routing bridge → selected outbound; TCP and UDP separately tested |
| Snell v4/v5/v6 | unique client PSK + exclusive managed instance/listener → per-client isolated egress namespace → transparent TCP/UDP routing bridge → policy/routing → outbound |
| Port forwarding | exclusive node/transport/address/port owner → TCP stream or UDP association → client policy → original configured target + unified routing → outbound |
| AmneziaWG/WireGuard | authenticated peer → stable client mapping → userspace flow bridge/core dispatcher → shared policy → router/outbound |
| TUIC | authenticated UUID at backend dispatch (existing encrypted aggregate relay insufficient) → client policy → routing bridge |
| MTProto | per-secret authenticated identity → backend policy/identity-preserving egress → routing; no second charge at bridge |

An internal bridge authenticates the client with a private per-binding secret
and carries original destination, network and source inbound. Domain rules use
only domains supplied by the protocol or actually observed; an IP-only Snell
egress cannot invent a domain. Namespace DNS interception and observable
mapping can preserve available DNS information, but must not promise domains
for externally resolved requests. Sniffing follows existing explicit policy.
Route precedence, block, balancer and egress choice need real requests.

## Rate and quota enforcement

Each authenticated data path acquires raw-byte budget from the same client
policy object, shared across directions' independent buckets, connections,
channels and listeners. Bounded queues/backpressure implement shaping; a
datagram larger than the available quota is rejected whole. A policy update
wakes waiters; disable/quota/expiry cancels only that client's flows.
The accounting boundary is before forwarding application payload; backend
network counters remain diagnostic, never a second billing source.

Quota uses durable reservations so simultaneous flows cannot independently
spend the same remainder. The reservation/event journal has bounded units,
acknowledgment and recovery rules; do not claim zero overuse until independently
observed at the documented boundary. Expiry and manual disable are distinct
from depletion; reset clears usage/depletion only.

For multiple nodes, an owner allocates bounded byte credits and rate shares;
the sum of shares cannot exceed the configured global client rate. A node with
an expired policy lease stops protected traffic. Do not expose node-local caps
as global. Node snapshots carry already billed usage and source identity,
without applying the multiplier again. Mixed-version nodes reject policies
they cannot enforce rather than silently running unrestricted.

## Backend-specific constraints

- Snell: separate v4, v5 and beta v6 binaries until each compatibility pairing
  is verified. One client per process/listener; describe memory/process/port
  costs. Test ordinary UDP and v5/v6 QUIC mode separately. Official binary
  provenance/hash/architecture checks, license review and Linux capability
  gating are required. Real Surge clients are currently unavailable here.
- mieru: prefer its native user model. Its rolling-window quota and panel
  lifetime/reset quota must not run as independent authorities. A policy-aware
  adapter must preserve username before outgoing SOCKS dispatch and UDP.
- SSH: embedded dedicated server; public keys, no session/shell/exec/PTY/SFTP/
  agent forwarding. Standard -L/-D are TCP; -R requires admin opt-in and
  controlled addresses/ports. No UDP claim for standard SSH channels.
- Port forwarding: userspace implementation gives explicit ownership and a
  uniform routing hook. nftables evaluation remains documented; if introduced,
  only dedicated rules/chains/marks, atomic validation/application, restored
  conntrack marks, bidirectional counting and established-flow revocation.

## Integration and acceptance

Every vertical task includes models/migrations, service, API schemas and
docs, React forms/validation, en-US/zh-CN plus existing locale fallback rules,
real backend lifecycle, raw/billed statistics, export, recovery and packaging.
Protocol-only fields appear only where meaningful. Diagnostics expose observed
capabilities and revision without secrets. Missing binaries or permissions
produce actionable errors.

The matrix tracks all existing features, including LDAP/groups/HWID/host
overrides/fallbacks/notifications, and must be revisited per vertical slice.
The final gate includes make verify/race, PostgreSQL, real clients, sustained
throughput at two caps and unlimited baseline, concurrent same-IP clients,
quota cutoff/restart, route egress/priority/block and failure recovery.

## Managed SSH production integration (partial vertical)

The existing Inbound and Client services accept `protocol: ssh` and typed client
`ssh.publicKeys`, `ssh.targets` and optional `ssh.reverse` settings. Canonical
`clients.ssh_config` survives client JSON, merge, migration and backups. Client
email is the SSH login/routing label; the internal policy UUID owns accounting.
A dedicated Ed25519 host key and loopback bridge port are generated once in the
inbound settings. Neither uses the operator's Git key or host management sshd.

Creation establishes durable account ownership inside the same serialized
transaction as the new canonical client and traffic projection. For now, existing
legacy clients and mixed native/remote attachments are refused: their coordinated
accounting migration is still required, not declared inapplicable. All enabled
local SSH listeners share one process-owned controller and runtime source.

`GetXrayConfig` stages bridge credentials without rotating them on repeated reads;
failed preparation leaves the previous authentication snapshot intact. The manager
waits for the actual process configuration fingerprint and authenticates its
private SOCKS listener before opening SSH. It checks process state every 250ms,
retries occupied ports, deduplicates protected-state log messages and closes owned
resources on stop. Public-key/permission edits use Runtime to revoke affected
sessions immediately. New or renamed logins remain unavailable until the actual
bridge binding matches their immutable policy ID and routing label. Retaining the private bridge binding for a disabled client
avoids rebuilding the router for unrelated clients; SSH admission remains revoked.
Reset fencing is followed by automatic source replacement, still subject to quota,
manual enable and expiry checks. First successful signed authentication activates
negative delayed expiry atomically across canonical, traffic and inbound settings;
its database operation and queue wait have a 500ms deadline.

The existing-client policy tab, billed client list, SSH creation/credential forms
and strict OpenSSH configuration export now have implementation and direct tests.
SSH upstream, online/IP/device integration, remaining bulk and inbound management
surfaces, node distribution and packaging remain open. Standard proxy
subscriptions do not fabricate SSH nodes; OpenSSH files use actual host public-key
pins. Initial listener/membership application follows the existing 30-second core
configuration scheduler; live rate/multiplier checks have separate two-second
bounds. This remains a partial SSH vertical. Bridge membership or
routing-label changes still require core configuration application and can restart
other sessions. Per-client isolation for those operations, preview generation
without runtime staging side effects, explicit status UI, capacity under many
listeners/clients and complete failure rollback remain follow-up work.


Portable client export reads canonical records, attachments, traffic projections
and owned policy/usage in one snapshot. PostgreSQL uses a read-only repeatable-read
transaction. SQLite uses `BEGIN DEFERRED` on one dedicated connection: its driver
ignores the read-only option and the normal writer DSN selects `BEGIN IMMEDIATE`,
which would block accounting writers. The WAL read snapshot allows those writers
to commit. A fresh GORM session prevents prior query state leaking between tables.

Portable restore commits each client's complete state before Runtime admission.
Rates, multiplier, historical whole billed bytes, fractional carry and independent
projection restrictions are restored with a new immutable identity; source IDs
and active cursor lifetimes stay local to the original database. Retained traffic
may be reassigned only when its previous canonical owner no longer exists.
No schema migration is needed for this wire-format change: it uses the existing
policy, usage and traffic tables. Native/global policy executors and complete
backup/deployment restoration remain separate required work.
