# Snell backend prerequisites — 2026-09-29

This records official documentation and isolated binary observations. Snell
panel integration, authenticated payload forwarding, policy enforcement and
real Surge interoperability are **not implemented or verified by this audit**.

## Version and transport targets

| Requested protocol | Official server asset | ARM64 program banner | Native listeners observed | Client evidence |
|---|---|---|---|---|
| v4 | 4.1.1 | **4.1.0**, Sep 21 2024 | TCP | Real Surge pairing not executed |
| v5 | 5.0.1 | 5.0.1, Nov 19 2025 | TCP and UDP on the same port | Real Surge pairing not executed |
| v6 | 6.0.0rc2, **beta** | 6.0.0, Aug 7 2026 | TCP, including simultaneous IPv4/IPv6 listeners | Real Surge pairing not executed |

The v4 asset/banner discrepancy remains unresolved. An installer must retain
the requested asset identifier, observed binary version and exact hash separately.
The v6 banner's lack of an RC suffix does not make the downloaded beta stable.
Server binaries remain separate until actual client compatibility is demonstrated.

Ordinary UDP uses TCP carriage. Only v5 has QUIC Proxy Mode, with UDP carriage
selected for QUIC traffic. **v6 removes that mode**; the earlier design wording
that grouped v5/v6 QUIC mode was incorrect. The official v6 announcement explains
the removal. [Official release notes](https://kb.nssurge.com/surge-knowledge-base/zh/release-notes/snell),
[v6 announcement](https://nssurge.com/blog/snell-v6/).

Surge's current manual lists v6 support from iOS 5.20.0 and Mac 6.7.0, but warns
that beta compatibility may change. Export must specify `version` explicitly
because its default is 1. Connection reuse is optional for v4 and later; v6
does not support the obfuscation options of earlier versions. The default v6
mode preserves encryption; this integration has no requirement for its unsafe
debug mode. These are documentation requirements, not a tested Surge build
pairing. [Official client syntax](https://manual.nssurge.com/policies/snell.html).

## Download and licensing observations

All 11 architecture/version combinations listed by the official release page
were downloaded over HTTPS and inspected. Each ZIP contains only `snell-server`.
ELF machine values match amd64, i386, aarch64 and armv7l for v4/v5, and amd64,
i386 and aarch64 for v6. No v6 armv7l download is listed; none was invented.
The programs were executed only on this environment's ARM64 architecture.

The [complete observed manifest](snell-observed-assets.json) retains all 11
download URLs, architectures, archive/binary hashes and sizes. The values below
identify the downloaded ARM64 artifacts.
They are locally measured hashes of official HTTPS downloads, **not an
independent publisher signature or signed checksum manifest**.

| Asset | ZIP SHA-256 | Executable SHA-256 |
|---|---|---|
| 4.1.1 aarch64 | `38d4cdc03dcdb3608af8594df83e1795265167fafc5d802f815148908902d758` | `a6dceb898ade6da58840bf26499a0747894fb1c6407878139c8d863e7926d297` |
| 5.0.1 aarch64 | `2f178bf5ac468ce1a130454efa40a0603fbbe4e47ecc4880a989f4abc7f824cf` | `c9e1cc1f1a86e7d2958f2bc41ff9dc668edf479455a651ea05c6db2c18cd2e4e` |
| 6.0.0rc2 aarch64 | `a0b2915cbc77dc3baf8fa069e741c20808d8a10c3a8a93e709a0a580645c3bd7` | `316c924cb2f7bea75278303265cf004c66379244e101c64ab672a1c987bf8041` |

For each version, `--license` prints third-party libuv and libsodium notices.
It does not establish a redistribution grant for Snell itself. No proprietary
binary is committed or bundled; installation should obtain the pinned asset
directly from the official host. Licensing review remains qualified accordingly.

The ZIP URL pattern is
`https://dl.nssurge.com/snell/snell-server-v<VERSION>-linux-<ARCH>.zip`, restricted
to the exact combinations linked by the official release page. Complete local
observations are also retained locally in
`/tmp/3x-ui-snell-audit-20260929/observed-manifest-all-architectures.json`.

## Isolated startup observations

This host provides `unshare`, `ip`, `nft` and `iptables`; the effective and bounding
capability masks observed were `000001ffffffffff`. A disposable `unshare --net`
namespace initially contained only a down loopback interface. Startup probes
enabled that namespace's loopback and ran each binary as UID/GID 65534, without
supplementary groups, with `no-new-privs` and a cleared environment. No host
interface, route, firewall, service or production deployment was changed.

The generated private configuration contained `[snell-server]`, an exclusive
loopback `listen`, a newly generated UUID PSK, and loopback DNS. Each process
accepted a TCP socket connection and terminated on SIGTERM. v6 also accepted
connections on its second IPv6 listener. No Snell authentication or payload
transfer was attempted, and the namespace had no external route.

| Asset | Idle RSS snapshot | SIGTERM exit | Observed exit delay |
|---|---|---|---|
| 4.1.1 | 5984 KiB | signal 15 | 1.144 ms |
| 5.0.1 | 5996 KiB | signal 15 | 2.765 ms |
| 6.0.0rc2 | 4652 KiB | signal 15 | 1.173 ms |

These single idle observations are not memory or shutdown bounds under load.
Per-client instance counts, concurrency, namespace/relay overhead, stream and
datagram accounting, source/destination preservation, routes and revocation
still require implementation and measurements. Real Surge clients are absent
from this Linux environment; mock or third-party clients cannot substitute for
the required per-version interoperability results.

Local probe files are under `/tmp/3x-ui-snell-audit-20260929/`: `probe_config.py`,
`runtime/config-observations.json`, and each version's help/license log. The
first startup probe failed on a mistaken `/usr/sbin/ss` path; its owned process
was cleaned up. The corrected probe used `/usr/bin/ss` through PATH and completed
for all three versions. This diagnostic failure is not counted as a backend
compatibility failure or a passing acceptance test.

The later [isolated Linux networking probe](linux-network-prerequisites.md)
adds real IPv4/IPv6 TCP/UDP original-target and reply evidence for the proposed
transparent egress mechanism, plus nft transaction/owned-cleanup checks. It does
not authenticate a Snell session or close the missing real-Surge acceptance.
