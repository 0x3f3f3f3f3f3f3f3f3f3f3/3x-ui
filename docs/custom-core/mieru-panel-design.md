# Mieru panel integration

The user's primary outcome remains native Snell v4/v5/v6, mieru and SSH in one
Custom Xray-core, with ordinary panel/API/client workflows controlling the real
data paths. Native mieru core checkpoint6c475f50 is verified; this design covers
its following panel integration. The user's original instruction authorizes
ordinary engineering choices and staged plans without repeated approval gates.

## Stored identity and credentials

Retain the existing clients/client_inbounds tables, stable_id and shared policy
ledger. Add protocol-specific `mieruUsername` and `mieruPassword` to model.Client
and ClientRecord (`mieru_username`/`mieru_password` SQL columns). This avoids
rotating Trojan/Shadowsocks/TUIC passwords when editing mieru. An email is a
display/statistics label, never the mieru authentication identity. No separate
user table, quota system or panel-side decoder is introduced.

Create/attach generates missing credentials once using cryptographic random
values, then preserves them on reuse and omitted-field updates. Explicit valid
values rotate credentials. Validate the actual native library's UTF-8 and
1..64-byte limits; require unique usernames within each listener, including
disabled identities that may later be enabled. Preserve independent sibling
users. SQLite and PostgreSQL migrations, old backups, export/reopen and node
snapshots must preserve these fields and existing clients unchanged.

Panel settings keep `clients` with the usual lifecycle/policy fields and the
new credential pair. Runtime generation replaces that array with the native
core's `users`, each containing username/password, canonical email and stable
clientId from current SQL records. Saved/request-supplied clientId fields never
authorize ownership. Reject malformed, duplicated or stale credential bindings.
Physical transport TCP/UDP, MTU, user hints and resource bounds stay native
settings. Unsupported Xray security/framing is rejected rather than ignored.

## Runtime and controls

Recognize mieru as a managed-policy protocol, using the existing atomic
preparation/activation, private Unix control socket, durable usage settlement,
quota reset, expiry/disable and deletion workflows. Add
`trusted-mieru-client-id-v1` only to the core with tested native support;
compilation/startup and add/remove-account mutations must negotiate it before
changing state. Older cores are rejected before preparation or handler writes.

Build typed mieru accounts for HandlerService. Diff the native users array so
credential replacement removes the old generation before adding the new one,
and unrelated sibling transports survive. Listener edits/removal use existing
managed Runtime lifecycle. Tests must exercise public CRUD with actual official
mieru client payload, SQL receipts, stable identity shared with Tunnel, rotation,
disable/delete, expiry, restart and listener removal; configuration-only tests
do not count as data-path acceptance.

## Existing UI and official export

Add mieru to existing inbound and outbound choices and client attach/bulk flows.
Use existing form components, validation, permission checks and English/Chinese
translations. Inbound fields expose physical transport and actual applied native
options; client credentials remain on the shared client form. Outbound fields
use the exact core endpoint, authentication, transport, MTU and four multiplex
levels. Do not expose unsupported TLS/REALITY/Xray mux framing for this protocol.

Export an official v3.38.0 client profile/config and mieru URL using its public
appctl serializers; test round trips with official parsers, including IPv6,
domains and escaped Unicode credentials. The chosen subscription format must
support mieru; unsupported target formats give an explicit response rather than
a disguised VMess/SOCKS node. Preserve server/profile/transport choices on
reimport and outbound conversion. Use current routing/export/QR/download flows
instead of creating a separate disconnected management page.

## Scope and acceptance

Backend credential/runtime binding, UI and official export are staged logical
increments. Each must have real RED/GREEN evidence and scoped checks before
publication. Entire mieru panel support remains open until all of them and their
combined real-child CRUD acceptance pass. Snell/SSH core work continues in
parallel; further password migration features are not prerequisites. Remote
coordinated budgets, other protocol migrations and production deployment remain
outside this design; preserve existing explicit scope checks rather than invent
remote support.
