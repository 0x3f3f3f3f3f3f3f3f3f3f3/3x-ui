# Native Snell v5 QUIC implementation plan

This follow-on is isolated at `/tmp/3x-ui-native-snell-quic`, branch
`feature/native-snell-quic`, copied from the corrected 71-file TCP/UDP manifest.
The branch now uses committed parent baseline
`f8ae882b5e1dc950e70859794588a3d01bb06dd9`; advancing the index baseline preserved
every working-file SHA. The reviewed TCP/UDP worktree stays unchanged. Execution is inline under the
existing authorization, with no implementation commits, pushes or new reviewers.

## Required behavior

Official Surge documentation says v5 automatically selects QUIC Proxy Mode for
QUIC traffic; it is a distinct capability from UDP-over-TCP. v4/v6 do not enable
this native UDP listener. v5 ordinary non-QUIC UDP continues using its existing
TCP transport. A v5 listener must accept TCP and UDP on each configured port in
one core. One authenticated PSK per exclusive listener still resolves to one
canonical UUID; wire IDs never create independent accounting identities.

The first authenticated UDP envelope carries the target and an inner QUIC
packet; later raw QUIC datagrams belong to the authenticated source association.
Official v5 also unwraps a repeated authentic envelope on an established
association. Inbound opens a bounded owned Dispatcher flow after authentication,
meters inner datagram bytes, preserves targets/domains/IPv6, applies shared
quota/rate/expiry/disable/remove policy and joins all cleanup. Outbound must use
only the supplied Xray dialer for server UDP, preserve complete datagrams and
exclude envelope overhead from payload policy.

Actual different-target official probes confirm that repeated envelopes retain
the FIRST target for that source; their inner bytes are unwrapped. Native
associations follow that rule, rather than retargeting the source socket.

## Audited codec boundary

The complete public OpenSnell archive is pinned at
`3100984fd7c3a2bd7b41e292ad41f10d928bfb2d`, SHA256
`2532ad83a46375652cf5b78c1a429b7f2390e27731542bf77242977d56a3365e`.
Retain complete GPL source and LICENSE.md under `core/deps/opensnell`; add an
immutable source/extraction manifest and third-party notice. Adapt only envelope
encoding/decoding from `components/snell/quic.go`, the KDF/AEAD helpers from
`cipher.go`/`v4.go`, and the published captured Initial fixture. The core already
has a newer x/crypto than this source requires. Never call `ServeQUIC`, its direct
DNS/dial relay or its racy activity table.

Actual upstream captured-packet codec tests pass. A real official v5.0.1 fixture
probe proved exact opening inner1280, raw1280 and repeated envelope-to-inner1280
loopback transfer after constraining the random salt prefix. The source encoder
has no prefix constraint, and unconstrained salt0xD2 is dropped by official v5.
The controlled four-prefix matrix confirms salt0x33/0xA8 accepted and
salt0x44/0xE5 dropped: clear only fixed-bit0x40 in the first salt byte. No private framing, warm-up or speculative
fragmentation is permitted.

## Native listener choice

Use the existing normal UDP `Process` worker along with the existing TCP worker.
This naturally supports every configured port and avoids an independent runtime
listener service. Its reader is `buf.Reader`, not `net.Conn.Read` (udpConn.Read
panics); its deadline methods are no-ops, so Snell must own an activity timer.
The generic UDP hub currently truncates at8192 and drops empty datagrams. Add a
small explicit full-datagram hub option requested only by this native adapter,
with real behavioral RED/GREEN, preserving ordinary hub defaults. The required
bounded source association checks stay in Snell. Coordinate these necessary
shared changes with root before applying them.

Root authorized both narrow options. `OwnsDatagramTimeouts()` additionally keeps
the legacy generic 120s reaper from shortening a native policy timer. Its actual
controlled-time RED closes only v5 early; GREEN retains the owned source while
v4/v6 default cleanup controls still close it.

## Steps and tests

1. Write and run configuration/native-network RED tests: v5 TCP+UDP, v4/v6 TCP
   only, valid explicit v5 QUIC request, unsupported version/mode failures.
2. Preserve full upstream source/provenance and license; write captured Initial,
   corrupted header/tag/payload, wrong PSK, truncation/oversize, domain/IPv6 and
   encoder prefix tests before implementing the native codec. Verify the exact
   official prefix matrix and repeated envelope behavior.
3. Write actual socket RED tests for source codec -> native inbound, opening
   envelope/later raw/repeated envelope, exact target bytes/source and canonical
   ledger (envelope padding/header excluded); domain and IPv6 routing denies;
   independent listener owner versus forged wire IDs. Implement bounded owned
   associations with per-source Dispatcher flows and duplex activity tracking.
4. Write supplied-dialer RED tests for native outbound -> official v5 UDP server,
   automatic first-Initial selection with non-QUIC UDP retained, per-destination
   source sockets, cancellation/late-dial fence, gateway/mark/credential scope,
   repeated Initial/retries and mode/resource cleanup. Implement the outbound
   datagram bridge, including any wire limitation on reply address metadata.
5. Exercise a real QUIC client/server handshake through the native and official
   paths, then HTTP/3 payload transfer, disable/expiry/quota/rate/removal/sibling
   and idle/close tests. Actual Surge -> native inbound remains separately
   unverified until an official client/device fixture is available.
6. Run native/full policy race, affected UDP-worker/Freedom/Dispatcher/CPE/buffer
   tests, build/vet/protobuf/provenance checks. Record every gap and actual
   compatibility pair; only root controls review and integration.

Steps 1-5 are implemented and verified, including actual official IPv6 and strict
direct first13k replies, two-target source isolation, QUIC v1/v2 handshakes and
complete 90,112-byte HTTP/3 payloads. Step 6 final affected gates passed.
Actual proprietary Surge-client -> native inbound remains unverified and is not
substituted by the source wrapper. Details are in `native-snell-quic-testing.md`.

## Current evidence

- `/tmp/native-snell-quic-upstream-fixture.log`: captured official envelope codec
  and roundtrip under race PASS1.025s.
- `/tmp/native-snell-quic-official-codec-probe.log`: unconstrained source encoder
  fails actual official target transfer.
- `/tmp/native-snell-quic-official-codec-salt-probe.log`: low-prefix envelope and
  raw transfer succeed; old assumption that repeated envelope is forwarded raw
  fails, proving required re-decoding.
- `/tmp/native-snell-quic-official-codec-envelope-raw-green.log`: exact opening,
  raw and repeated authentic envelope transfer PASS.
- `/tmp/native-snell-quic-official-salt-matrix.log`: controlled prefix matrix,
  complete: both fixed-bit-clear classes PASS; both fixed-bit-set classes reject before target dial.
