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

## Remaining boundaries

Public AWG create/edit currently reject node assignment. A remote-node test
experiment therefore did not describe a supported AWG operation and was
removed; node integration remains an open requirement.

Peer-versus-peer duplicate forward ownership, candidate self-collisions during
complete edits, runtime listener failure reporting, and AWG forwarded-payload
policy enforcement require follow-up. These save-path checks do not prove
wire interoperability, bandwidth shaping, accounting or quota cutoff. General
first-class forwarding, kernel forwarding/offload and firewall coexistence
also remain unimplemented or unverified as recorded in the plan.
