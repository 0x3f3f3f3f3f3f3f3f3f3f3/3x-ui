# Native Snell panel backend checkpoint

This checkpoint completes the backend account and listener layer for Snell4/5/6.
The complete panel deliverable still requires the existing client/service/outbound
forms, truthful native client downloads and full public HTTP acceptance in Tasks2/3.
SSH and mieru remain part of the same managed business core.

A canonical SQL account stores an independent `snellPsk` (`snell_psk`), separately
from ordinary passwords, SSH authentication, mieru authentication and wire UUIDs.
Initial ownership generates a random32-byte URL-safe PSK inside the SQL writer;
omitted updates and membership commands preserve the current stored PSK.
Standalone creation, bulk creation and import reject non-UTF8 or oversized values.
Bound PSKs are nonempty and at most255 bytes; all linked v6 listeners require12.

Each local Snell resource has exactly one canonical owner. PostgreSQL locks the
physical resource row before checking prospective membership, including disabled
owners. Two independent SQL transactions cannot acquire the same resource.
The existing outer `ownerClientId` command can select a canonical local account;
raw runtime IDs, flat PSKs and native user arrays are rejected as panel commands.
Remote/uncoordinated shared memberships are refused. The Tunnel ownership rules
remain distinct and retain their existing checks.

Pure native option/port/stream validation runs before normalization and SQL writes.
Versions4/5/6 are explicit. Version5 reserves TCP+UDP regardless its optional
`quic` setting; versions4/6 reserve TCP. Unsupported wrappers, invalid socket
options and invalid version/obfs/mode/QUIC combinations fail before persistence.

Managed compilation projects Snell's flat `psk`, `email`, `clientId` shape from SQL,
including a disabled reserved owner. Handler RPCs use the typed native Snell PSK
account. PSK rotation removes/adds authentication without rebuilding the physical
listener, while revoking its existing flows. Shared policy disable, expiry and quota
close/refuse business traffic. Final detach/delete disables the empty SQL resource
and releases its native TCP/UDP sockets. Historical shared ledger values survive.

`trusted-snell-client-id-v1` is negotiated before startup preparation, add/change,
account removal and listener removal. The actual preceding SSH checkpoint core
refuses all3 Snell versions before preparation or business listener activation.
Snell removal resolves the canonical flat identity and cannot inherit a forged
`clients` array. Public filtered updates fence the selected canonical owner against
rename/label reuse, including writes through ordinary protocol forms.

Acceptance uses actual local sockets and the built custom core. It verifies native
v4/v5/v6 CRUD, rotation, sibling continuity, exact shared Tunnel ledger, disable/
restore, expiry/quota restore, final detach/delete, restart and TCP/UDP release.
SQLite old-schema upgrade and PostgreSQL upgrade, cross-database migration and
SQLite recovery preserve identity, independent credentials, memberships and usage.
Standalone portable account export/import preserves PSK and creates a new identity.

Evidence is preserved outside the repository in `/root/task-evidence`, including
compiled RED regressions, frozen source/binary/log hashes,25 SQLite and28 actual
PostgreSQL native parent tests with no applicable native runtime skips, and retained
SSH/mieru gates. Optional PostgreSQL-only tests are explicitly skipped on SQLite.
The Task1 ledger records final Go/static/generated-contract gate results and commit.

This is a local backend checkpoint. Proprietary Surge device inbound use, full
public panel/download acceptance and the original project's broader distribution,
remote coordinated budgets and legacy migration work remain separate requirements.
