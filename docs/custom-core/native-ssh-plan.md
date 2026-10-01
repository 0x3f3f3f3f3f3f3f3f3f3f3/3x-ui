# Native SSH Implementation Plan

> Use superpowers:executing-plans inline and test-driven-development. Leave all
> production changes uncommitted for the root agent's single independent review.

**Goal:** Native SSH inbound/outbound with managed TCP tunnels and real client acceptance.

**Architecture:** x/crypto SSH inside Xray; direct channels use Dispatcher;
reverse payload uses directional CPE; transport leases fence authenticated idleness.

**Tech Stack:** Go 1.27.1, pinned x/crypto v0.55.0, existing CPE/protobuf/JSON loader.

**Spec:** [native-ssh-design.md](native-ssh-design.md), [requirements.md](requirements.md).

## Global constraints

- Worktree /tmp/3x-ui-native-ssh-core from a00a7b5cc8e2c133366dfea02f0fb9e33bac3667.
- No external sshd, OS accounts, panel decode, Git-key access, deployment or push.
- Do not edit the mieru worktree or broaden panel/legacy/password behavior.
- Managed SSH accounts require nonempty trusted ClientID and configured policy.
- Host-key pin and dedicated persistent host-key file are required.

## Review focus

- Credential removal during verified authentication must not resume traffic.
- Channel cleanup must preserve siblings; policy termination closes idle transport.
- Forward cancellation cannot race into a newly leaked listener/stream.
- Reverse bytes must use client-relative directions and unknown-target metadata.
- Concurrent mutable session contexts must not leak routing/identity between channels.

### Task 1: Native config and managed direct forwarding

Files: core/xray/proxy/ssh/{config.proto,config.pb.go,config.go,users.go,server.go,channel.go};
core/xray/infra/conf/{ssh.go,xray.go}; core/xray/main/distro/all/all.go;
core/xray/testing/policy/ssh_test.go.

Consumes: proxy.Inbound.Process, User.TrackSession, CPE.Manager.Open, Dispatcher.DispatchLink.
Produces: NewServer(context.Context,*ServerConfig)(*Server,error), native "ssh" inbound.

- [ ] Write real JSON-loaded core test for OpenSSH -L/-D, shared Tunnel exact ledger,
  session rejection, disabled/unknown user, credential removal and sibling channel.
- [ ] Run `go test ./testing/policy -run '^TestSSH' -count=1` and observe missing SSH loader RED.
- [ ] Add typed config/users/host-key parser and direct-tcpip channel/transport lifetime.
- [ ] Run the named tests; expected PASS. Record evidence; keep implementation uncommitted.

### Task 2: Controlled reverse forwarding and bounds

Files: core/xray/proxy/ssh/reverse.go, config fields; testing/policy/ssh_test.go.
Consumes Task 1 authenticated transport and CPE. Produces opt-in managed -R.

- [ ] Add failing tests for authorized -R, default denial, port-zero/cancel cleanup,
  source/port/bind denial, quota direction, idle policy disconnect and channel bounds.
- [ ] Run new tests, expected protocol denial before implementation.
- [ ] Implement bounded listener registry and directional admission/copy; reject unsupported requests.
- [ ] Run `go test ./testing/policy -run '^TestSSH' -count=1`; expected PASS.

### Task 3: Strict outbound, shared rates and final verification

Files: core/xray/proxy/ssh/client.go; infra/conf/ssh.go outbound;
testing/policy/ssh_test.go; docs/custom-core/native-ssh-testing.md.
Consumes supplied internet.Dialer and existing originating policy link.
Produces Client.Process(context.Context,*transport.Link,internet.Dialer) error.

- [ ] Write failing native routed SSH outbound test: right pin echo, wrong pin typed
  host-key rejection, TCP only, same-client shared rate/live disable.
- [ ] Run new tests and observe missing native outbound RED.
- [ ] Implement one owned SSH transport per Process with bounded handshake/open/copy cancellation.
- [ ] Run scoped race tests, affected conf/dispatcher/CPE regressions, vet and core build.
  Expected PASS; report exact command/results and unverified remaining panel scope.
- [ ] Provide diff and evidence to root for its single independent review; no production commit.
