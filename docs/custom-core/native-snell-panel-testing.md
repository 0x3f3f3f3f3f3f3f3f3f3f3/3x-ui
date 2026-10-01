# Native Snell panel verification

The native Snell panel uses the existing accounts, service forms, outbound forms
and client downloads. Each local listener has one canonical SQL account owner.
`snellPsk` is independent of UUIDs and other protocol passwords. Disabled owners
retain their credential and membership; final detach disables the empty listener
and releases its TCP port and, for version 5, its UDP port. Runtime identities
come from the SQL stable UUID, never from presentation JSON.

## Native client exports

Request `/sub/<subId>?format=snell-surge` for a Surge profile or
`/sub/<subId>?format=snell-json` for a Custom Xray client configuration. The existing
client information modal provides both downloads. Generic URI, JSON subscription
and Clash formats report that a native Snell format is required. There is no
invented `snell://` format.

Both exports read current SQL credentials and preserve every allowed managed
endpoint, including repeated display names, IDN and IPv6 addresses. Endpoint
security wrappers, mux and transport options that native Snell cannot represent
are refused. Explicitly excluded managed hosts do not cause the listener address
to be published as a replacement.

Surge values preserve literal UTF-8, commas, comment characters, quotes and
backslashes using documented quoted-value escapes. Such escaped values require
Surge iOS 5.21.0+ or Mac 6.8.0+. Multiline and control values require the JSON
format. Version 5 QUIC Proxy Mode is selected automatically by Surge and has no
profile QUIC parameter. Native Custom Xray JSON retains the version, PSK, HTTP
obfuscation or v6 mode and explicit outbound v5 QUIC setting. Version 5 listeners
reserve TCP and UDP even when the stored QUIC field is false.

Newline, carriage-return, tab and other representable control characters retain
their exact bytes in JSON with both databases. PostgreSQL text cannot store NUL;
NUL-containing credential preservation is separately verified with SQLite.

The downloaded JSON binds a no-auth SOCKS listener to `127.0.0.1:1080`.
It explicitly enables `udpFullDatagrams` so the local SOCKS relay preserves large
and empty native UDP replies. Older SOCKS configurations retain their default
size and empty-payload behavior. The native Snell outbound also interrupts a
direct SOCKS or HTTP socket source even when sniffing wraps it with a pipe-only
interrupt method. The real-socket idle-cancellation gate covers all 24 combinations
of SOCKS/HTTP, versions 4/5/6, reuse on/off and sniffing on/off. Pipe readers and
reusable inbound half-close semantics retain their own lifetime.

