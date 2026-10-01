# Native mieru panel acceptance

This scope connects the existing panel to the native mieru v3.38.0 adapter. It
adds no mita process or panel decoder. Server authentication uses the dedicated
mieruUsername/mieruPassword pair on the shared SQL client; display email and
other protocol passwords do not supply authentication. Source pin/license and
core payload/routing/rate evidence remain in mieru-native-design.md and testing.md.

## User workflow

Create a mieru listener in the existing inbound form, choose physical TCP or UDP,
then attach or create an ordinary shared client. Empty native credential fields
on first attachment generate a separate pair once. Editing the display label
preserves the pair; explicitly replacing either native field rotates that
credential generation and closes its old transports.

Use the ordinary share/QR control for official mierus:// links. Client information
provides an official mieru-client.json download from the existing subscription
URL with ?format=mieru. This response uses the upstream ClientConfig protobuf
JSON and preserves eligible native profiles, IPv6/domain endpoints, TCP/UDP,
MTU and credentials. Repeated profile labels are made unique; the first profile
is active and the local official client SOCKS port defaults to1080. Other
protocols in the same subscription are not included in this native format.

The existing outbound form accepts an official simple URL or a single-profile
ClientConfig JSON through its import/JSON controls. Backend outbound subscription
import also accepts the official full-config mieru:// protobuf URI and official
JSON. A single outbound must have one server and one port binding. Multi-server,
multi-port and unmapped profile options are explicitly rejected rather than
silently selected or discarded. Generic Xray JSON and configured Clash/Mihomo
subscriptions currently return406 with format=mieru guidance when an eligible
native listener or simple external native profile is present. This does not
claim stock Xray/Clash support for mieru.

## Actual tests

The public HTTP lifecycle test creates listeners and shared clients through
existing controllers, downloads the official configuration, starts the official
library client and exchanges payload through the actual built custom core.
Both physical TCP and UDP modes pass. The first native/Tunnel exchange has exact
12 upload and12 download payload bytes and48 billed bytes at multiplier2.
Credential rotation closes the old native generation while the sibling and
Tunnel connection survive. Public read/portable export/import preserve the pair
and identity. Expiry closes native and Tunnel flows; disable, quota reduction,
renewal, deletion, restart and listener removal exercise actual socket closure.

The earlier real service CRUD fixture checks retained historical100/200 raw
bytes and300 billed bytes plus new native/Tunnel payload, without repricing
history. Separate SQLite/actual PostgreSQL old-schema/reopen and SQLite→PG→SQLite
backup/export tests preserve native fields, canonical identity, membership and
traffic. Native core TestNativeMieruAndTunnelShareUploadAndDownloadRate supplies
the actual directional-rate evidence; the HTTP fixture is not a throughput
benchmark. Remote coordinated policy remains explicitly rejected for this scope.

Failure logs distinguish genuine missing behavior from fixture mistakes. The
combined pre-export test uses an overlay containing the9e5d8f0 subscription source
and fails with404 before native export selection is wired. An explicit local
HostMapResolver maps the exported localhost endpoint to the real loopback
listener; this is resolver setup, not a replacement for official protocol code.

## Reproduce scoped gates

Use Go1.27.1, Node26/npm11, and the repository toolchain. Build the core and its
source-overlay missing-marker fixture:

```sh
bash tools/build-custom-core.sh
bash tools/build-mieru-capability-fixture.sh
XRAY_E2E_BINARY="$PWD/build/custom-xray" \
XRAY_PRE_MIERU_MARKER_E2E_BINARY="$PWD/build/pre-mieru-marker-xray" \
go test -race -count=1 -v ./internal/database/model ./internal/database \
  ./internal/web/service ./internal/xray ./internal/sub ./internal/util/link \
  github.com/xtls/xray-core/app/clientpolicy/command \
  -run '^TestMieru|^TestCapabilitiesAdvertiseVerifiedNativeMieru' > native-mieru-panel.log
python3 tools/verify-native-mieru-panel.py sqlite native-mieru-panel.log
```

The CI gate requires30 actual SQLite PASS names; PostgreSQL requires32, adding
the actual old-PG schema and cross-database recovery cases. PostgreSQL jobs use
isolated test schemas. Missing binaries, omitted cases or skipped native child
subcases cannot satisfy the gate; PostgreSQL-only skips are excluded from SQLite
acceptance. Capability negotiation tests use empty/populated TCP/UDP listeners
and reject the absent trusted-mieru-client-id-v1 marker before preparation.

Final whole-branch independent review, merged-parent validation and clean-source
panel/core distribution provenance are recorded after these scoped gates pass.
