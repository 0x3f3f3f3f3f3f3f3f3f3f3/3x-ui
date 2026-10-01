# Native Snell TCP/UDP increment evidence

This increment registers typed Snell inbound and outbound handlers in the managed
Xray process. The product neither starts nor depends on an external Snell server.
The original Snell requirement remains open until v5 QUIC and real official
client-to-native-inbound interoperability are verified.

## Compatibility actually exercised

| Version | Native inbound/source client | Native outbound/native server | Native outbound/official ARM64 server | Official client/native inbound |
| --- | --- | --- | --- | --- |
| v4 | TCP, UDP; none/http obfs; first 13k, empty, domain, IPv6; canonical policy | TCP reuse and UDP chains | v4.1.1 archive; none/http, TCP with/without reuse, small UDP, complete first 13k upload | Unverified |
| v5 | TCP, UDP; none/http obfs; first 13k, empty, domain, IPv6; canonical policy | TCP reuse and UDP chains | v5.0.1; none/http, TCP with/without reuse, small UDP, complete first 13k upload | Unverified |
| v6 | TCP, UDP; default/unshaped; first 13k, empty, domain, IPv6; canonical policy | TCP reuse and UDP chains | v6.0.0rc2; default/unshaped, TCP with/without reuse, small UDP, complete first 13k upload; full reply observed | Unverified |

The official v4.1.1 archive's binary identifies itself as v4.1.0 in its banner;
archive and binary hashes identify the tested artifact precisely. The source
client is pinned sing-snell, not a Surge application. These results do not prove
a particular Surge beta/RC pair. The v6 server compatibility claim is limited to
the exact reference artifact below and pinned source implementation.

v5 QUIC Proxy Mode is **unimplemented** here. `quic:true` fails validation. v6
`unsafe-raw` is rejected because it cannot authenticate the listener owner.
The official v5.0.1 fixture accepts an entire first 13000-byte datagram at the
target but truncates its first large UDP reply to its initial record window
(approximately 2.5 KiB). The first-large official test explicitly proves upload;
it does not pretend this fixture proves complete large replies. Native-to-native
first-large replies retain every byte. No fragmentation or warm-up workaround
is introduced.

Deferred Minor from the single review: the v6 first-large fixture probe validates
the complete reply when one is received, but its no-reply timeout branch logs
rather than fails. Its actual observed full reply is evidence, while this test is
not a strict required-reply CI gate. The separate official small-UDP checks do
require replies, and the native complete-datagram tests require all large replies.

## Source and distribution boundary

