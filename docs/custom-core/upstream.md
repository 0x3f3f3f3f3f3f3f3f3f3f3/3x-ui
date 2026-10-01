# Source and version audit

Checked 2026-09-28 against GitHub API and checked-out source, not search snippets alone.

| Item | Immutable baseline / status |
| --- | --- |
| Fork | `0x3f3f3f3f3f3f3f3f3f3f3/3x-ui`, parent and source `MHSanaei/3x-ui` |
| Panel stable reference | `v3.8.5`, `7ef22f94c950ff09f0870e2295fa65ad5968742c` |
| Actual fork main start | `17d7dd46b512d0a9c22921a6094f30c672e436c9`, 64 commits ahead of stable, none behind |
| Upstream main observed | `8c023d13dc9d01a72bf8be6fa409c6af392b8088`, 6 commits ahead of fork main, no fork-only commits in comparison |
| Xray stable reference | `v26.3.27`, `d2758a023cd7f4174a5a5fa4ff66e487d4342ba0`; API `/releases/latest` identifies this non-prerelease |
| Actual panel-compatible core | `v26.9.9`, `52a412d9e2f5c2a5142b1b4e2ab3771dacb8b120`, prerelease; panel already requires pseudo-version `v1.260327.1-0.20260908222543-52a412d9e2f5` |
| Custom core identifier | `26.9.9-custom.1`, source revision included in build metadata; never labeled official Xray |
| Panel/core Go | panel `1.27.1`, core `1.27`; build toolchain `go1.27.1`; default build tags initially |
| Frontend | requires Node 26/npm ≥11; build verified with Node 26.10.0/npm 11.19.1; initial environment Node 22.23.1/npm 10.9.8 |
| Protocol API | planned additive custom capabilities/control API v1; upstream existing APIs retained |
| mieru candidate | official stable `v3.38.0`, `b961978c3be9dd26b94158487c760858e19d1db2`; not yet integrated |
| OpenSnell candidate | latest release `v1.0.4`; source audit main `3100984fd7c3a2bd7b41e292ad41f10d928bfb2d`; release/main feature differences must be resolved before integration |
| sing-snell candidate | `v0.0.0-20260904135315-bc5a12ac736f`, `bc5a12ac736f235b2de2926ecd2791cc925e6b8c`; public v4/v5/v6 TCP/UDP framing; core integration and official interop pending |
| SSH library | panel already pins `golang.org/x/crypto v0.57.0`; core currently `v0.55.0`; choose a single tested build list when adapter lands |

Ruling: preserve fork main and its existing core types rather than downgrade to older stable releases. Record prerelease accurately. Do not merge newer upstream commits into protected branches during this task.

## License and provenance

