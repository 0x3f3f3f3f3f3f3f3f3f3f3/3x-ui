# Native SSH review evidence

Worktree: `/tmp/3x-ui-native-ssh-core`, branch `feature/native-ssh-core`.
Base: `a00a7b5cc8e2c133366dfea02f0fb9e33bac3667`.
Design/plan-only local commit: `a93c9d1322ad1b9c85141f9ef6509f12ed655d37`.
The implementation received one independent review and a focused correction pass
before root integration. The source hashes below identify the verified core scope.

## Native acceptance

These tests load typed JSON into the real native custom core. They use generated,
ephemeral business host and account keys. They never use management SSH, system
sshd, OS accounts, or Git credentials. Real loopback sockets require the approved
socket-enabled tool execution environment.

| Contract | Required test |
| --- | --- |
| Standard OpenSSH `-L` and SOCKS `-D`, decoded routing, shared Tunnel ledger, live disable | `TestSSHOpenSSHDirectSharesTunnelLedgerAndDisable` |
| Standard OpenSSH opt-in `-R`, independent asymmetric payload direction | `TestSSHOpenSSHReverseMetersClientRelativeDirection` |
| Independent upstream SSH reference, native outbound selected by routing, strict right/wrong host pin | `TestSSHNativeOutboundStrictPinAndRoutedPayload` |
| Missing policy and blackhole route cannot reach target | `TestSSHMissingPolicyAndBlockedRouteCannotReachTarget` |
| Public key, denied session capability, channel EOF and removed credential; same-client Tunnel sibling survives credential removal | `TestSSHChannelEOFAndCredentialRemovalPreserveTunnelSibling` |
| Shared upload/download rate buckets and immediate hot rate/multiplier changes | `TestSSHAndTunnelShareLiveDirectionalRates` |
| Idle authenticated transports, shared quota and expiry fencing, denied reconnect | `TestSSHQuotaAndExpiryCloseIdleTransportAndTunnelSibling` |
| Persistent dedicated host pin and shared ledger across core restart | `TestSSHRestartRetainsBusinessHostKeyAndSharedUsage` |
| Password remains default-off even with a configured password; public-key access remains usable | `TestSSHPasswordDefaultOffRejectsCorrectPassword` |
| Explicit password opt-in, wrong/stale/removed password denial, canonical shared accounting and credential rotation/removal with surviving SSH/Tunnel siblings | `TestSSHPasswordOptInRotationAndRemovalPreserveCanonicalSiblings` |
| Port-zero reply, listener limits, cancellation/rebind and disabled idle transport | `TestSSHReversePortZeroCancellationAndDisableReleaseOwnedResources` |
| Reverse default denial, allowed bind/port and actual accepted peer CIDR | `TestSSHReverseAuthorizationChecksBindPortAndRealSource` |
| Channel and per-user transport limits, absolute handshake and idle closure | `TestSSHConnectionChannelHandshakeAndIdleBounds` |
| Cancelled pending reverse OpenChannel cannot leak a later accepted channel | `TestSSHCancelledPendingReverseOpenCannotLeakAcceptedChannel` |
| Offered old public key cannot authenticate replacement canonical identity after revocation | `TestSSHVerifiedAuthenticationCannotTransferRevokedOfferToReplacement` |
| Stalled acceptance/global/per-channel request replies bounded independently of idle timeout | `TestSSHStalledControlWriteClosesWithinChannelBound` |
| Delayed remote CLOSE retains capacity and ownership until request/stderr drains retire; normal acknowledgment preserves healthy sibling | `TestChannelCapacityRetainsDelayedPeerCloseAndOwnedDrains` |
| Missing close acknowledgment reaches the configured operation timeout; parent raw closure unblocks owned drains and releases their slots | `TestChannelRetirementTimeoutClosesTransportAndJoinsOwnedDrains` |

`-L`/`-D` plus Tunnel admits upload 18, download 18, billed 54; the independent
echo target receives 18. Standard OpenSSH reverse sends upload 3 and download 5,
billed 12. This asymmetry detects swapped reverse directions. Shared rates use
an exhausted burst, queued sibling traffic and live rate release. Restart twice
retains upload/download 24 each and billed 72 without resetting the host key.
Reverse metadata reports the known listener and `unknown-client-target`, because
SSH does not disclose the client's configured ultimate `-R` destination.

