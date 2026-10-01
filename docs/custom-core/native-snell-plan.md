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

**Files:** `core/xray/proxy/snell/{config.proto,config.pb.go,account.go,config.go}`, `core/xray/infra/conf/{snell.go,snell_test.go,xray.go}`, `core/xray/main/distro/all/all.go`, module manifests, `core/THIRD_PARTY_NOTICES.md`, source provenance.

**Interfaces:** Produce `ServerConfig`, `ClientConfig`, typed `Account`, validated `NewServer`/`NewClient`; consume the audited source constructors and native config registration.

- [ ] Write JSON/native validation tests for each version, authenticated UUID mapping and unsupported versions/modes/QUIC/transports.
- [ ] Run focused tests. Expected: RED because protocol is unknown.
- [ ] Pin dependencies, generate additive protobuf config, implement validation and native registration with source/license manifest.
- [ ] Run focused tests. Expected: GREEN; existing sing callers compile.

### Task 2: Real decoded inbound/outbound and policy lifecycle

**Files:** `core/xray/proxy/snell/{inbound.go,outbound.go,udp.go,native_test.go}`.

**Interfaces:** Consume Task 1 configs and native `proxy.Inbound.Process`/`proxy.Outbound.Process`. Produce TCP and packet handler callbacks, physical credential tracking and bounded destination links.

- [ ] Write real socket tests for explicit versions 4/5/6 in both directions, TCP half-close/reuse and UDP domains/IPv6/empty/large payloads. Assert independently observed target bytes and quota totals.
- [ ] Run focused tests. Expected: RED because handlers do not transfer decoded payload.
- [ ] Implement native callbacks, full datagram bridge, Xray dialer injection, immutable owner mapping and resource cleanup.
- [ ] Add RED tests for wrong PSK, blocked route, shared quota/rate, disable/expiry/removal/sibling/idle cleanup before each missing behavior is implemented.
- [ ] Run race tests. Expected: GREEN with exact byte totals and zero active resources after cleanup.

### Task 3: Interoperability and regression evidence

**Files:** `core/xray/proxy/snell/*_test.go`, `docs/custom-core/native-snell-testing.md`.

**Interfaces:** Consume Task 2 and official test-only ARM64 v4.1.1/v5.0.1/v6.0.0rc2 reference fixtures.

- [ ] Run separately labeled library-client/native inbound and native outbound/official-reference tests, including v6 distinct PSKs and UDP boundary probes. Expected: real observed transfers or an explicit reproducible external-client gap.
- [ ] Run affected native core packages, existing sing callers and broader core build/tests; report every failure or skip without weakening APIs.
- [ ] Record exact commands/logs, compatibility pair, remaining v5 QUIC and official Surge-client gaps. Expected: evidence supports only delivered paths.
- [ ] Send root the uncommitted diff for its one independent review; do not push or commit implementation.
