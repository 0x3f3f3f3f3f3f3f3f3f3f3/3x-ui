# Shared billing, directional rates and Tunnel acceptance

This stage completes the missing bulk client policy form and fixes reproduced
Tunnel data-path defects after the native Snell, mieru and SSH panel checkpoints.
It uses the existing canonical client identity, core policy engine and SQL ledger.

## Product behavior

Bulk creation accepts separate upload/download byte limits and an exact decimal
multiplier for each generated account. Blank settings preserve legacy omission;
explicit zero rates mean unlimited. A multiplier remains a string with at most
six decimal places, greater than zero and no more than 1000. Remote or missing
listener bindings refuse explicit policy instead of silently discarding it.
Snell exclusive ownership and independent SSH business credentials remain intact.

Tunnel/dokodemo-door now opts into the existing full-datagram listener. Previously
the first 13,000-byte packet reached the target as 8,192 bytes; empty packets were
dropped. The empty-packet source placeholder is converted to the fixed forwarding
target, while original-destination forwarding retains its existing metadata.
Reader interruption/closure still reaches the underlying UDP association. Packet
destinations are copied separately so outbound resolution cannot share mutable
metadata between packets. Both ordinary and managed forwarding use this behavior.

For TCP forwarding, a per-request copy of the existing content context enables
the core's half-close handling. Direct, SOCKS, HTTP/1 CONNECT and HTTP/2 CONNECT targets receive FIN and can
return their final response. SOCKS/HTTP CONNECT inbounds retain this signal when
this core itself is the upstream proxy. HTTP/2 ends only the request body and
preserves the response and shared connection. Previously that response was lost while the target waited for
EOF. Forwarding continues through Xray routing and existing fixed-outbound and
source-ACL controls; no NAT rule is required by these tests.

The SOCKS outbound now preserves full datagrams on both encoding and decoding.
Wire headers count toward its portable 65,507-byte UDP payload limit: maximum
IPv4-address, IPv6-address and `localhost`-domain payloads are 65,497, 65,485 and
65,491 bytes respectively. Larger encoded packets return an explicit error rather
than becoming empty writes. A full-datagram-capable upstream is required; these
SOCKS tests enable `udpFullDatagrams` on the upstream mixed listener.

## Evidence matrix

| Requirement | Named evidence | Current result |
| --- | --- | --- |
| Bulk exact policy, independent accounts, omission and unsupported scope | `client-bulk-policy-form.test.tsx` plus existing individual/native bulk/renewal/schema tests | 49 related tests and the full 177-file/1819-test frontend suite passed |
| Bulk authenticated controller → SQL → actual native core → two owned Tunnel rules | `TestClientPolicyBulkHTTPNativeBindingsAndTunnelAccounting` | SQLite and isolated PostgreSQL passed using the UDP-fixed review binary |
| Native Snell plus Tunnel shared exact fractional billing | Same public HTTP test | Passed at multiplier `1.234567`, then `2.5`, retaining historical billed fractions |
| Restart, period reset and sibling independence | Same public HTTP test | Passed; lifetime retained, new period starts at zero, sibling survives disable |
| First complete 13 KB and maximum ordinary IPv4 UDP payload | `TestTunnelFirstLargeUDPDatagramPreservesTargetAndLedger` | Ordinary and managed 13,000/65,507-byte cases passed in the final core race run |
| First empty UDP packet reaches target and returns | `TestTunnelFirstEmptyUDPDatagramPreservesTargetAndLedger` | Ordinary and managed cases passed, without payload charge |
| IPv4, IPv6 and domain targets | `TestTunnelConfiguredIPv4IPv6AndDomainTargets` | All six TCP/UDP cases passed, with exact shared billing |
| TCP FIN and final direct-target response | `TestTunnelTCPHalfCloseRetainsReplyAndLedger` | Ordinary and managed cases passed with exact payload billing |
| Selected SOCKS/HTTP/HTTP2 FIN, large UDP in each direction and wire limits | New selected-proxy tests and `TestUDPWriterRespectsWireHeaderLimit` | All 77 required core/adapter/Snell cases passed under race, zero skips |
| Routing inheritance, selected SOCKS/HTTP/loopback, fixed outbound, block and no direct fallback | Existing `TestTunnel*` policy tests | Final focused core run passed |
| One canonical budget across TCP/UDP and native protocols, hot rates and disconnect | Existing Tunnel, authenticated/password proxy, Snell/QUIC and SSH shared-policy tests; native mieru tests | 35 policy/adapter parents after review plus 121 policy/listener/mieru parents passed under race, zero skips |
| 100 MiB quota at multiplier 2 | `TestTunnelQuotaStopsExistingFlowsAndReconnectAtHundredMiB` | Passed in final focused core run; 50 MiB admitted bidirectional raw traffic exhausts the quota |
| Two nonzero upload/download rate tiers and unlimited control | `tools/test-custom-tunnel-rates.py` | Fresh clean-artifact measurement pending |
| Rebuilt panel/core and final protocol HTTP gates | Clean-source build and native public HTTP acceptance | Pending final review/build |