The outbound fixture is an independent Go x/crypto SSH server, not this native
adapter or the earlier standalone wire probe. It verifies actual forwarded
payload at a separate target. Unit lifecycle tests additionally inject a real
TCP connection through the Xray dialer interface:

- `TestOutboundUDPReturnsExplicitUnsupportedNetwork`
- `TestOutboundHostMismatchReturnsTypedError`
- `TestOutboundHandshakeCancellationClosesSuppliedTransport` (`handshake`, `late-dial`)
- `TestOutboundHandshakeUsesAbsoluteDeadline`
- `TestOutboundIdleClosesBothCopyDirectionsAtConfiguredDeadline`

## Actual RED / GREEN history

Each regression below was run against the preceding production state before its
fix. The final race run below includes all these tests.

| RED observation | Result after implementation/fix |
| --- | --- |
| Native direct JSON loader unknown config id (3 tests / 4 cases) | Native direct tests PASS 0.378s |
| Standard OpenSSH reverse and port-zero requests denied | All then-current SSH tests PASS 1.890s |
| Native outbound unknown config id, correct/wrong reference pin cases | All then-current SSH tests and shared rates PASS 2.309s |
| Cancelled reverse request followed by late Accept leaves the channel readable instead of EOF; FAIL 1.269s | Exact regression PASS 0.139s |
| Old signed public-key offer transfers to replacement owner's canonical identity; FAIL 0.338s | Exact regression PASS 0.132s |
| One-second outbound idle after payload retains copy goroutines beyond 1.5 seconds; FAIL 1.726s | Exact deadline timer; combined lifecycle PASS 2.241s |
| Dial completing after cancelled context starts an SSH version write | Immediate post-dial cancellation fence; combined lifecycle PASS 2.241s |
| Both stalled channel acceptance and global reply outlive the configured one-second operation bound; FAIL 3.716s | Exact regression PASS 2.356s; other transport remains usable |
| Reviewer finding: locally sent CLOSE releases capacity while remote CLOSE/drains remain pending; controlled three-channel test observes owned 1, expected 2; FAIL 0.025s | Slot/ownership release follows joined request/stderr drains; exact regression PASS 0.024s |
| Reviewer finding: unsupported per-channel request reply stalls beyond one-second bound on real SSH socket; FAIL 1.919s | Timed per-channel response; exact regression PASS 1.090s |
| Parent validation: missing peer CLOSE acknowledgment keeps request/stderr retirement waiting beyond one-second bound; controlled regression FAIL 1.832s | Retirement AfterFunc closes parent transport at configured bound; acknowledged/missing-ack tests PASS 1.026s |

## Fresh final verification

Environment for all Go commands:

```sh
PATH=/root/toolchains/bin:$PATH GOTOOLCHAIN=go1.27.1 GOFLAGS=-p=1 GOPROXY=off
```

Run in `core/xray`:

```sh
go test -race ./proxy/ssh ./testing/policy -run '^(TestOutbound|TestSSH|TestChannel)' -count=1 -timeout=90s
```

Actual result, exit 0:

```text
ok  github.com/xtls/xray-core/proxy/ssh       4.304s
ok  github.com/xtls/xray-core/testing/policy  7.510s
```

```sh
go test ./infra/conf ./app/dispatcher ./app/clientpolicy ./proxy/socks -count=1 -timeout=90s
```

Actual result, exit 0:

```text
ok  github.com/xtls/xray-core/infra/conf        0.092s
ok  github.com/xtls/xray-core/app/dispatcher    1.048s
ok  github.com/xtls/xray-core/app/clientpolicy  4.265s
ok  github.com/xtls/xray-core/proxy/socks       0.027s
```

Both commands below exited 0 with no diagnostics:

```sh
go vet ./proxy/ssh ./testing/policy ./infra/conf ./app/dispatcher ./app/clientpolicy
go build -o /tmp/3x-ui-native-ssh-core-xray ./main
```

`gofumpt` and `goimports` ran over all new Go files before these checks.


