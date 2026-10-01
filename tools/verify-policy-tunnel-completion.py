#!/usr/bin/env python3
"""Require real Tunnel and bulk-policy acceptance; skipped fixtures fail closed."""
import re
import sys
from pathlib import Path

if len(sys.argv) != 3 or sys.argv[1] not in ("core", "sqlite", "postgres"):
    raise SystemExit("usage: verify-policy-tunnel-completion.py core|sqlite|postgres LOG")
mode, path = sys.argv[1:]
log = Path(path).read_text()
if re.search(r"^\s*--- (?:FAIL|SKIP):|^FAIL(?:\s|$)|WARNING: DATA RACE", log, re.M):
    raise SystemExit("policy/Tunnel acceptance contains failure, skip or race")
required = {"TestClientPolicyBulkHTTPNativeBindingsAndTunnelAccounting"}
if mode == "core":
    required = {
        "TestTunnelFirstLargeUDPDatagramPreservesTargetAndLedger",
        "TestTunnelFirstEmptyUDPDatagramPreservesTargetAndLedger",
        "TestTunnelConfiguredIPv4IPv6AndDomainTargets",
        "TestTunnelTCPHalfCloseRetainsReplyAndLedger",
        "TestTunnelTCPHalfCloseThroughSelectedProxy",
        "TestTunnelSelectedSocksLargeUploadPreservesTargetAndLedger",
        "TestTunnelSelectedSocksLargeReplyPreservesTargetAndLedger",
        "TestTunnelSelectedSocksEmptyDatagramPreservesTargetAndLedger",
        "TestUDPWriterRespectsWireHeaderLimit",
        "TestNativeSnellOutboundSniffingIdleCancellation",
    }
    for source in ("socks", "http", "tunnel"):
        for version in (4, 5, 6):
            for reuse in ("false", "true"):
                for sniff in ("false", "true"):
                    required.add(f"TestNativeSnellOutboundSniffingIdleCancellation/{source}/v{version}/reuse-{reuse}/sniff-{sniff}")
    for managed in ("false", "true"):
        for size in (13000, 65507):
            required.add(f"TestTunnelFirstLargeUDPDatagramPreservesTargetAndLedger/managed-{managed}/{size}")
        for parent in ("TestTunnelFirstEmptyUDPDatagramPreservesTargetAndLedger", "TestTunnelTCPHalfCloseRetainsReplyAndLedger"):
            required.add(f"{parent}/managed-{managed}")
        for proxy in ("socks", "http", "core-socks", "core-http", "http2"):
            required.add(f"TestTunnelTCPHalfCloseThroughSelectedProxy/managed-{managed}/{proxy}")
    for network in ("tcp", "udp"):
        for address in ("127.0.0.1", "::1", "localhost"):
            required.add(f"TestTunnelConfiguredIPv4IPv6AndDomainTargets/{network}/{address}")
    for direction in ("Upload", "Reply"):
        for size in (13000, 65497):
            required.add(f"TestTunnelSelectedSocksLarge{direction}PreservesTargetAndLedger/{size}")
    for address in ("ipv4", "ipv6", "domain"):
        required.add(f"TestUDPWriterRespectsWireHeaderLimit/{address}")
else:
    backends = set(re.findall(r"bulk traffic policy backend: (\w+)", log))
    if backends != {mode}:
        raise SystemExit(f"expected actual {mode} dialector; observed {sorted(backends)}")
passed = set(re.findall(r"^\s*--- PASS: (Test\S+) ", log, re.M))
missing = required - passed
if missing:
    raise SystemExit("missing policy/Tunnel PASS: " + ", ".join(sorted(missing)))
if not re.search(r"^PASS$", log, re.M):
    raise SystemExit("missing completed Go test result")
print(f"policy/Tunnel {mode}: {len(required)} required cases passed")
