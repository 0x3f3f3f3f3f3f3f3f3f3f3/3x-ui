# Native Snell v5 QUIC increment evidence

This increment adds v5 QUIC Proxy Mode inside the managed Xray process. It does
not launch or depend on a second server. The TCP/UDP increment committed at
`f8ae882b5e1dc950e70859794588a3d01bb06dd9` is the baseline of this isolated
`feature/native-snell-quic` worktree. Parent integration and the independent QUIC
review are separate gates. Actual proprietary Surge client -> native inbound
acceptance remains unverified.

## Delivered behavior

v5 automatically listens on TCP and UDP on every configured native port. v4/v6
keep TCP and their UDP-over-TCP path. Explicit `quic: true` is valid only for v5;
v5 automatic selection also works without that option, matching the
[official Snell policy](https://manual.nssurge.com/policies/snell.html), checked
2026-10-01. Outbound recognizes the public v1/v2 Initial long-header structure
defined in [RFC 9000](https://www.rfc-editor.org/rfc/rfc9000.html#section-17.2.2)
and [RFC 9369](https://www.rfc-editor.org/rfc/rfc9369.html#section-3).
Ordinary UDP still uses authenticated TCP. Each outbound target has its own
physical server socket; QUIC sockets never enter cross-request reuse pools.

The first authentic envelope fixes the target for its UDP source. Later raw
packets use that binding. Repeated authentic envelopes are decrypted again and
their inner packets still use the first target, even when their envelope names a
different target. A new source requires its own authenticated envelope. Wire IDs
remain untrusted metadata; every accepted source resolves to the exclusive
listener's canonical UUID.

Inbound calls Dispatcher only after authenticating the PSK, inherits sniffing,
preserves the actual UDP response metadata inside the core and meters decoded
datagrams. Shared policy controls upload/download rates, quota, expiry, disable
and cleanup. Inbound physical TCP and QUIC transports share a 128-slot limit;
outbound active plus pending transports share 128 slots; at most 128 packet requests
may also wait for their first payload, with 64 destinations
per datagram request. Authentication and first writes obey the handshake policy.
Successful uploads and downloads renew the native idle timer. Cancellation and
credential/handler removal close owned sockets, interrupt links and join copy
goroutines; late successful dials are closed immediately.

Two small opt-ins are used only by native Snell v5: `PreserveDatagrams()` requests
the full 65535-byte UDP hub buffer and preservation of empty datagrams;
`OwnsDatagramTimeouts()` prevents the generic worker's legacy 120s reaper from
overriding the native policy timer. Default hub truncation/drop behavior and
default worker reaping are preserved by behavioral controls. An empty packet is
preserved by the listener but cannot establish a QUIC association. Empty
ordinary UDP remains covered by the TCP/UDP native regression tests.

## Source and wire corrections

Complete GPL-3.0-or-later OpenSnell source is retained under
`core/deps/opensnell`, pinned to
[`3100984fd7c3a2bd7b41e292ad41f10d928bfb2d`](https://github.com/missuo/opensnell/tree/3100984fd7c3a2bd7b41e292ad41f10d928bfb2d).
The archive SHA256 is
`2532ad83a46375652cf5b78c1a429b7f2390e27731542bf77242977d56a3365e`.
All 42 original files are unmodified; their hashes, full license path, extracted
codec/capture hashes and changes are recorded in
`core/deps/opensnell.UPSTREAM.json`. Only the pure envelope format and captured
test fixture are adapted; upstream `ServeQUIC`, its DNS/dial relay and activity
table are unused. Existing pinned sing-snell KDF/AES-GCM primitives and existing
quic-go dependencies are used without a module or API change.

Actual official v5.0.1 fixture probes established three compatibility details:

- Salt first-byte prefixes `0x33` and `0xA8` work; `0x44` and `0xE5` are dropped.
  Clear only fixed bit `0x40` in the random salt so both remaining prefix classes
  retain their randomness.
- Repeated envelopes are unwrapped, while the original source target remains
  fixed. The initial different-target retarget assumption failed against the
  official fixture; corrected original-binding assertions pass.
- Envelope IPv6 hosts must be literal `::1`, not Xray's bracketed `[::1]` display
  form. The original native encoding failed with the official log
  `Misformatted domain name`; unbracketed IPv6 passes.

Bounds and authenticated command/header/host fields are checked before
Dispatcher admission. Whitespace/control hosts are rejected before ParseAddress
can normalize them. No fragmentation or warm-up framing is introduced.

## Compatibility and remaining limits

| Compatibility pair | Actual evidence |
| --- | --- |
| Published captured Surge Initial -> native codec | Correct cloudflare target and complete 1280-byte real Initial; corrupted/truncated/wrong-PSK cases rejected |
| Independent source wire client -> native inbound | Opening, raw and repeated packets; first13k, domain/IPv6, source isolation, route denies and canonical lifecycle |
| Real quic-go -> source envelope wrapper -> native inbound | QUIC v1/v2 TLS handshakes and complete 90,112-byte HTTP/3 POST echo |
| Native codec -> official v5.0.1 | Exact opening/raw/repeated, fixed-source binding and IPv6 reply |
| Native outbound -> official v5.0.1 | Two distinct IPv4/IPv6 first13k uploads AND replies; target sockets remain separate; ordinary UDP uses TCP |
| Real quic-go -> managed ingress -> native outbound -> official v5.0.1 | QUIC v1/v2 TLS handshakes and complete 90,112-byte HTTP/3 POST echo with canonical accounting |
| Proprietary official Surge client -> native inbound | Unverified; an actual client/device fixture is still required |

The reference executable is a test-only subprocess, pinned by its SHA256
`c9e1cc1f1a86e7d2958f2bc41ff9dc668edf479455a651ea05c6db2c18cd2e4e`.
Its [official ARM64 archive](https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-aarch64.zip)
and all earlier fixture hashes remain documented in `native-snell-testing.md`.

The raw QUIC response wire carries no target-address field. Outbound retains
the verified request target; when the remote server resolves a domain, it cannot
truthfully report that remote resolved IP from this wire. Inbound Dispatcher
still retains actual response IP metadata inside the core. Fixed-bit-clear raw
QUIC packets cannot use the observed official classifier and are explicitly
rejected outbound. Other QUIC versions use ordinary UDP rather than an invented
native format. The codec bounds its complete envelope to 65535 bytes; the system
UDP socket's smaller family-specific wire limit can return an explicit write
error. Existing TCP-record max16383 and official v5 TCP first-large reply
truncation remain separate limitations. The deferred v6 official first-large
test still has its previously recorded missing-reply assertion Minor.

## Required tests and commands

`custom-core.yml` now requires named native13k/empty/domain/IPv6, QUIC identity,
route, source-isolation, quota/rate/revocation/reclaim, capacity, handshake and
default-worker controls. Its source-only CI run requires both native inbound
real HTTP/3 versions. Official ARM64 fixture tests are opt-in and skip when
`SNELL_REFERENCE_DIR` is absent; the local final run sets it explicitly.

```sh
cd core/xray
SNELL_REFERENCE_DIR=/tmp/native-snell-reference go test -race -shuffle=on -count=1 -v ./proxy/snell ./infra/conf ./testing/policy ./transport/internet/udp ./app/proxyman/inbound -run '^TestNativeSnell|^TestOutbound|^TestHubFullDatagrams|^TestUDPWorkerNativeTimeout'
go test -race ./app/proxyman/inbound ./transport/internet/udp ./proxy/freedom ./app/dispatcher ./app/clientpolicy/... ./common/buf ./transport/pipe -count=1
go vet ./proxy/snell ./infra/conf ./app/proxyman/inbound ./transport/internet/udp
go build -o /tmp/native-snell-quic-xray ./main
```

Complete logs and the final frozen file hashes are retained under
`/root/task-evidence`. The review evidence JSON lists every executed command,
actual RED/GREEN log, named acceptance test and fixture/source pin. The complete
Snell/config/policy suite before the final worker opt-in passed under race,
including all earlier v4/v5/v6 timeout/UDP regressions (policy49.128s). The final
affected CI/shared gates passed after that opt-in. The last frozen-source CI
run also includes missing/initially-disabled/expired fresh-source rejection.

Final shuffled CI race packages completed in proxy 3.890s/config 1.356s/policy 35.480s/
UDP 1.332s/inbound-worker 1.272s, with all 16 required top-level names and both
native HTTP/3 version subtests present and zero SKIPs. Final shared race packages
passed (worker 1.203s/hub 2.250s/Dispatcher 2.206s/CPE 6.025s/buffer 1.848s/pipe 3.130s);
Freedom compiled with no standalone tests. Build and vet exited 0. Complete
42 upstream, 2 extracted and 3 managed sing-snell file hashes, YAML parsing, Go formatting,
unchanged protobuf and whitespace checks passed.
Root-module selected dependency versions also compiled the native code with
`go test ./internal/xray -run '^$' -count=1` (0.136s; compile gate, no tests run).

## Single independent review and correction

The frozen 67-file candidate was reviewed read-only against HEAD f8ae882b,
manifest SHA256 5bb3568b0f3aab14bab70b957f6ec0d35dd11b8e6f1e6a9a20f8c7c5555b76b8.
One Important initial-dial timer defect and two Minor adapter/host-validation
gaps reproduced independently. The regression file reproduces all three before
the correction (including a 50.35ms early cancellation against a 500ms handshake
allowance). All three regressions, the concurrent 128-request control, and
previous late-dial/first-write controls pass under race after the correction.
Full logs: native-snell-quic-review-correction-{red,green}.log.

The native v5 request now registers bounded cancellation and credential ownership
before reading any payload. Its initial read/dial/write phase uses Handshake;
after the first successful write normal idle and directional timeout tracking
starts. TCP/v4/v6 retain the existing default copy timer. Raw target whitespace
and control characters are rejected throughout the host before normalization.
The direct-adapter pre-first-packet Close/revoke gap was mitigated by outer
proxyman/managed Dispatcher cancellation; it was not evidence of a panel-removal
leak. These changes do not establish proprietary Surge-client acceptance.

The corrected shuffled native race run passed all 20 required top-level CI
names with the official ARM64 fixture enabled and zero skips. Corrected vet
and a separately named build both exited 0; logs are
native-snell-quic-review-corrected-{ci-race,vet,build}.log. Parent-module combined
regression and clean-source distribution remain subsequent gates.

## Parent clean checkpoint

Merged Snell QUIC commit7a822795 is included in clean sourceab997b39 alongside
the verified mieru panel and existing SSH core. The distinct panel/core artifact
paths, hashes, clone/toolchain details,30-artifact preservation and exact fork
push receipt are recorded in mieru-panel-testing.md. Proprietary Surge inbound,
SSH/Snell panel and broader whole-system migration remain incomplete.
