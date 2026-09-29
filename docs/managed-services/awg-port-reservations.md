# AmneziaWG forwarded-port reservation safety

This is a prerequisite for adding managed services alongside existing AWG
forwards. It does not implement first-class forwarding or account for AWG
forwarded payload. Those Task 8 requirements remain open.

## Behavior

AWG forwards bind both TCP and UDP on the host wildcard address. Saving their
port numbers must therefore reject overlapping SSH upstream bridge, Xray
template listener, API, metrics and AWG egress reservations. The existing
panel, public inbound, managed bridge and AWG relay checks remain in place.
The rejection names the reservation and port before applying runtime changes.

The template is read through the caller's database transaction. Missing
settings use the existing default template; a malformed stored template retains
the existing default API reservation. A batch loads the reservation list once,
rather than querying for each of its up to 100 forwarded ports per client.

The checked public operations are inbound creation and complete edit, client
addition and edit, portable import, inbound enable and bulk client enable.
An inbound enable re-reads its row inside the transaction and validates its
forwarded ports after the transactional flag update; failure rolls it back.
Full inbound edits recheck before changing traffic records or runtime state.
Bulk enable checks the affected clients before changing their projections.

## Concurrent saves and retained accounting

PostgreSQL writers share the existing schema-scoped transaction advisory lock
`3x-ui:listener-reservations`. AWG client writes now take that lock before
identity, attachment or traffic mutations, then load current reservations.
Inbound and template saves already participate in the same locking protocol.
Inbound enable now holds the lock and validation in its write transaction.

Portable import takes the reservation lock before releasing a retained traffic
row for reuse. Acquiring it only in the later prepared client addition would
reverse the inbound-save lock order. The regression test first observes the
actual advisory wait, then uses the other transaction's `FOR UPDATE NOWAIT` to
prove the import has not already locked that traffic row. A rejected import
preserves the old policy identity and raw counters.

SQLite uses its existing writer transactions; no PostgreSQL advisory SQL is
issued against SQLite. No database schema, public API fields or frontend
assets change in this milestone.

## Reproduction and evidence

Use the repository Go toolchain and an isolated PostgreSQL test database:

```sh
XUI_TEST_PG_DSN='host=127.0.0.1 port=55432 user=nobody dbname=postgres sslmode=disable' \
go test -p 1 -race -count=1 -timeout=3m \
  -run '^TestAWGClient(ForwardsRespectAdditionalReservations|ForwardMutationWaitsForReservation)' \
  ./internal/web/service
```

`TestAWGClientForwardsRespectAdditionalReservations` and its PostgreSQL variant
cover five operations against five reserved owners and a free-port control:
60 cases. Conflict rejection preserves row counts, existing settings and the
enable flag. The enable fixture represents an existing disabled/restored row.

`TestAWGClientForwardMutationWaitsForReservation_Postgres` covers six operations:
client add/edit, import, full inbound edit, inbound enable and bulk client
enable. A separate transaction holds the listener lock and stages a new
template listener. Each operation must be observed waiting through
`pg_blocking_pids` and `pg_stat_activity`, then reject the newly committed
reservation. This exercises a separate database writer, not merely the panel's
in-process serialization. Bulk rejection keeps client JSON, canonical record
and traffic record disabled; removing the reservation allows a subsequent
bulk enable.

Observed failures before the corresponding fixes:

- 30 reserved-owner cases accepted conflicting forwards; all six free-port
  controls passed. An earlier PostgreSQL fixture reused a schema and failed
  on duplicate identities; that fixture was corrected before this result.
- Client add/edit completed while the other transaction still held its lock.
- Import held the retained traffic row while waiting for the listener lock;
  the other transaction received PostgreSQL `55P03` from `FOR UPDATE NOWAIT`.
- Complete inbound edit and enable waited but accepted the conflicting
  template after it committed. Five enable reservation cases also accepted
  conflicts. The targeted run selected PostgreSQL cases only; it is not
  evidence of a SQLite RED run.
- Bulk enable completed with `Changed: 1` before the lock holder committed.

The first corrected add/edit/import race run passed 3 top-level tests and
39 subtests without skips. After including full edit and inbound enable, the
focused SQLite/PostgreSQL run passed 3 top-level tests and 65 subtests without
skips. Final expanded regression results are recorded in [validation.md](validation.md).