Password follow-up changed only tests and documentation, with no authentication
implementation change. The real x/crypto client checked the default-off and
wrong-password attempts acquire no payload or policy lease. Explicit opt-in then
shares one ledger with a separate public-key SSH credential and Tunnel flow.
Both removal/rotation steps terminate active and idle transports for the removed
credential; the sibling SSH credential and Tunnel stay usable. The exact final
ledger is upload 48, download 48, billed 144, and the independent target sees 48.
The old password cannot authenticate after rotation and the new password cannot
authenticate after removal.

```sh
go test -race ./testing/policy -run '^TestSSHPassword' -count=1 -timeout=20s
go vet ./testing/policy
```

Actual result: race tests PASS 1.382s, vet exit 0 with no diagnostics. No deliberate
RED or auth fix was needed because this follow-up covers existing behavior.


## Single-review lifecycle correction

The original reviewer found two Important lifecycle defects. Both were reproduced
before the focused correction; no second reviewer was spawned.

`TestChannelCapacityRetainsDelayedPeerCloseAndOwnedDrains` uses exactly three
faithfully controlled channels. Like the pinned library, local CLOSE only signals
closure: requests and extended-data readers remain until remote acknowledgment.
The test checks that the cap rejects a third channel until the first actually
retires, then restores capacity; the healthy sibling remains open. Finally parent
transport closure unblocks all owned drains; the owning workers join them and
release every slot.
This is a bounded controlled-object regression, not a claim of observed growth of
real library mux channels. Real socket cleanup, normal EOF/sibling survival and
reverse cancel/late acceptance remain covered by the native acceptance suite.

Every direct and reverse channel now retains its registry entry and cap until
its owning worker joins both the request drain and stderr drain. The incoming
request stream closes when the pinned library retires the channel on remote
CLOSE or parent transport failure. Parent shutdown closes raw transport, which
unblocks those drains; it no longer prematurely frees their slots. No additional
per-channel monitor goroutine was added. The owning retirement wait also carries
an AfterFunc with the configured channel operation timeout. If the peer never
acknowledges local CLOSE, it closes the parent transport and unblocks its drains,
even while another channel would otherwise keep the transport active. A normal
acknowledgment stops that timer and preserves healthy siblings. The bounded
controlled missing-ack test verifies this timeout and eventual zero owned slots.

The real-socket `channel-request-reply` case stalls the network write after one
successful SSH handshake and direct channel acceptance. It now verifies rejection
of an unsupported channel request closes that transport within the configured
one-second bound. The bounded request-serving loop replaces bare DiscardRequests
for both direct and reverse channels. Normal channel-close EOF is handled without
terminating healthy sibling channels.

Protobuf was regenerated into `/tmp/native-ssh-proto-check` using protoc 36.2 and
the pinned protoc-gen-go 1.36.12, formatted with goimports and compared byte-for-byte:

```sh
/root/toolchains/protoc-36.2/bin/protoc --proto_path=. --proto_path=/root/toolchains/protoc-36.2/include --go_out=/tmp/native-ssh-proto-check --go_opt=paths=source_relative proxy/ssh/config.proto
goimports -w /tmp/native-ssh-proto-check/proxy/ssh/config.pb.go
cmp proxy/ssh/config.pb.go /tmp/native-ssh-proto-check/proxy/ssh/config.pb.go
```

All exited 0. Parent validation subsequently verified the same corrected source
against the original review findings; no second reviewer was dispatched.

## Scope and review limits

Core SSH only. Panel/API/DB/forms/export, business key provisioning/rotation UI,
capability negotiation, externally certified interoperability, and full-system
release acceptance remain root's next work. Explicit password enablement is covered by an independent mature Go x/crypto
SSH client using `Password`; this evidence is not labelled as OpenSSH password
interoperability. The original standard OpenSSH `-L`/`-D`/`-R` evidence uses public
keys. No outbound pooling is used, so each request owns its
supplied-dialer SSH transport.

Xray's existing `Dispatcher.DispatchLink` does not expose upstream connection
success to the adapter. A valid direct-tcpip channel is accepted before selected
routing/dialing completes; denied/failed routes close the channel without a direct
fallback. This increment verifies that routing denial prevents target payload,
but does not promise OpenSSH channel-open failure codes for every failed target.