Admission precedes writing payload to the next hop. Immediate quota termination
can discard the final admitted buffers before application delivery. The original
100 MiB/2× echo observation retained an 8,192-byte admitted/delivered difference,
within that test's declared 64 KiB uncertainty. It does not establish a universal
delivered-byte error bound. Empty datagrams preserve their boundaries and do not
start first-use expiry or consume payload quota.

The full core regression exposed an additional Snell cancellation regression:
the EOF flag had also been used as a physical input-socket ownership signal.
SOCKS/HTTP and Tunnel TCP connections own one request even when preserving FIN;
reusable native streams retain their independent lifetime. The corrected native
Snell test passes all 36 source/version/reuse/sniffing combinations. Failed full
and isolated runs remain retained; the final complete rerun is pending.

The new CI verifier requires named core cases and actual bulk HTTP PASS results
on both SQLite and PostgreSQL, including the actual Gorm dialector marker.
Failures, data races, skipped fixtures and missing backend markers are rejected.
The required core list also includes all 36 native Snell source-cancellation cases.
The pre-marker review logs intentionally cannot satisfy these final gates.
A general Snell/policy race run omitted `SNELL_REFERENCE_DIR` and skipped 26
official-fixture cases; it is retained but cannot close those interoperability
gates. The first explicit pinned-fixture run had one v5 outbound timeout;
25 isolated repeats passed. A controlled equal-port fixture reproduced recursive
forwarding failure. Tests now ensure distinct native-server and forwarding ports
and report their values. The original failure is consistent with port collision,
but its log did not record both ports, so that cause cannot be proved for that
specific run. The fresh complete pinned-fixture rerun passed all 114 parent tests with zero skips or races.

Local logs remain under `/root/task-evidence/policy-tunnel-*`. Original failing
bulk-form, large/empty UDP, metadata-trace and half-close runs are retained; later
passing runs do not overwrite them. The first review core binary precedes the
half-close fix and is a review artifact, not the final clean-source checkpoint.

## Reproduction

Use the pinned Go/Node toolchains and a built Custom Xray-core. The public HTTP
tests require `XRAY_E2E_BINARY`; an unset variable skips them and does not count
as acceptance. Set `XUI_DB_TYPE=postgres` and an actual test `XUI_DB_DSN` for the
second database run. Existing test helpers isolate their own schemas.

```sh
XRAY_E2E_BINARY="$PWD/build/custom-xray" go test -race -count=1 -v ./internal/sub \
  -run '^TestClientPolicyBulkHTTPNativeBindingsAndTunnelAccounting$' | tee /tmp/policy-tunnel-sqlite.log
python3 tools/verify-policy-tunnel-completion.py sqlite /tmp/policy-tunnel-sqlite.log
go test -race -count=1 -v github.com/xtls/xray-core/testing/policy \
  -run '^TestTunnel|^TestQuotaWindowConfig|^TestAuthenticatedProtocolsShare|^TestPasswordMixedAliasesShare|^TestSSHAndTunnelShare|^TestNativeSnellWireIDsShare|^TestNativeSnellV5QUICWireIDsShare'
python3 tools/test-custom-tunnel-rates.py --binary build/custom-xray
```

These local results do not complete coordinated cross-node/global policies,
installer/Docker distribution, restore allocation fencing, other-platform gates,
or commercial-device interoperability. Those remain original requirements and
must retain their own acceptance evidence.