Panel is GPLv3. Imported Xray retains MPL-2.0 and file-specific notices. No secondary-license-incompatibility notice found in initial Xray source search outside LICENSE (not a substitute for full dependency notice audit). mieru and OpenSnell are GPLv3 candidates; keep their original notices. For a GPL-combined binary, follow MPL §3.3 larger-work conditions, preserving MPL source availability and supplying complete corresponding build source; do not relabel upstream files. See [Mozilla FAQ Q14](https://www.mozilla.org/en-US/MPL/2.0/FAQ/). Complete dependency notices and distribution audit are required before release packaging.

## Primary references

- [Panel releases](https://github.com/MHSanaei/3x-ui/releases), [fork](https://github.com/0x3f3f3f3f3f3f3f3f3f3f3/3x-ui)
- [Core stable](https://github.com/XTLS/Xray-core/releases/tag/v26.3.27), [selected core source](https://github.com/XTLS/Xray-core/tree/52a412d9e2f5c2a5142b1b4e2ab3771dacb8b120)
- [Surge Snell requirements](https://manual.nssurge.com/policies/snell.html), [v6 design/beta](https://nssurge.com/blog/snell-v6/)
- [OpenSnell](https://github.com/missuo/opensnell), [mieru embedded example](https://github.com/enfein/mieru/blob/v3.38.0/test/cmd/exampleapiserver/exampleapiserver.go)

## Deeper dependency audit

OpenSnell commit `3100984f` contains public v4/v5 code, but `SNELL_V6.md` explicitly says its v6 implementation is closed-source. Its installer downloads an official external server. Therefore this repository does **not** provide a usable embedded v6 implementation; its installer cannot satisfy this task. The 2026-10-01 audit identified a separate public sing-snell v6 candidate below. Core integration and interoperability remain required, not marked inapplicable.

At mieru `b961978c`, `apis/server.Accept` returns a connection plus a parsed request and requires `UserContext`; server configuration rejects its own egress settings. This is an appropriate dispatcher integration boundary. `apis/server/interface.go` explicitly states Stop leaves established connections alive; the core adapter must track and terminate them itself. These are source findings, not interoperability results.

Protobuf generation used official protoc 36.2 (download SHA-256 `8b8f18bd2b30346efbc698dd5a73dd7c805f3ef8380f6dfc95c768f3f1852f6a`) and protoc-gen-go v1.36.12. Existing protobuf field numbers remain unchanged. Node archive SHA-256: `7a6353f63eb3d04765004b4adf172616243e4522434635cb1d26288658b04ab5`.


## Durable-store dependency

bbolt v1.5.0, commit `e7a8b2dd498494a3766ba24dd94d3509e5588485`, module `go.etcd.io/bbolt`, checksum `h1:S7GAl7Fxv12yohbwFfIbQCGDWbQbtDGPET4P/bD4lxU=`. MIT license inspected in downloaded source; attribution retained in [third-party notice](../../core/THIRD_PARTY_NOTICES.md). Primary release evidence: [bbolt 1.5 changelog](https://github.com/etcd-io/bbolt/blob/v1.5.0/CHANGELOG/CHANGELOG-1.5.md) and pinned source. The store is embedded in the same core process, with fsync enabled; it is execution state, not another configuration authority.

## Existing AmneziaWG dependency repair

The main baseline pins `github.com/amnezia-vpn/amneziawg-go/v3 v3.1.20260828`,
commit `b5928efb6ca19f0153958460c3d141f04abc5c2e`, module checksum
`h1:D8d8gGvwXcTxUIsE4z6F6vjy4/VZddu95vMNtOygh1c=`. The complete 121-file Go
module source is managed in `core/deps/amneziawg-go`, retaining its MIT license
and file-specific notices. The [manifest](../../core/deps/amneziawg-go.UPSTREAM.json)
records the immutable origin, both modified upstream files and the added regression test.

The full panel race gate exposed an unlocked write in the
[pinned timer callback](https://github.com/amnezia-vpn/amneziawg-go/blob/b5928efb6ca19f0153958460c3d141f04abc5c2e/device/timers.go).
The patch clears the pending duration while holding its existing mutex, before
invoking the callback outside that lock. The observed upstream master still had
the same write on 2026-09-30; no unverified version upgrade is assumed to fix it.
The dependency suite also exposed first-packet loss when a blocked TUN read
retained the old S4 padding while configuration changed. The unchanged upstream
module reproduces the same failure. The send path now reloads padding after the
read and relocates the payload, with an explicit buffer limit; a deterministic
paired-device regression covers increasing, decreasing and clearing padding.
A root-module `replace` makes normal builds and CI use the checked-in repairs.
This preserves the current panel-side runtime; AmneziaWG's single-core migration
remains a separate, unimplemented requirement.


## Native protocol audit update, 2026-10-01

[sing-snell pinned source](https://github.com/SagerNet/sing-snell/tree/bc5a12ac736f235b2de2926ecd2791cc925e6b8c)
provides public v4/v5 and v6 service/client APIs with externally supplied handlers
and dialers. Source archive SHA-256 is
`353891a9f3f6e6cea714d8c815b7eefe5d1750bbf02d7d2373f69d1ab2bbf586`;
license is GPL-3.0-or-later. This is a viable integration candidate, without a
claim of official Surge interoperability. It requires a newer sing dependency,
whose effects on existing adapters need tests. Use one PSK/exclusive listener per
trusted canonical identity; shared-PSK wire user identifiers do not establish
independent billing identities. v5 QUIC is absent here; OpenSnell has a separate
codec candidate, whose stock target-dialing server cannot replace Dispatcher.
v6 unsafe-raw mode does not establish normal PSK authentication.
Evidence: /root/task-evidence/native-snell-source-audit.json.

An independent Go SSH/OpenSSH probe passed local forwarding, dynamic forwarding,
opt-in reverse forwarding, rejected exec/default reverse forwarding and outbound
host-key pin success/failure. It used ephemeral business test keys and no system
sshd. This is library/wire evidence only; native core Dispatcher, shared policy,
lifecycle and panel acceptance remain open. The existing core x/crypto library
provides the adapter boundary; no SSH sidecar or management Git key is involved.