Complete GPL-3.0-or-later source is retained at `core/deps/sing-snell` from
[SagerNet/sing-snell](https://github.com/SagerNet/sing-snell/tree/bc5a12ac736f235b2de2926ecd2791cc925e6b8c),
commit `bc5a12ac736f235b2de2926ecd2791cc925e6b8c`, module
`v0.0.0-20260904135315-bc5a12ac736f`. Its source archive SHA256 is
`353891a9f3f6e6cea714d8c815b7eefe5d1750bbf02d7d2373f69d1ab2bbf586`.
The immutable manifest records original and managed SHA256 for exactly three
changed files; all 46 upstream files and the complete original LICENSE are
present. Both module replacements point to this managed source, and no module
cache was edited. Combined GPL distributions require corresponding source and
retained MPL notices, as recorded in `core/THIRD_PARTY_NOTICES.md`.

The v4/v5 record writer changes only the UDP complete-record size check from the
TCP growth window to the genuine 16383-byte wire maximum. TCP shaping is
unchanged. For IPv4 requests, the address/request header leaves 16374 payload
bytes; 16375 is rejected and is neither metered nor delivered. Other address
headers reduce the allowed payload accordingly. The v4 packet reader accepts
empty IPv4 replies after the complete seven-byte address header. v6 is unchanged
upstream source and retains its 65535-byte record maximum minus packet headers.

The core sing dependency moves from v0.5.1 to the upstream minimum
`v0.8.12-0.20260727003324-d096a164bc7f`. The root module retains its existing
v0.9.5 selection. Affected callers were tested under core and root module MVS.

## Required native behavior checks

All versions use one authenticated PSK per exclusive listener and map it to one
canonical UUID. Forged wire client IDs share that owner's rate bucket and ledger.
Each decoded UDP destination uses a separate Dispatcher flow; domains receive
core DNS/routing and replies retain actual IPv4/IPv6 source addresses. Metadata
marks empty datagrams, and only payload bytes enter the Client Policy Engine.

Required test names include:

- `TestNativeSnellUDPFirst13kEmptyDomainIPv6SourceAndRoutePolicy`
- `TestNativeSnellOutboundUDPFirst13kEmptyExactLedger`
- `TestNativeSnellInboundHTTPAndV6UnshapedTCPUDP`
- `TestNativeSnellWireIDsShareCanonicalRateAndLedger`
- `TestNativeSnellUDPDisableExpiryQuotaAndCredentialCleanup`
- `TestNativeSnellCredentialRemovalRotationAndSibling`
- `TestNativeSnellQuotaExpiryAndMissingPolicy`
- `TestNativeSnellRespectsHandshakeTimeout`
- `TestNativeSnellUDPWireBoundary`
- `TestOutboundReuseScopeAndCredentialFence`
- `TestOutboundCloseFencesLateDials`
- `TestOutboundCapacityIncludesPendingDials`
- `TestOutboundUDPIdleCleanupUsesPolicy`
- `TestNativeSnellTCPReuseIdleStartsAfterLogicalCompletion`
- `TestNativeSnellUDPDownlinkRefreshesIdle`
- `TestOutboundNonreuseHandshakeWriteUsesPolicy`

Reuse scopes contain credential-generation pointer, inbound tag, supplied dialer
identity, latest gateway resolved before acquisition and socket mark. Revoking a
credential closes only its sockets/pools. The bounded handler tracks at most 128
physical sockets and pending dials combined, 64 pool scopes and 64 destinations per UDP
association. Late successful dials after close are immediately closed. Registry
entries are removed before peer-visible EOF. Stream directions are joined before
reuse completion, and core user-level handshake/idle/half-close timers apply.
The fresh reuse idle deadline begins when the logical stream completes, and
downlink UDP datagrams refresh the idle deadline. A Snell-only
`Content.PreserveTCPHalfClose` opt-in propagates normal upload EOF through
Freedom to a direct TCP socket `CloseWrite`, with no change to legacy defaults.
The initial outbound non-reuse handshake write is bounded before payload copies.

## Commands and actual RED/GREEN logs

Run from `core/xray`, with `PATH=/root/toolchains/bin:$PATH`,
`GOTOOLCHAIN=go1.27.1`, `GOFLAGS=-p=1`; socket tests require local network access.

```sh
SNELL_REFERENCE_DIR=/tmp/native-snell-reference go test -race ./proxy/snell ./infra/conf ./testing/policy -count=1
go test -race ./proxy/freedom ./app/dispatcher ./app/clientpolicy ./common/buf ./transport/pipe -count=1
go test ./testing/scenarios -run 'TestShadowsocks|TestWireguard' -count=1
go test ./common/protocol ./common/singbridge ./proxy/shadowsocks_2022 ./proxy/shadowsocks ./proxy/wireguard ./infra/conf ./app/dispatcher ./app/clientpolicy ./testing/policy -count=1
go vet ./proxy/snell ./infra/conf
go build -o /tmp/native-snell-xray ./main
```

Official probes skip when `SNELL_REFERENCE_DIR` is absent, so CI must supply and
verify the pinned fixtures for these acceptance checks. Every subprocess is a
test fixture and is killed/joined by test cleanup.

| Behavior | Actual RED log | Actual GREEN log |
| --- | --- | --- |
| Typed protocol registration | `/tmp/native-snell-config-red.log` | `/tmp/native-snell-config-green.log` |
| Decoded TCP handler | `/tmp/native-snell-transfer-red.log` | `/tmp/native-snell-transfer-green.log` |
| Reuse scope/generation | `/tmp/native-snell-pool-scope-red.log` | `/tmp/native-snell-pool-scope-green.log` |
| Packet headroom | `/tmp/native-snell-udp-red.log` | `/tmp/native-snell-udp-green.log` |
| Core handshake policy | `/tmp/native-snell-timeout-red.log` | `/tmp/native-snell-timeout-green.log` |
| First-large upstream records against official server | `/tmp/native-snell-official-large-source-red-final.log` | `/tmp/native-snell-official-large-green.log` |
| Empty response/domain source | `/tmp/native-snell-udp-source-red.log` | `/tmp/native-snell-final-native-race.log` |
| Canonical UUID spelling | `/tmp/native-snell-canonical-red.log` | `/tmp/native-snell-final-native-race.log` |
| Malformed typed addresses | `/tmp/native-snell-address-red.log` | `/tmp/native-snell-final-fence-green.log` |
| Outbound UDP idle policy | `/tmp/native-snell-udp-idle-red.log` | `/tmp/native-snell-final-fence-green.log` |
| Pending physical dial capacity | `/tmp/native-snell-capacity-red.log` | `/tmp/native-snell-final-fence-green.log` |
| Long active request then physical TCP reuse | `/tmp/native-snell-review-timeouts-red.log` and `/tmp/native-snell-review-reuse-rootcause.log` | `/tmp/native-snell-review-timeouts-green.log` |
| Continuous UDP downlink idle refresh | `/tmp/native-snell-review-timeouts-red.log` | `/tmp/native-snell-review-timeouts-green.log` |
| Stalled outbound initial handshake | `/tmp/native-snell-review-handshake-red.log` | `/tmp/native-snell-review-timeouts-green.log` |

The pristine-source RED uses a Go overlay mapping only the three managed files to
unchanged audited upstream copies. v4/v5 target bytes are zero, while v6 uploads
13000 bytes successfully; the patched official probe observes all 13000 bytes in
each target. This reproduces a real wire/library defect without reverting code or
altering the module cache. Dependency scenarios, broader affected packages and
root database/web-service regressions are in
`/tmp/native-snell-final-sing-scenarios.log`,
`/tmp/native-snell-affected-regression.log`, and
`/tmp/native-snell-root-mvs-regression.log`.
The one focused review correction pass has fresh all-package native/config/policy
race and shared Freedom/Dispatcher/CPE/buffer/pipe gates in
`/tmp/native-snell-corrected-full-race.log` and
`/tmp/native-snell-corrected-shared-race.log`. The old 69-file review manifest is
retained as the fault baseline; a distinct corrected manifest includes both
additive shared files.

The generic legacy UDP socket hub still uses an 8192-byte reader and drops empty
packets; that independent pre-existing issue is excluded from the direct native
handler fixture. Native UDP paths above use the reviewed shared full-datagram
reader and source-preservation APIs; the direct official first-large probe cannot
be hidden by that hub's truncation.

## Official test artifacts

URLs come directly from [Surge Snell release notes](https://kb.nssurge.com/surge-knowledge-base/release-notes/snell).
Official [Snell policy documentation](https://manual.nssurge.com/policies/snell.html)
and [v6 announcement](https://nssurge.com/blog/snell-v6/) were checked on 2026-10-01.

| Official archive | Archive SHA256 | Executable SHA256 |
| --- | --- | --- |
| [v4.1.1 ARM64](https://dl.nssurge.com/snell/snell-server-v4.1.1-linux-aarch64.zip) | `38d4cdc03dcdb3608af8594df83e1795265167fafc5d802f815148908902d758` | `a6dceb898ade6da58840bf26499a0747894fb1c6407878139c8d863e7926d297` |
| [v5.0.1 ARM64](https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-aarch64.zip) | `2f178bf5ac468ce1a130454efa40a0603fbbe4e47ecc4880a989f4abc7f824cf` | `c9e1cc1f1a86e7d2958f2bc41ff9dc668edf479455a651ea05c6db2c18cd2e4e` |
| [v6.0.0rc2 ARM64](https://dl.nssurge.com/snell/snell-server-v6.0.0rc2-linux-aarch64.zip) | `a0b2915cbc77dc3baf8fa069e741c20808d8a10c3a8a93e709a0a580645c3bd7` | `316c924cb2f7bea75278303265cf004c66379244e101c64ab672a1c987bf8041` |

## Next native v5 QUIC increment

[OpenSnell public source](https://github.com/missuo/opensnell/tree/3100984fd7c3a2bd7b41e292ad41f10d928bfb2d)
provides the v5 envelope codec and real captured Surge Initial fixture at
`components/snell/quic.go` and `quic_test.go`: `decodeQUICEnvelope`/`parseQUICRequest`
and `EncodeQUICEnvelope`, backed by `v4AEAD`/`snellKDF`. Preserve its full GPL
license, immutable source/archive hashes and narrow extraction provenance.

Reuse the authenticated envelope codec, not its directly dialing `ServeQUIC`
relay. A same-port native UDP listener must authenticate the initial envelope,
assign the listener's canonical owner, and create a bounded owned Dispatcher UDP
flow. Subsequent raw QUIC packets belong to that authenticated source association;
both directions require decoded accounting, route policy, atomic idle tracking,
revocation, expiry/quota closure and joined cleanup. Outbound raw UDP must use the
supplied Xray dialer, prepend the official envelope only to the opening packet,
and preserve complete subsequent datagrams. Validate the captured official packet,
wrong-PSK/source reassociation, independent ledgers and official v5 server UDP
interop before claiming this distinct capability. Obtaining and exercising an
actual Surge client remains a separate official-inbound acceptance step.

## Independent parent correction verification

The parent verified all 71 corrected-file SHA256 entries before the final run.
`go test -race -count=1 -v -timeout=120s ./proxy/snell ./infra/conf
./testing/policy -run 'TestOutbound|TestNativeSnell|TestNativeCanonical|
TestNativeMalformed|TestNativeRejects|TestReviewReuseIdle|
TestReviewInboundUDPDownlink|TestReviewNonreuse'` passed with the pinned official
server fixtures. Packages completed in 1.818s/1.466s/18.499s, including all three
versions of nonreuse handshake bounds, reuse after logical completion and UDP
downlink idle refresh. Evidence:
`/root/task-evidence/native-snell-parent-corrected-race.log`.

This scoped core increment has one independent review and one accepted correction
pass. The next QUIC increment remains separate; its official salt-classifier and
repeated-envelope probes must be recorded independently. Panel integration and
actual official Surge inbound acceptance remain open.
