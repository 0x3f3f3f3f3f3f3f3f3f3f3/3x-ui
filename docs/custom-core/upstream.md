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

OpenSnell commit `3100984f` contains public v4/v5 code, but `SNELL_V6.md` explicitly says its v6 implementation is closed-source. Its installer downloads an official external server. Therefore this repository does **not** provide a usable embedded v6 implementation; its installer cannot satisfy this task. Independent lawful compatibility work/library evaluation remains required, not marked inapplicable.

At mieru `b961978c`, `apis/server.Accept` returns a connection plus a parsed request and requires `UserContext`; server configuration rejects its own egress settings. This is an appropriate dispatcher integration boundary. `apis/server/interface.go` explicitly states Stop leaves established connections alive; the core adapter must track and terminate them itself. These are source findings, not interoperability results.

Protobuf generation used official protoc 36.2 (download SHA-256 `8b8f18bd2b30346efbc698dd5a73dd7c805f3ef8380f6dfc95c768f3f1852f6a`) and protoc-gen-go v1.36.12. Existing protobuf field numbers remain unchanged. Node archive SHA-256: `7a6353f63eb3d04765004b4adf172616243e4522434635cb1d26288658b04ab5`.


## Durable-store dependency

bbolt v1.5.0, commit `e7a8b2dd498494a3766ba24dd94d3509e5588485`, module `go.etcd.io/bbolt`, checksum `h1:S7GAl7Fxv12yohbwFfIbQCGDWbQbtDGPET4P/bD4lxU=`. MIT license inspected in downloaded source; attribution retained in [third-party notice](../../core/THIRD_PARTY_NOTICES.md). Primary release evidence: [bbolt 1.5 changelog](https://github.com/etcd-io/bbolt/blob/v1.5.0/CHANGELOG/CHANGELOG-1.5.md) and pinned source. The store is embedded in the same core process, with fsync enabled; it is execution state, not another configuration authority.
