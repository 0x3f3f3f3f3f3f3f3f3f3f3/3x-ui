# SSH upstream connector implementation plan

> Execution: continue the existing native/inline `superpowers:executing-plans`
> workflow. This is the first data-path task of plan.md Task 5, not a reduced goal.

**Goal:** A bounded SSH client transport that opens TCP destinations at a strictly
pinned upstream and interoperates with a real, isolated OpenSSH server.

**Architecture:** The unified router will select an authored SSH outbound whose
managed private SOCKS bridge calls this connector. Each forwarded TCP stream
owns its SSH transport, so cancellation can close a stalled handshake/channel
without terminating unrelated streams. This costs one SSH handshake and socket
per stream; reuse is not required for correctness and any later pooling needs
separate cancellation/isolation evidence. All applicable ingress client policies
remain in place and the outbound performs no second client accounting.

**Tech stack:** existing Go 1.27, golang.org/x/crypto v0.57.0, OpenSSH test sshd.
No new module, system daemon change or use of the Git credential.

**Spec:** [Full requirements](requirements.zh-CN.md), especially sections III,
V (SSH upstream), VI/VII (shared ingress policy) and X D/E.

## Constraints and review focus

- Require address, port 1–65535, account, client private key and server public
  key pin; accept an optional private-key passphrase. No agent, shell, session,
  PTY, remote listener or automatic host-key trust. Errors contain no key data.
  Negotiate only the pinned key's algorithm; RSA pins use SHA-2 signatures.
  Host certificates and insecure host-key algorithms are rejected.
- Accept network `tcp` with IPv4/IPv6 literals or domain destinations; pass the
  host unchanged. Reject malformed targets and unsupported networks before dialing.
  The SSH channel cannot request a particular remote DNS address family, so the
  Go network aliases `tcp4`/`tcp6` are not exposed as false family guarantees.
- At most 128 simultaneous pending/established connections per connector;
  admission does not queue without a bound. Total dial, key exchange,
  authentication and channel-open deadline is at most 10 seconds, also respecting
  a shorter caller context. Established streams retain ordinary net.Conn ownership.
- Close cancels pending opens and closes owned transports. Connection Close is
  idempotent and releases its capacity once. Active read/write deadlines close
  the dedicated transport, waking channel-window waiters and returning a timeout.
  A timed-out active stream cannot be reused. An idle expired deadline can be
  cleared without closing the stream. Dial context cancellation affects only
  establishment; the caller owns the returned stream.
- The bridge/runtime integration must also bound total outbound resources and
  reject unsupported UDP, transport and chain configuration, without direct
  fallback. Connector completion alone does not finish the public outbound.

Review focus with required tests: wrong host pin/credentials must deliver no
payload; a server that stalls a channel open must release resources on cancel;
exhausted capacity must reject promptly and be reusable after close; connector
shutdown must interrupt pending and established streams; real IPv4/IPv6/domain
forwarding and half-close must preserve bytes without requesting shell access.

## Task 1: Bounded authenticated connector

Files: new `internal/sshoutbound/config.go`, `connector.go`, `deadline.go`,
`connector_test.go` and `openssh_test.go`.

Interface: `Config` contains address, port, user, privateKey,
privateKeyPassphrase and hostKey JSON fields. `NewConnector(Config)
(*Connector, error)` validates/compiles keys without opening sockets.
`(*Connector).DialContext(context.Context, string, string) (net.Conn, error)`
opens a destination through SSH. `(*Connector).Close() error` shuts down only
its own transports. Export typed sentinel errors for invalid configuration,
unsupported network, connection capacity and closed connector.

- [x] Write real-sshd forwarding/strict-pin tests, observe a failing connector.
- [x] Implement key validation, bounded dialing, context interruption, dedicated
  connection deadlines and idempotent close; retain original destination.
- [x] Add wire-peer tests for stalled opens, capacity/release, shutdown,
  unsupported requests and multiple concurrent streams; prove each critical
  regression can fail without its corresponding behavior.
- [x] Run actual OpenSSH and focused race/static checks, document versions,
  costs and limitations; cross-compile the package for Windows and macOS.

After verification, commit and push this increment to the authorized feature
branch, independently checking its remote SHA as required by the main plan.

## Implemented boundary

The package now also contains an authenticated, staged SOCKS bridge, described in
[the integration plan](ssh-upstream-integration.md). It has no panel/runtime caller
yet. No public SSH outbound can be selected until runtime/editor integration is
implemented. There is no UDP encapsulation, local destination resolution,
direct fallback, transport reuse or new billing source.

Each active flow consumes one upstream TCP connection and SSH handshake. The
pinned SSH implementation advertises a 2 MiB receive window per channel; this is
not a total process-memory bound. The connector adds no unbounded payload queue
and admits at most 128 pending/established flows. Caller buffers, SSH transport
buffers and goroutines still have costs; aggregate RAM/CPU/throughput measurements
remain open integration work. The new bridge bounds all accepted connections,
including authentication, to 512 across at most 32 configured SSH outbounds.
Callers must close returned streams to release their capacity, including after
EOF or timeout. Timers exist only while application operations are pending.

Real OpenSSH forwarding, strict identity rejection and IPv6 are separate from
the Go SSH wire-peer tests for capacity, cancellation, encrypted keys, multiple
host-key algorithms, concurrent writes and deadlines. See
[validation evidence](validation.md) for commands and observed failures.

## Required subsequent Task 5 work

- The private authenticated bridge now enters existing configuration and Runtime
  services. See [runtime boundaries](ssh-upstream-runtime.md) for staged apply,
  validation, selective revocation, rollback and core stop/exit behavior. Public
  upstream health/probe presentation remains open; core readiness is not an SSH
  reachability claim.
- Existing Outbounds editor/API/probes and EN/ZH/fallbacks; no unsupported Xray
  transport options. Protect upstream private keys in administrative settings,
  logs and export surfaces. Carry configuration through backup/restore.
- Actual Xray → bridge → OpenSSH → independent target tests now exercise selected
  egress, block priority, wrong pins, no fallback, concurrent clients, ingress
  shaping/quota and single billing. Full acceptance and unmeasured capacity costs
  remain in the task ledger.
- Node distribution, global policy execution, all other protocols and the full
  A–E task scope remain required; none is satisfied by this connector alone.

## Source evidence

The pinned Xray module's `infra/conf/xray.go` outbound loader has no SSH entry.
The repository's AWG outbound demonstrates conversion to a managed SOCKS bridge;
its lifecycle and validation require adaptation, not automatic reuse.
[`ssh.Client.DialContext`](https://pkg.go.dev/golang.org/x/crypto/ssh#Client.DialContext)
only applies its context until establishment. Its implementation leaves a pending
channel-open goroutine until the transport responds/closes; this connector owns
and closes a dedicated transport on cancellation.
[`ssh.FixedHostKey`](https://pkg.go.dev/golang.org/x/crypto/ssh#FixedHostKey)
accepts only the supplied server key. Standard direct-tcpip is TCP, not UDP.