Primary format references:
[Snell policy parameters](https://manual.nssurge.com/policies/snell.html) and
[quoted profile values](https://manual.nssurge.com/profile/format.html).

## Required backend acceptance

Build the native core and a test-only copy with only the trusted Snell identity
capability marker removed:

```sh
bash tools/build-custom-core.sh
bash tools/build-snell-capability-fixture.sh
XRAY_E2E_BINARY="$PWD/build/custom-xray" \
XRAY_PRE_SNELL_MARKER_E2E_BINARY="$PWD/build/pre-snell-marker-xray" \
  go test -count=1 -v ./internal/database/model ./internal/database \
  ./internal/web/service ./internal/xray ./internal/sub \
  github.com/xtls/xray-core/app/clientpolicy/command \
  -run '^TestSnell|^TestCapabilitiesAdvertiseVerifiedNativeSnell$|^TestMigrationModelsMatchPanelModels$' \
  > /tmp/native-snell-panel-sqlite.log
python3 tools/verify-native-snell-panel.py sqlite /tmp/native-snell-panel-sqlite.log
```

Repeat with `XUI_DB_TYPE=postgres` and an actual PostgreSQL `XUI_DB_DSN`, then run
the checker in `postgres` mode. Each package uses an isolated schema; unknown or
public schemas are not removed. The checker requires named PASS results, all four
HTTP transport cases and all six capability startup cases. It refuses failures
and applicable SKIPs. SQLite runs exclude only the three explicitly PostgreSQL
specific cases; those must pass in the PostgreSQL run.

The public HTTP acceptance creates and edits canonical accounts and listeners,
downloads the native JSON, changes only its local SOCKS port and launches the
real core with that configuration. It checks TCP, the first 13 KB UDP payload,
empty UDP, v5 native QUIC, shared Tunnel identity, exact raw and billed bytes,
both directional rate buckets, PSK rotation, disable, expiry, quota renewal,
sibling survival, last-owner removal, port release and restart.

The custom-core workflow requires these SQLite/PostgreSQL gates alongside the
retained SSH and mieru gates. It also checks native source extraction hashes and
the native Snell core tests, including explicit local SOCKS opt-in and legacy
default guards. Pinned official ARM64 server fixtures remain test-only tools;
product handlers do not launch them.

## Interoperability boundary

The pinned official fixtures are v4.1.1, v5.0.1 and v6.0.0rc2, with executable
SHA-256 verification before launch. The v6 first-large test now requires the
complete 13 KB reply and fails on missing replies. The v5.0.1 fixture receives the
complete first 13 KB upload but its first reply record is truncated (2363 bytes
in the current captured run). That reference behavior is recorded separately;
native-to-native first-large replies must still match every byte without a
fragmentation workaround or warm-up packet.

Actual proprietary Surge device acceptance remains unverified. This checkpoint
does not establish packaged-client compatibility or coordinated remote budgets.

## Reviewed clean-source checkpoint — 2026-10-01

Local Snell panel implementation source
`a94e102b16677885e20f84a6c1bcecedef414b2e` was pushed and verified on the
fork's `feature/custom-xray-unified-policy` branch. One whole-panel review
of `a4f82bf6..d442fa36` found one Important issue: sniffing exposed an Interrupt
method without interrupting its SOCKS socket source. The single correction
also covers HTTP CONNECT. All24 source/version/reuse/sniffing combinations
now close at the idle bound; reusable stream and half-close guards pass.

The scoped core race gate has56 top-level PASS results without FAIL/SKIP,
including pinned official references and actual QUIC v1/v2. Shared frontend
verification passed175 files/1807 tests. Type/lint/format, affected static checks
and generated API consistency passed. The earlier frontend timeout, functional
RED runs and PostgreSQL NUL rejection remain in the evidence directory.
Frontend/static/codegen inputs equal the pre-review snapshot. The correction
changes Snell stream cancellation, its test, the CI required name and explanatory
docs. Fresh integrated root Go also passed.

Clean clone `/tmp/native-snell-panel-clean-source-b01zgiw8` used Go1.27.1,
Node26.10.0/npm11.19.1 and shared verified caches. This is a clean source build,
not an independent cold-cache build. Panel reports `dev+a94e102b`; core reports
Custom Xray-core26.9.9-custom.1 with the full source SHA without a dirty suffix,
and retains official mieru v3.38.0.

- `build/x-ui-native-snell-panel-checkpoint`: SHA-256
  `f85102bc4176c7c8af3e2b229cb050dd63c3d7a7adf93b1d651658e73ae315e0`.
- `build/custom-xray-native-snell-panel-checkpoint`: SHA-256
  `5e9a1b77572b5a88e66304d5bb114cb03e534f824dcac3c584d636db436a777d`.

The exact clean core artifact passed47SQLite/50PostgreSQL Snell required checks,
47/49 SSH and35/37 mieru, including public exports, native TCP/UDP/QUIC, shared
policy, business OpenSSH trust and sibling lifetimes. The first combined command
omitted mieru's import package; its two named tests then passed separately.
Original and supplemental logs are preserved; no product change was needed.
All34 prior binaries rehash unchanged. This follow-up changes documentation only.

Evidence: `/root/task-evidence/native-snell-panel-task3-final-receipt.json`,
`native-snell-panel-clean-build-manifest.json`, clean-artifact original/imports/
complete logs, `native-snell-panel-review-core-race.log` and per-SHA push receipts.
The Task3 verifier checks source/log/artifact hashes and exact published feature
HEAD. No deployment, release or default-branch merge occurred.

Installation, upgrade and distribution must still package and preserve this
Custom Xray; official-core replacement is the next priority. Proprietary Surge
devices, remote coordination, legacy migrations and broader acceptance remain
open. This checkpoint does not close those original requirements.
