# Isolated Linux transparent-proxy prerequisites — 2026-09-29

The reproducible [probe](../../tools/managed-network/probe_tproxy.py) validates
kernel mechanisms needed by the proposed isolated Snell egress bridge. It is
not a Snell server, a client-policy adapter or a production firewall installer.

Run from the repository on Linux with Python 3, util-linux (`unshare`, `setpriv`),
iproute2 and nftables, with permission to create network/PID namespaces and
administer networking inside them:

```sh
sudo python3 tools/managed-network/probe_tproxy.py
```

The launcher creates disposable network, PID and mount namespaces, mounts a
private `/proc`, and checks its PID view before modifying any network resource.
The second namespace's process-reported identity must match its `/proc/PID/ns/net`
path before any veth or `nsenter` operation. A 45-second outer timeout kills
its owned process group; killing the PID-namespace init also removes its
remaining children. All addresses, routes, rules and both test tables exist only
inside those temporary namespaces in the corrected run. A fixture isolation
failure during development and its cleanup audit are recorded below. No host
routing policy or firewall table is flushed or replaced. Do not copy these rules into
host configuration: they use fixed test-only UIDs, marks, ports and table names.

## Actual result

On Linux `6.17.0-1018-oracle`, aarch64:

| Observed tool | Version/backend |
|---|---|
| nft | 1.0.9 |
| iptables | 1.8.10, nf_tables |
| iptables-nft | 1.8.10, nf_tables |
| iptables-legacy | 1.8.10, legacy |

Only native nft rules were exercised. Availability of the other frontends does
not establish that they are interchangeable, active on the host, or safe to mix.
The unprivileged backend fixture runs as UID/GID 65534 with no supplementary
groups. Its original-direction TCP/UDP packets are marked in an output route
hook, policy-routed locally and delivered through a prerouting TPROXY rule.
`ct direction reply` returns before marking, preserving replies to incoming
connections. The [nftables manual](https://www.netfilter.org/projects/nftables/manpage.html)
defines this direction relative to the tracked connection. The bridge fixture runs
separately from that identity and enables the transparent socket options.

Four real socket exchanges passed: IPv4 TCP and UDP retained
`198.51.100.9:28888`; IPv6 TCP and UDP retained `[2001:db8:1::9]:28888`.
Each sent and received exactly **27 payload bytes**. TCP observes the original
target through the accepted socket; UDP receives original-destination ancillary
data and replies from a transparent socket bound to that address. The connected
UDP client receives the reply from the requested peer. Sources also remain the
fixture's `192.0.2.1` / `2001:db8::1` addresses and actual client ports.

Four additional same-namespace TCP/UDP server-reply controls passed. A further
four IPv4/IPv6 TCP/UDP controls now use a separate network namespace across a
veth pair. The unprivileged server receives the remote address and actual source
port unchanged, and the connected remote client receives exactly **22 bytes**
from the expected server address and port. The fixture addresses are
`10.203.0.1` / `10.203.0.2` and `fd00:3f::1` / `fd00:3f::2`, with server port
28889. These are socket controls, not Snell authentication or wire tests.

With UID-only output marking, the corrected isolated external IPv4 TCP case
timed out at both the client and server accept. Adding only the reply-direction
exclusion made all twelve socket cases pass, while the original-destination
egress cases still prove interception. The final nft counters recorded both
reply exclusion and TCP/UDP interception; no broad established-flow bypass was
added to the original-direction traffic.

`nft --check` accepted the valid batch before application. A deliberately invalid
batch, with a valid chain addition before an invalid rule, failed and left no
partial chain. Deleting only `xui_prereq` preserved the separate
`unrelated_control` table. Namespace exit then removed all fixture resources.

A negative control changed the TCP TPROXY destination from listening port 28080
to unbound port 28082. The first real client timed out and the probe exited 1,
showing that the echo depends on the transparent path. Restoring the script
passed all eight socket cases and both transaction/cleanup checks. An earlier
control-table declaration lacked an nft statement separator and failed before
any payload case; that fixture syntax error was corrected and is not counted
as a passed probe or a kernel capability failure. The first sandboxed version
query also lacked Netlink permission; the isolated privileged run above supplies
the actual version/capability observations.

Initial logs: `/tmp/3x-ui-net-prereq/reproducible-final.json`,
`reproducible-final.err`, and `missing-listener-red.json`. Expanded results:
`external-ingress-isolated-red.json` and `external-ingress-green.json` in the
same directory; both have corresponding `.err` files.

## Fixture isolation correction

The first veth experiment used a PID namespace without a matching procfs mount.
Its `nsenter --target PID` therefore resolved through the outer `/proc` view.
It attempted `ip link set lo up` in the host namespace before failing to find
the test peer interface. This was a test-safety defect, not a failed kernel or
Snell capability check. Existing successful loopback tests establish that `lo`
was already up. A subsequent read-only audit reported flags `0x9` and no
`probe_a` or `probe_b` interfaces remaining; no host firewall or routing command
was issued by the experiment. The initial restricted cleanup audit lacked
permission; the authorized read-only check supplied these observations.

The launcher now uses `--mount-proc`, rejects a procfs PID view that does not
identify itself as PID 1, and verifies the peer namespace identity before its
first networking command. A negative control removed only `--mount-proc` from
a temporary script copy: it exited 1 at the procfs guard before setup. The
corrected script passed the twelve socket cases, atomic-batch rollback and
owned-table cleanup. Failure logs are `external-ingress-red.json` (the original
fixture defect) and `proc-guard-negative.err` (the guard's deliberate rejection).

## Scope of the design decision

The kernel's [transparent proxy documentation](https://docs.kernel.org/networking/tproxy.html)
explains why NAT REDIRECT is insufficient for reliable original-target recovery,
particularly UDP. This probe supports continuing with nft TPROXY and a bounded
userspace bridge for Snell's isolated egress. It does not justify native NAT as
a replacement for payload shaping, durable quota admission or authenticated
client ownership in first-class port forwarding.

Still required: production per-client namespace/process ownership and ingress/
reply separation; original incoming client identity/source handling; DNS and
available domain metadata; real Snell v4/v5/v6 clients and v5 QUIC mode; bridge
limits, payload accounting, live policy and routing; abnormal exit/reconciliation;
multiple instances; packaging and capability gates. The test namespace has no
flowtable/offload configuration and no Docker/UFW/firewalld/Fail2ban rules. Their
coexistence, offload exclusion, and production failure isolation remain
**unverified**. No host firewall compatibility claim follows from this probe.