The PostgreSQL observations use the documented
[session information functions](https://www.postgresql.org/docs/16/functions-info.html)
and [activity statistics](https://www.postgresql.org/docs/16/monitoring-stats.html).
No timing-only lock assertion, host firewall modification or production
deployment is used.

## Peer ownership and complete candidate edits

The ownership follow-up validates the entire saved AWG peer list against other
local AWG rows. Two clients cannot own the same wildcard TCP+UDP forward, even
on the same inbound. A canonical client attached to a second inbound cannot
create a second listener for the same host port; ordinary shared attachment
without forwarded ports remains allowed.

The service and runtime share `ForwardedPortClaims`: a claim requires a
nonempty email and a resolvable tunnel target on an enabled peer. IPv6-only
targets require the instance's IPv6 flag. As with the pre-existing reverse
listener check, disabled inbound rows retain otherwise valid peer claims.
That reservation behavior is distinct from actually starting their listeners.

Complete edits replace the old inbound in the reservation context with the
proposed row. Moving its public listener onto its own forward is rejected;
moving the listener away and forwarding the released port in the same edit is
allowed. Client additions, edits and bulk enable validate the stored settings
after rebasing over concurrent client changes, before the transaction commits.

Disabling a client can remove its claim despite legacy peer or fixed-listener
conflicts. Bulk disable and inbound disable, including full inbound edits,
remain possible amid legacy peer conflicts. Full inbound normalization still
checks fixed listener specifications; this is not a guarantee that every
invalid legacy configuration can be edited. Re-enabling rechecks ownership and
keeps all affected projections disabled on failure. Removing the old owner
allows a subsequent enable.

The PostgreSQL peer concurrency test stages another peer's port change in a
separate transaction. It observes the actual advisory wait, commits the owner,
then verifies rejection and unchanged settings/identity rows. Both same-row
rebasing and cross-inbound changes are covered. Temporarily removing only the
post-rebase addition check made both cases accept the collision; restoring it
passed both cases under the race detector.

Reproduce the ownership and lifecycle tests with the isolated PostgreSQL DSN:

```sh
go test -p 1 -race -count=1 -timeout=3m -run '^TestAWGForward' ./internal/web/service
```

Validation of the complete follow-up is recorded in [validation.md](validation.md).

## Revoking existing forwarded connections

A separate lifecycle follow-up closes streams belonging to a removed TCP
forward. Each listener owns a cancellation context and its accepted sockets.
Removal cancels pending tunnel dials and closes both sides of established
relays. The dial timeout remains separate from the established stream's
lifetime. Repeated listener/set closure is safe.

UDP listeners now record closure under the session mutex. The receive loop
checks it both before resolving a new target and before publishing a dialed
session, so a concurrent close cannot be followed by a newly retained flow.

The regression uses an actual amneziawg-go client/server and a service on the
client's netstack. It removes only one forward while retaining the peer and
another forward: the old TCP stream must close, UDP must continue, and a
restored TCP rule must accept new traffic. Another test closes a TCP listener
while its tunnel dial is pending. The UDP race uses a real socket and a
target-resolution barrier, then joins the receive loop before inspecting
remaining sessions. All three exposed the old behavior before the changes.

Xray runs in a separate process and cannot directly reach the peer's address
inside this private gVisor network stack. The existing forward therefore dials
that stack directly with gonet, bypassing Xray's per-email statistics. Removing
a depleted peer can revoke its connections, but forwarded bytes themselves do
not currently advance its quota. The lifecycle regression passed as recorded
in [validation.md](validation.md); it does not add accounting, shaping or quota
admission.

## Remaining boundaries

Public AWG create/edit currently reject node assignment. A remote-node test
experiment therefore did not describe a supported AWG operation and was
removed; node integration remains an open requirement.

Runtime listener failure reporting, forward-field edit/attachment consistency
and AWG forwarded-payload policy enforcement require follow-up. These checks do not prove
wire interoperability, bandwidth shaping, accounting or quota cutoff. General
first-class forwarding, kernel forwarding/offload and firewall coexistence
also remain unimplemented or unverified as recorded in the plan.