Inbound fixed-buffer/window bounds use the mature pinned SSH implementation plus
explicit transport/channel/listener caps. Operation timers bound control writes;
reverse pending OpenChannel timeout closes the owned transport and joins the
pending goroutine. Copy cancellation closes its streams and joins both copies.
Root must independently review source before integration or capability claims.

## Frozen source hashes

Manifest: `/tmp/native-ssh-frozen.sha256`. Updated after bounded acknowledgment
retirement and password acceptance follow-up.

```text
cdffab5572a29c181b2e65635ef9550db9dbfc9aa3ee9e12af74710c9bfb683f  core/xray/infra/conf/ssh.go
b2b63201872ece9e3f4483c2a12389b341f2615702a01dce7b0d84083216c890  core/xray/infra/conf/xray.go
f33be4a80d304621c1a2c5ec19dea4fcef4f71176c8f4929ae5ae6671c631a1e  core/xray/main/distro/all/all.go
bb8b91023aad48150498c41fc6f044024d20068fae408aa81b9550e6cb89f06e  core/xray/proxy/ssh/channel.go
52b94c406e14ae9db3a7e3c13e32c116653307c7e04457f48ba9eb3120ede3c2  core/xray/proxy/ssh/channel_lifecycle_test.go
a89b69c44cef0d575b2de7703e9ff6fd5eb2a9c8b7ec9ea3b4b1c76f2b0f4d33  core/xray/proxy/ssh/client.go
ff27c5aec1935016bfce8962b6fc05c77f9c0ea4884fb888d14e2d70c313bd39  core/xray/proxy/ssh/client_test.go
bedfeab726d3df20c20ebb23e3ce71a5162ce096334382c0d310edf5f0144ec6  core/xray/proxy/ssh/config.go
ecaee4e4035f72e74ab64881ba41e06c080c7ee59a846ed5b429382a6a486697  core/xray/proxy/ssh/config.pb.go
cdf1267f5db48df48c66afd05eb0772c291b89fe6a6ec368fc8061f0b6af0465  core/xray/proxy/ssh/config.proto
23d0a987990180bcaff19d90de5bba3072fad707073803fa316ca0cd4f02cd63  core/xray/proxy/ssh/idle.go
bd161125aee221fe404e31b6c4cf8df7a97d8aebb7cdc500143857cb2980ca63  core/xray/proxy/ssh/reverse.go
6cd0c9ac7897c45705dd64935a77c71214acf2374b8237e7c33dc07cb95c7b90  core/xray/proxy/ssh/server.go
4b4047b58e7eacd8938dceab65e69ae6868fc396bd014a71fc280a8e924ece9d  core/xray/proxy/ssh/users.go
d4c6a1a823db5cca0853f3e86647a254141980b0d6eb0f1d662a9cc02de9d7cf  core/xray/testing/policy/ssh_test.go
```

## Parent validation and scope ruling — 2026-10-01

The one independent review raised two Important findings: prematurely released
channel ownership and unbounded per-channel request replies. Both enter the
accepted correction pass. The parent additionally reproduced the missing-close
acknowledgment bound and verified its correction. All 15 frozen source hashes
match `/tmp/native-ssh-frozen.sha256`.

Parent final race command (Go 1.27.1, count=1, verbose, 90-second timeout):
`go test -race -count=1 -v -timeout=90s ./proxy/ssh ./testing/policy -run
'^(TestOutbound|TestSSH|TestChannel)'` passed SSH 4.300s and policy 7.526s.
Log: `/root/task-evidence/native-ssh-parent-final-race.log`; both controlled
retirement regressions and real-socket channel-request-reply explicitly PASS.
Parent affected configuration/dispatcher/CPE/SOCKS tests passed
0.317/1.088/4.395/0.067s; scoped vet exited 0 without diagnostics.

Ruling on the review's deferred scope: panel/API/DB/forms/export, dedicated
business-key provisioning/rotation UI, capability negotiation and full release
acceptance remain required follow-on work. They are not represented as completed
by this native core checkpoint. Broad client certification and rekey-adversarial
coverage remain unverified; tested OpenSSH public-key forwarding and independent
Go password/outbound acceptance are reported separately. The reviewer's first
close-ack harness did not reproduce runtime growth; the later faithful bounded
three-channel regression proves the corrected capacity/drain contract.
