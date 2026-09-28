# SSH upstream runtime integration

This is an implemented backend increment of the full task. The dedicated
Outbounds form, node distribution and packaging acceptance are still pending.
The existing outbound probe service now runs real SSH routes as described below.
Do not treat the complete SSH vertical as delivered.

The existing administrator Xray settings service accepts an authored outbound:

```json
{
  "tag": "ssh-exit",
  "protocol": "ssh",
  "settings": {
    "address": "ssh.example.net",
    "port": 22,
    "user": "tunnel",
    "privateKey": "<dedicated OpenSSH private key>",
    "hostKey": "<verified upstream authorized-key-format public key>"
  }
}
```

`privateKeyPassphrase` is optional. Obtain the host public key through a trusted
channel. Use a dedicated forwarding account/key, never the panel's Git credential.
SSH outbounds reject native transport, mux, proxy and send-through fields. Native
outbounds may chain through the SSH tag using their supported
`streamSettings.sockopt.dialerProxy`; the pinned core has removed `proxySettings`.
Standard SSH forwarding transports TCP; UDP requests fail without direct fallback.

## Data path and accounting

An existing Xray routing rule selects the authored tag. Compilation replaces
only that outbound with an authenticated loopback SOCKS outbound:

client → ingress identity, shared shaping and payload admission → Xray routing →
private SOCKS bridge → pinned SSH connection → original target.

The upstream adds no accounting source. Existing managed SSH ingress still owns
its policy identity, raw upload/download and fixed-point billing. Real tests use
OpenSSH at both ends, independent target byte counts, and upstream authentication
records. Other native protocol policy executors remain under the full task.

The loopback bridge defaults to `127.0.0.1:64901`. Override it before starting the
panel with `XUI_SSH_UPSTREAM_BRIDGE_PORT` (integer 1–65535). A conflict is an error;
there is no fallback port. Changing the environment port requires a panel restart.
Maximums remain 32 SSH outbounds, 512 total bridge connections and 128 concurrent
connections per upstream generation. Each forwarded stream uses one SSH transport.

## Preview, application and recovery

Saving or previewing validates and compiles settings without opening an SSH
listener or changing applied ingress credentials. Private bridge credentials are
derived from a process secret and the complete upstream configuration; the core
config never contains the upstream private key. The stored administrative template
does contain that key, like other outbound credentials, so its backups are sensitive.

Runtime application stages new generations alongside applied ones. The installed
Xray binary checks the compiled configuration with `run -test` using a temporary
0600 file, removed after validation. Raw validator diagnostics are suppressed
because malformed configuration diagnostics can contain credentials. The preflight
has a ten-second timeout. Rejection or bridge bind failure retains the working
core, credentials and connections and records a held-back reason.
An unconfirmed stop also refuses replacement; the previous process lifecycle
must finish before the panel can hand control to another child.

SSH upstream activation and removal require the configured core API for startup
verification. The existing API block is retained; the panel does not silently
enable a disabled API. With the API disabled or no usable API listener, the change
is refused before replacing a running core. API-less SSH upstream activation is
not supported by this increment. Native-only operation remains available, including
after an unused SSH preview. This capability boundary must be exposed in the editor.

After a replacement starts, the panel requires a successful StatsService request
within five seconds while the child remains running. Only then are old generations
retired. A failed replacement attempts to start and verify the previous config;
the staged generation rolls back and the held-back reason distinguishes successful
recovery from failed recovery. Full core replacement interrupts its existing flows;
successful recovery permits new connections with the previous configuration.

An upstream pin edit can hot-apply through the existing core API. Revocation resets
the changed bridge sockets so Xray promptly closes the associated client flows;
unchanged upstreams and native flows survive this path. Changing the first/default
outbound still follows Xray's existing full-restart requirement. Normal relays keep
TCP half-close; reset is reserved for explicit retirement/shutdown.

Core stop closes the bridge, clients and connectors. A 250ms watcher also closes
these resources after an unexpected core exit; it skips an ongoing serialized
runtime application. Later reconciliation can recreate the applied bridge. Existing
SSH ingress policy checks continue during core preflight/application; no slow
core operation holds the ingress manager mutex. Only the explicitly staged core
fingerprint is temporarily accepted alongside the applied fingerprint.

Core readiness does not prove SSH upstream reachability. A syntactically valid
wrong pin replaces the old pin and refuses new traffic; it never keeps access
through a superseded pin or falls back directly. The existing outbound test
service can verify a submitted route; continuous upstream health/status and
browser acceptance remain pending.

## Isolated route probes

The existing `testOutbound` and `testOutbounds` service paths compile SSH through
a separate authenticated bridge and temporary Xray process. Modes `http` and
`real` issue actual HTTP requests through the pinned SSH transport; mode `tcp`
also uses HTTP for SSH and reports `mode: "http"`. A listening SSH TCP port alone
does not prove authentication, host-key validity or target access. Protocol IDs
are matched case-insensitively, consistent with other outbound readers.

The requested outbound wins over an older context entry with the same tag.
Only requested outbounds and their transitive proxy-chain dependencies enter the
temporary core configuration. This prevents an unrelated malformed entry from
poisoning an isolated retry. Invalid pins, dead upstreams and failed core starts
return failed results without connecting directly to the target. These are
point-in-time checks of submitted settings, not claims about saved/applied health.

Each batch owns a separate manager and an ephemeral loopback port. The existing
port reservation is released immediately before binding; a lost allocation race
fails visibly. Neither the runtime's fixed bridge port nor its applied connector
generations are reused. Temporary core files are 0600 and contain bridge
credentials, never upstream private keys; completion and failure remove the
files, listeners and owned connections. The existing one-batch semaphore,
50-item limit, 16 HTTP workers, ten-second request timeout and bridge limits apply.
Probes need no core API because the actual HTTP response verifies this temporary
path. The API prerequisite for applied runtime activation remains unchanged.

Administrative authorization and the controller's configured, sanitized public
test URL are unchanged. Real route tests use isolated local targets by calling
the service directly; full browser/API authorization acceptance remains pending.

See [verification evidence](validation.md) and the
[remaining integration plan](ssh-upstream-integration.md).
