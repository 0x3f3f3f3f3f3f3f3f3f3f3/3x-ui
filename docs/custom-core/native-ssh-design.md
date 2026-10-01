# Native SSH core increment — 2026-10-01

Implements requirements sections 8 and the shared identity/data-path constraints
inside the pinned custom Xray module. Execution is authorized inline; ordinary
engineering decisions and staged plans are autonomous. This increment excludes
panel, export, node orchestration and legacy/password-owner changes.

## Chosen boundary

Use core-pinned `golang.org/x/crypto/ssh v0.55.0`, with native registered SSH
inbound and outbound config. Alternatives of an external sshd or a panel SOCKS
bridge violate the architecture. A shared outbound SSH pool adds attribution and
cancellation hazards without helping acceptance; each outbound Process instead
owns its SSH connection.

Inbound users are `protocol.User` with SSH Account username, authorized public
keys and an optional password. Every account requires a server-owned client ID
and email. Public-key offers are checked, but identity is finalized only by the
verified-signature callback. Password auth is explicitly enabled by server config.
Business host keys are loaded from an absolute persistent file; creation, backup
and UI management belong to the later panel increment. No Git key is read.

After authentication a zero-payload CPE session and credential lease own the SSH
transport, so disable/quota/expiry/removal closes idle transports and reverse
listeners too. Each direct-tcpip channel clones session state and enters
Dispatcher.DispatchLink with decrypted Reader/Writer. CPE charges reads as Upload
and writes as Download once. A channel-specific closer avoids killing siblings
on ordinary channel cleanup. The real TCP SSH source is authoritative; the wire
origin fields are untrusted.

Opt-in reverse forwarding handles tcpip-forward/cancel-tcpip-forward and creates
forwarded-tcpip channels. Bind IPs, port range, source CIDRs and listener count are
explicit. The request bind string is preserved for OpenSSH matching; localhost
maps only to an authorized loopback IP. Port zero is optional and the allocated
port must satisfy the range. Channel→accepted peer is Upload; peer→channel is
Download. Both use the same CPE before forwarding. Registry metadata identifies
the reverse listener and marks the client-selected final target unknown; there
is no fabricated final-domain route.

Transport state owns listeners, streams and cancellation under one lock. Forward
cancellation removes the listener and its active streams. All channel/global
requests are serviced and unknown requests rejected. Session/shell/exec/PTY,
subsystems, agent/X11 and streamlocal channels are rejected. TCP only; UDP fails
explicitly. Half-close is preserved where possible.

## Bounds and outbound trust

Defaults: 10s handshake and channel-open timeout, 300s idle timeout, 6 auth tries,
64 transport slots, 4 per user, 16 channels per transport, 128 aggregate channels,
4 reverse listeners per transport. Config can lower these and increase within
validated hard ceilings. SSH has a 2 MiB channel window, so channel bounds limit
its buffered memory. Cancellation closes channel/transport because SSH channel
connections do not implement socket deadlines. Pending opens are bounded and a
timed-out OpenChannel closes its transport to release library goroutines.

Outbound uses the supplied Xray dialer for the configured SSH server, then
NewClientConn with the configured hostname and a required authorized host-key
pin. It opens direct-tcpip to the routed TCP destination and preserves the
originating Dispatcher identity. There is no insecure-ignore callback.

Dispatcher currently accepts before target connection success and closes on
route/dial failure; this matches existing proxy behavior. This increment does
not claim an SSH channel-open success acknowledgment proves target availability.

## Acceptance

Real native core + standard OpenSSH -L/-D/authorized -R; denied reverse/session;
strict outbound pin; shared Tunnel ledger and rates; disabled, revoked and
unknown identities; listener/channel cleanup; configured resource/handshake
bounds; fresh channel contexts; port-zero reply and cancel lifecycle. Race tests,
core build and configuration regressions supplement the protocol tests.
