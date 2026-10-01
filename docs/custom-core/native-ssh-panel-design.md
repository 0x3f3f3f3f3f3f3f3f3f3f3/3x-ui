# Native SSH panel integration — 2026-10-01

The original requirements authorize autonomous staged engineering and inline
execution. This is the next native-protocol vertical after the mieru panel and
Snell v5 QUIC increments. Its purpose is usable SSH inbound and outbound through
the existing panel, with canonical users and the same policy/ledger as Tunnel.
No business sshd, OS account, panel decoder or independent quota is introduced.

## Account and host-key authority

Extend Client and ClientRecord with separate `sshUsername`,
`sshAuthorizedKeys` (authorized-key lines in a string) and `sshPassword`.
SQL remains authoritative; email is a display label and stable_id owns policy.
Generate a missing username once. Users supply public keys; the panel never
generates or retrieves their private keys. Password authentication is enabled
only by the listener's explicit allowPassword setting. Native limits are
username256bytes, password1024bytes, at most16 public keys and16384bytes per
authorized-key line. Reject authorized-key options, certificates and malformed
keys using the pinned Go SSH parser; normalize keys for consistent comparisons.

Omitted authentication fields are resolved inside the serialized SQL writer.
Explicit clear-password/clear-authorized-keys commands must be distinguished
from omission, validated across linked SSH listeners and never clear unrelated
protocol credentials. Reserve usernames across enabled and disabled bindings.
When a user has no valid enabled authentication method, attachment/enable fails
before committing. Rotation removes only that native credential generation;
other users and the owner's Tunnel continue with the same usage history.

Each SSH inbound receives a server-generated immutable host-key UUID and an
Ed25519 business host key in a dedicated NativeSSHHostKey SQL record. Include
the internal Inbound.SSHHostKeyID reference and a NativeSSHHostKey record with
ID (UUID primary key), PrivateKeyPEM (excluded from ordinary JSON), PublicKey,
Fingerprint and CreatedAt. Clear commands are clearSshPassword and
clearSshAuthorizedKeys; omission has no clearing effect. Include
the new model in both database model inventories. Full SQLite/PG backups and
cross-database migration retain the UUID, private PEM and public fingerprint.
Clone/portable inbound creation receives a new service key; full database
restore preserves the original trust identity. Missing/corrupt referenced SQL
keys fail rather than silently changing the host fingerprint.

Materialize the database-owned key only under a private native-ssh/hosts
directory inside XUI_DB_FOLDER. Reject links, permissive modes, conflicting
existing contents and invalid references. Write0600 files atomically, preserve
them on reuse/restart and recover the same key after a file-only loss. Never
read any Git credential or host management SSH file. Host private material is
excluded from public options, ordinary client responses and logs; privileged
database backup retains it. Show the public key/fingerprint in existing UI.

## Runtime, outbound and reverse forwarding

Panel settings retain clients and native server options. Runtime emits users
with username/publicKeys/password, canonical email and SQL stable clientId, plus
the managed hostKeyFile. Reject request-supplied users, host-key paths, IDs and
unsupported TCP/security wrappers. Negotiate trusted-ssh-client-id-v1 before
runtime preparation/materialization and handler account changes, including
empty listeners and removals. Diff native users for hot credential rotation;
listener/host-key/reverse changes follow the existing rebuild boundary.

Expose real core resource/time limits and opt-in reverse controls. Reverse
bind addresses, port range, source CIDRs, port-zero permission and listener
count must be validated. The client-selected final -R target stays unknown to
the server. Existing direct channels enter Dispatcher; reverse channels retain
the verified transport's identity and client-relative policy directions.

Native outbound forms provide address/port, username, required host-key pin,
password or a dedicated business privateKeyFile, and actual timeouts. Key-file
configuration is confined to a private native-ssh/outbound directory; validate
regular files, private permissions and parsed keys without touching Git keys.
No insecure-ignore pin, UDP, global mux or fake TLS transport is offered.

## Existing UI and export

Use the existing client/inbound/outbound forms, attach and bulk flows. Public
keys remain separate from WireGuard keys and protocol passwords. Preserve
credentials on rename/reopen/clone; show actual validation errors in EN/ZH.
Expose public host trust data through existing inbound options. Client info
exports OpenSSH connection instructions/config and a known_hosts entry, with
strict verification and an explicit user-owned business private-key path.
Never export a fabricated VMess/Clash node or put a password in command argv.
Reverse examples are offered only for explicitly authorized configurations.

## Acceptance and remaining boundaries

Verify public API/form payload → SQL → merged custom core → real OpenSSH
-L/-D/authorized -R → target, plus strict native outbound. Cover shared Tunnel
usage, rotation/sibling survival, disable, expiry, quota, delete/restart and
listener release. Verify old SQLite/PG schema, full backup/key restoration,
portable identity export and missing-capability refusal with actual binaries.
One final whole-panel review follows all tasks, with one correction pass.
Remote key provisioning/coordinated budgets, Snell panel, other sidecar
migrations and production deployment remain open separately.
