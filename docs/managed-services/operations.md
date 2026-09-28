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
Portable client JSON now carries the policy and historical charges, but does
not include the complete database or deployment configuration described below.

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
Creation-time policy fields and bulk editing remain open. Portable policy snapshots
are supported through Export/Import as described below.

The editor retains its loaded immutable identity and revision while usage refreshes
in the background. A concurrent edit or any failed/ambiguous save disables further
application until **Reload saved policy** succeeds. Reload deliberately replaces
unsaved policy edits. Unsupported attachments show a capability warning and disabled
controls; this does not indicate that the underlying process is healthy.

### Billed balances in the client list

For a client with an owned usage account, paged/unpaged client APIs now include
`billing`; hydration (`GET /panel/api/clients/get/:email`) returns the same field
beside `client`. Its byte values are decimal strings. Existing `traffic` and
`usedTraffic` remain raw compatibility fields. An absent/null `billing` means
legacy accounting; reading a client never creates a usage account.

The Clients table, mobile cards, information modal and page summary use billed
usage for quota consumption. Compact balances use IEC units (KiB/MiB/GiB), and
hover details retain exact bytes. Usage filters, remaining/usage sorting and
status counts execute in SQL with integer whole bytes and fractional carry.
The effective managed quota is the smaller positive canonical/traffic limit.
An account is exhausted when it cannot pay for one more raw byte at its current
multiplier, even if a fractional billed balance remains. Unlimited quota remains
a separate flag. Historical billed usage is never recalculated on reads.

Both depleted-client cleanup entry points now select managed accounts using
billed usage, and preserve clients with interval, monthly or weekly renewal.
Inbound-scoped cleanup retains traffic still referenced by a sibling inbound.
Both paths recheck immutable identity and depletion under canonical/traffic row
locks, then atomically remove membership and related data before Runtime dispatch.
Concurrent reset/quota edits, identity recreation and late rollback are tested
on SQLite and PostgreSQL. This does not extend the same transaction guarantee
to ordinary explicit bulk-delete operations.

This does not complete node dashboard counts, inbound-specific traffic widgets,
notifications, subscription usage or distributed billing. Global raw overlays
cannot replace this local owned ledger; global ownership remains separate work.

### SSH creation, credentials and OpenSSH export

Create an inbound with protocol `ssh`, a literal listen IP and a dedicated TCP
port. The panel generates a separate host key and private routing bridge; this
form does not offer Xray transport, TLS or sniffing settings. Add a client in the
existing Clients page, select local SSH inbounds and enter independent public
keys under Credentials. The email field is the SSH username. Keep each matching
private key on the client device; never reuse the repository deployment key.

Each target rule authorizes an IP/domain and port for `-L`/`-D`. An empty list
rejects all destinations. Host `*` means any host and target port `0` means any
port. Reverse forwarding stays off until an explicit address/port is added.
Reverse port `0` authorizes only an OS-allocated port; it is not a wildcard for
fixed ports. Prefer a loopback reverse address. The server cannot observe or
restrict the client-side target of standard `-R`; listener permission does not
claim that restriction. SSH wire IP/device-count limits remain unimplemented
and are not offered as effective controls in this form.

Client Information and the QR/export dialog offer two actual files:
`xui-ssh-<inbound-id>.conf` and `xui-ssh-<inbound-id>.known_hosts`. Download both
into an individual directory for that client/inbound, change to that directory
and run the displayed command with the matching client private-key path.
Use `-D 127.0.0.1:1080`, `-L` with a permitted destination, or an authorized `-R`
argument. These standard modes carry TCP; no SSH UDP or proxy subscription node
is fabricated. A missing or invalid host public key/address prevents export.

The exported configuration requires strict host-key checking against the
listener's actual public key and disables global/DNS/command-based trust
alternatives. `IdentityFile none` avoids trying default private-key files; the
user supplies their independent key with `-i`. Session, PTY, agent and X11
requests are disabled. After an intentional host-key replacement, obtain a new
known_hosts file through the authenticated panel. This follows the documented
[OpenSSH client configuration](https://man.openbsd.org/ssh_config) and
[forwarding options](https://man.openbsd.org/ssh).

New listeners and attachment changes currently wait for the existing 30-second
Xray configuration refresh while the core is running. Initial readiness must be
measured separately from live rate/multiplier updates. The browser acceptance
fixture allows one scheduled refresh plus five seconds of startup observation,
then retains the existing two-second meter-replacement limit for a multiplier
change. Dedicated applied-state reporting and faster isolated attachment
application remain open work; waiting is not evidence that a failed apply worked.


Portable client import now commits each new client's complete attachment and
raw-usage restoration before Runtime apply. Failed items are reported in
`skipped` and leave no new client or partial binding; previously successful
items remain committed. Existing email identities are skipped even if subId
matches. The ordinary create/attach APIs keep their existing reuse behavior.
Legacy SSH imports without `policy` receive a fresh accounting identity with
historical raw usage charged at the legacy default 1x, including unattached
clients. New exports include a versioned policy snapshot for owned accounts.

### Portable policy and usage snapshots

Use **Clients → Export** and **Clients → Import**, or the existing administrator
API pair `/panel/api/clients/export` and `/panel/api/clients/import`. Import sends
the exported JSON array as the string-valued `data` field. Preserve SSH public
keys, destination permissions and inbound IDs; referenced inbounds must already
exist on the destination. Unattached clients export `inboundIds: []`.

`client.totalGB`, `traffic.up` and `traffic.down` now export as decimal strings.
Import also accepts the old integer JSON form. Keep the strings intact when
editing or processing exports: conversion to JavaScript Number loses precision
above 2^53. Decimal fractions, exponent notation, noncanonical strings, negative
byte values and values outside signed 64-bit storage are rejected. A raw
upload/download sum must also fit signed 64-bit storage.

An owned account includes this additional object (example: 7.5 historical billed
bytes, current multiplier 2, quota projection 9 bytes):

```json
"policy": {
  "formatVersion": 1,
  "uploadBps": 65536,
  "downloadBps": 131072,
  "scope": "local",
  "multiplier": "2",
  "billed": "7",
  "remainder": 500,
  "trafficTotal": "9",
  "trafficEnable": true,
  "trafficExpiry": 0
}
```

The format preserves historical charges and thousandth-byte carry without
repricing them at the current multiplier. Canonical and traffic-projection
quota/enable/expiry remain independent; restoring or adding quota does not clear
manual disable or expiry. Rates remain raw B/s, independent of the multiplier.
The current managed-policy importer accepts only local SSH attachments or an
unattached SSH client. A policy snapshot requires a raw traffic snapshot. Unknown
versions and unsupported attachments are reported in `skipped`; ordinary create
and bulk-create reject policy snapshots instead of silently discarding them.

Export reads one database snapshot while accounting can continue to commit.
Import creates a fresh immutable policy identity and fresh meter lifetimes; it
never exports or reuses source cursor IDs. An explicit snapshot can replace
retained traffic belonging to a deleted client. Late reports from the deleted
identity cannot charge the restored client. Existing live clients are skipped.

This is client-level portability, not a complete database or deployment backup:
inbounds, host signing keys, external links, routing configuration and live meter
cursors are not recreated by this file. Keep a complete database backup for
upgrade/rollback; older binaries do not understand the new byte-string and
policy format. No safe downgrade migration is established. Large-import/export
performance and distributed policy restoration remain unverified.
