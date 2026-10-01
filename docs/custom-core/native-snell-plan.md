# Native Snell Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax. Execution is inline and already authorized; keep implementation uncommitted for the root's single review.

**Goal:** Add managed in-process Snell v4/v5/v6 stream and UDP adapters with real routing, accounting and lifecycle tests.

**Architecture:** Native Xray typed handlers consume sing-snell decoded callbacks; one authenticated listener PSK resolves to a canonical UUID. UDP uses bounded destination-specific Dispatcher links, and outbounds use the supplied Xray dialer.

**Tech Stack:** Go 1.27.1, protobuf 36.2, sing-snell `v0.0.0-20260904135315-bc5a12ac736f`, existing Dispatcher/Client Policy Engine.

**Spec:** [native-snell-design.md](native-snell-design.md), [requirements.md](requirements.md) section six.

## Global Constraints

- Work only in `/tmp/3x-ui-native-snell-core`, branch `feature/native-snell-core`, baseline `a00a7b5cc8e2c133366dfea02f0fb9e33bac3667`.
- No panel work, external runtime servers, private protocols, unsafe raw mode, wire-ID billing identities, pushes or deployment.
- Preserve existing protocol APIs and license notices. v5 QUIC is unimplemented and must fail explicitly.
- Tests precede behavior implementation; preserve RED/GREEN logs. Only this plan is locally committed before implementation; root controls review and final commits.

## Review Focus

- Reuse cleanup must not close a later logical request; callbacks finish exactly once after both directions.
- Domain/IPv6/empty/large UDP datagrams retain routing and bytes.
- Wrong PSK, idle UDP, removed credentials and owner disable cannot continue managed traffic.
- Library optimized interfaces cannot bypass decoded metering.
- sing dependency reconciliation cannot regress existing Shadowsocks/WireGuard/TUN consumers.

### Task 1: Typed configuration and dependency boundary

**Files:** `core/xray/proxy/snell/{config.proto,config.pb.go,account.go,config_test.go}`, `core/xray/infra/conf/{snell.go,snell_test.go,xray.go}`, `core/xray/main/distro/all/all.go`, module manifests, `core/THIRD_PARTY_NOTICES.md`, source provenance.

**Interfaces:** Produce `ServerConfig`, `ClientConfig`, typed `Account`, validated `NewServer`/`NewClient`; consume the audited source constructors and native config registration.

- [x] Write JSON/native validation tests for each version, authenticated UUID mapping and unsupported versions/modes/QUIC/transports.
- [x] Run focused tests. Actual RED: unknown Snell protocol in each version.
- [x] Pin dependencies, generate additive protobuf config, implement validation and native registration with source/license manifest.
- [x] Run focused tests. Actual GREEN; existing sing callers compile.

### Task 2: Real decoded inbound/outbound and policy lifecycle

**Files:** `core/xray/proxy/snell/{inbound.go,outbound.go,packet.go,stream.go,outbound_test.go}`, `core/xray/testing/policy/snell_test.go`.

**Interfaces:** Consume Task 1 configs and native `proxy.Inbound.Process`/`proxy.Outbound.Process`. Produce TCP and packet handler callbacks, physical credential tracking and bounded destination links.

- [x] Write real socket tests for explicit versions 4/5/6 in both directions, TCP half-close/reuse and UDP domains/IPv6/empty/large payloads. Assert independently observed target bytes and quota totals.
- [x] Run focused tests. Actual RED: missing handlers, packet headroom, reuse scope and timeout defects recorded separately.
- [x] Implement native callbacks, full datagram bridge, Xray dialer injection, immutable owner mapping and resource cleanup.
- [x] Add tests for wrong PSK, blocked route, shared quota/rate, disable/expiry/removal/sibling/idle cleanup; record actual missing-behavior RED/GREEN evidence.
- [x] Run race tests. Actual GREEN with exact byte totals, credential fences, pending dial capacity and zero active resources after cleanup.

### Task 3: Interoperability and regression evidence

**Files:** `core/xray/proxy/snell/*_test.go`, `docs/custom-core/native-snell-testing.md`.

**Interfaces:** Consume Task 2 and official test-only ARM64 v4.1.1/v5.0.1/v6.0.0rc2 reference fixtures.

- [x] Run separately labeled library-client/native inbound and native outbound/official-reference tests, including v6 distinct PSKs and UDP boundary probes. Actual official server interoperability passes; official Surge-client/native inbound remains unverified.
- [x] Run affected native core packages, existing sing callers and broader core build/tests; all chosen checks pass, including real Shadowsocks/Shadowsocks2022/WireGuard scenarios and root-module MVS callers. Full official-client inbound acceptance remains unverified.
- [x] Record exact commands/logs, compatibility pair, remaining v5 QUIC, official Surge-client and official v5 large-reply gaps in native-snell-testing.md. Evidence supports only delivered paths.
- [x] Send root the uncommitted diff for its one independent review; complete the single accepted three-finding timeout correction pass with actual RED/GREEN and fresh full race/shared build checks. Implementation remains uncommitted and unpushed.

The corrected 71-file manifest and evidence are under `/root/task-evidence`.
Original full Snell acceptance remains open for native v5 QUIC and actual Surge
client inbound tests. The official-v6 missing-reply assertion is a documented
deferred Minor, and the official-v5 large-first-reply truncation remains visible.
