# Installation and recovery — incomplete integration

Do not deploy this feature branch as a completed service integration. This
work has not changed any production service, host firewall, qdisc or sshd.

## Development

Use the versions in audit.md, a C compiler, and an isolated checkout:

```sh
go mod download
cd frontend
npm ci
npx playwright install chromium
cd ..
make dist-stub
make test-go
make verify
```

`make dist-stub` only satisfies Go embedding for tests; it is not a usable UI.
`make build` compiles the real frontend and backend. Configure runtime data
under the checkout using .env.example. Use ephemeral ports and loopback for
tests. Privileged network tests must run in a new namespace/container and
refuse to run against the host network namespace.

## Required delivery work

- Pin backend assets and checksum manifests by architecture; capability probe
  before enabling a service. Do not bundle Snell until redistribution terms
  are established. Keep its beta label explicit.
- Update Docker libc/toolchains and expose only selected service listeners.
- Set fork-specific installation/update origin and channel. Reject an update
  that would silently replace the managed-policy build with upstream binaries.
- Back up DB, source cursors, billing remainders, policy revisions, host keys,
  managed service bindings and backend manifest. Secret backups require the
  same protection as the current panel database.
- On restore, stop managed data paths, restore atomically, rotate source
  incarnations/leases under the restored billing authority, reconcile policy
  state, then allow traffic. Never attach an old counter to a recreated user.
- Rollback with a tested pre-upgrade complete backup and matching binaries.
  No safe downgrade migration has been established. Do not run an older
  binary over a partially migrated policy database or discard billing state.
- Uninstall cleans only project-owned resources; never flush the host ruleset,
  reset a global qdisc, remove unrelated containers or change management SSH.

These are required implementation/validation tasks. No installation script or
rollback guarantee for the new backends is claimed at this stage.

## Local client policy API

`GET /panel/api/clients/policy/:email` returns the client identity, edit version,
raw directional B/s limits, decimal-string multiplier and exact usage. Save all
policy fields with `POST` to the same path, retaining `policyId` and `version`
from GET. Reload after a conflict. Admin/session authentication is required;
monitor and node-sync tokens cannot use this endpoint.

The current executor supports clients whose attachments are all local managed
SSH inbounds. Scope `local` means the sum across those listeners on this node;
it does not claim a global multi-node limit. A rate is an integer from 0 to
1099511627776 raw B/s, with zero unlimited. One Mbps is 125000 B/s; one MB/s is
1000000 B/s. The billing multiplier is a decimal string from 0.001 to 1000 with
at most three fractional digits. It affects billing, not raw bandwidth.

Rate-only changes preserve live flows. A multiplier change atomically closes
previous metering sources, preserves historical billed bytes and thousandth-byte
carry, and retires their existing flows; clients reconnect using the new billing
boundary. Flow reset preserves rates, scope, multiplier and the edit version.
A post-commit runtime error explicitly says that the policy was saved; reload
before retrying. `supported` means backend capability, not process health.

Database backups include `client_policy_settings`, accounting records, cursors
and SSH credentials. SQLite restore and SQLite→PostgreSQL copy are tested.
Portable client JSON import/export does not yet carry the new policy and must
not substitute for a complete database backup during this development stage.

Usage `billed` and `remaining` are whole-byte accounting components. Preserve
`remainder` (0–999 thousandths of a byte) when displaying the fractional balance:
exact billed bytes = billed + remainder/1000; for a positive limited balance,
exact remaining bytes = remaining − remainder/1000. For example, billed `5`,
remaining `995`, remainder `500` represents 5.5 billed bytes and 994.5 remaining
bytes. Use integer/decimal arithmetic for display; never convert large byte
strings to JavaScript Number.

### Client policy editor

Open **Clients → Edit → Traffic policy** for an existing client. The tab displays
raw upload/download, billed usage, quota, remaining quota and the saved multiplier
as exact byte values, including thousandth-byte carry; it never rounds large
integers through JavaScript Number. The inputs use raw B/s and decimal multiplier
strings. **Apply traffic policy** saves this policy immediately, independently
of the other tabs. Change quota/expiry in Basics and save those separately.
Creation-time policy fields, bulk editing and portable policy export remain open.

The editor retains its loaded immutable identity and revision while usage refreshes
in the background. A concurrent edit or any failed/ambiguous save disables further
application until **Reload saved policy** succeeds. Reload deliberately replaces
unsaved policy edits. Unsupported attachments show a capability warning and disabled
controls; this does not indicate that the underlying process is healthy.
