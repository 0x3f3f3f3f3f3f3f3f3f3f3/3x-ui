# Managed Xray source patches

These patches are a prerequisite under development for complete authenticated
UDP routing. They do not install or replace the panel's configured core.

The source is the unchanged panel dependency pin:

```text
github.com/xtls/xray-core v1.260327.1-0.20260908222543-52a412d9e2f5
source sum h1:BsUC2sCXcdVCb09SUh1iWku0ci779t4bUIlKUor1ZRI=
go.mod sum h1:obbr2WDmr/cpQ/YLe1k0HTULnFXCO0rTWIeSrHoFk3o=
```

Upstream source: <https://github.com/XTLS/Xray-core/tree/52a412d9e2f5>.
Xray source and modifications to its files are under MPL-2.0; see
[LICENSE](LICENSE). Preserve this patch, its source pin, the upstream license
and the applicable modified source when distributing a derived core binary.
No upstream source is modified in the module cache. `prepare.sh` verifies the
Go module cache, copies source into a new directory and applies the patches
with `git apply --check`. Patch drift fails the build.

Use Go 1.27+, Git and a POSIX shell. Output directories must already exist;
source and binary output paths must not exist:

```sh
sh tools/managed-xray/prepare.sh /tmp/xui-core-source
(cd /tmp/xui-core-source && go test -race -count=1 ./common/buf ./transport/pipe ./proxy/trojan ./proxy/freedom)
sh tools/managed-xray/build.sh /tmp/xui-core
XUI_MANAGED_XRAY_E2E_BINARY=/tmp/xui-core go test -race -count=1 ./internal/routedbridge
```

The binary reports `3x-ui-packets-1` alongside the original Xray version. The
build uses the pinned core's own module graph and disables VCS stamping and
local source paths. No release has been published and no installer currently
selects this build. Runtime capability negotiation and packaging are remaining
work; a version label alone does not prove an installed backend is healthy.

Patch 0001 keeps one complete UDP payload in one buffer, sizes framing for a
255-byte domain and the full two-byte payload length, validates packet CRLF,
and preserves empty datagrams independently of raw byte counts. The common
packet reader and freedom response reader use a full-size packet allocation.
UDP buffers are charged at least 8192 bytes of queue capacity, or their actual
allocation if larger. This preserves byte billing at zero for an empty packet
while making finite pipe limits effective. Existing stream queue semantics and
explicit upstream unlimited-buffer settings are retained. A managed bridge
must select a finite buffer policy; UDP Trojan now passes that policy through
to the dispatcher. The pipe may admit one final whole write past its limit,
as before; this is a capacity bound, not an exact byte reservation.

Regression tests in `testdata` are copied into the corresponding upstream
packages. They are run against that patched source; an ordinary panel
`go test ./...` does not execute them. The actual binary tests remain in
`internal/routedbridge` and skip unless `XUI_MANAGED_XRAY_E2E_BINARY` is supplied.

Source-IP response metadata, other UDP outbounds, additional native protocol
packet parsers and public managed-service activation are not established by
these boundary tests. See the [bridge plan](../../docs/managed-services/xray-datagram-bridge.md).

The upstream full suite additionally needs `resources/geoip.dat` and
`resources/geosite.dat`. The verified run used the upstream CI's
`Loyalsoldier/v2ray-rules-dat` source at commit
`f810cb1a484824b94604872b82b1eb74ec7a43c3`. From a new prepared source directory:

```sh
mkdir resources
cd resources
curl --fail --location --remote-name https://raw.githubusercontent.com/Loyalsoldier/v2ray-rules-dat/f810cb1a484824b94604872b82b1eb74ec7a43c3/geoip.dat
curl --fail --location --remote-name https://raw.githubusercontent.com/Loyalsoldier/v2ray-rules-dat/f810cb1a484824b94604872b82b1eb74ec7a43c3/geosite.dat
printf '%s\n' '3cf2236c19063c1c80803368cca5ff589c5033129fdf9ba154230c689b81fc2a  geoip.dat' 'f49b374f424693ea38745aa6175acb6cc303c5ecf7b09980101b21ed8eebfce3  geosite.dat' | sha256sum --check
cd ..
go test -p 1 -count=1 -shuffle=on ./...
```

These assets are test resources and are not automatically installed with the
core. The Linux backpressure fixture changes only its owned socket options,
with a 64 KiB requested buffer at the core input/output and test sender, plus
a 4 KiB stalled receiver. Its two-second write deadline and original 8 MiB
acceptance ceiling distinguish a bounded pipe from an unlimited 32 MiB queue;
they are not general end-user throughput or maximum resident-memory claims.
