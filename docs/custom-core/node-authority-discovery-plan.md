# Node authority discovery implementation plan

> Execute inline with superpowers:executing-plans. One sole fresh phase review after all task completion contracts; one author correction pass. Original autonomous ordinary-engineering authorization applies.

Goal: add authenticated, bounded HTTPS discovery of an owned node core's capabilities and monotonic boot-bound challenge as a Task12 prerequisite.
Spec: docs/custom-core/node-authority-discovery-design.md.
Tech: existing Go1.27.1/Gin/token-mTLS/node HTTPS/pinned custom core.

## Global Constraints

Native Snell/mieru/SSH, multiplier billing, directional limits and TCP/UDP Xray Tunnel remain mandatory. Full original project remains required. Keep private core RPC on the owned Unix socket; require verified HTTPS node transport for authority discovery. Preserve default existing envelope/runtime behavior, credentials and all managed remote-scope guards. No remote allowance issuance or policyScope/UI completion claim. Retain workspaces/evidence; normal authorized feature push and exact remoteSHA, no force/default merge/release/deploy.

## Review Focus

Discovery must identify the same live owned process/socket/instance/boot and fail on restart, restore, foreign/stopped owner or changed socket. TLS/auth/limits must apply before body decode. Stale expected bindings must never turn into unbound discovery. Core challenge and capabilities must agree and remain bounded. Old general remote calls must retain existing limits/behavior. Native/shared regression must actually execute in internal/sub on both backends.

## Task 1: Owned core discovery contract

Files: create internal/web/runtime/node_authority.go; internal/web/service/node_authority.go and node_authority_test.go.
Interfaces: AuthorityDiscoveryRequest{ExpectedInstanceID,ExpectedBootID}; NodeAuthorityDiscovery{Capabilities,Challenge}, Validate(request). ClientPolicyNodeService.DiscoverAuthority(ctx,request) returns *NodeAuthorityDiscovery/error. Both expected IDs empty for first discovery or both valid/bounded. Produces shared contract for Tasks2/3.

- [x] Step1: add TestManagedAuthorityNodeDiscoveryUsesCurrentOwnedBoot and TestManagedAuthorityNodeDiscoveryRejectsUnsafeOwnership. Real owned core, fresh nonce, copied capabilities, same-boot bound read; forced restart rejects oldboot then discovers a fresh one; partial/wrong IDs, cancellation, stopped/foreign/restore/socket mismatch refuse. Expected RED: missing service/behavior, no invented accounting defect.
- [x] Step2: implement serialized owned read/challenge, shared bounded response/request validation, active-restore refusal and current owner/socket identity checks. Expected named parents PASS with actual core and no skips.
- [x] Step3: commit and task-done running both exact parents with verified core. Expected actual PASS; ledger all deviations.

## Task 2: Verified bounded remote discovery

Files: modify internal/web/runtime/remote.go; create remote_authority.go and remote_authority_test.go.
Interfaces: consumes Task1 DTO; Remote.DiscoverAuthority(ctx,request). Existing do delegates to a private response-limit variant, preserving64MiB default. New authority response capped32KiB.

- [x] Step1: add TestRemoteAuthorityDiscoveryRequiresVerifiedTLS and TestRemoteAuthorityDiscoveryRejectsInvalidResponse. Real TLS pin/token/private opt-in positive transport; reject HTTP, skip, disabled/transitive node, malformed/oversized/unknown JSON, wrong boot/instance/challenge/capability and canceled request. Expected behavior RED before implementation.
- [x] Step2: implement verified HTTPS preconditions, strict bounded envelope/DTO decode and binding validation using existing credential transport. Expected focused cases PASS and unchanged existing remote/TLS/envelope tests.
- [x] Step3: commit/task-done runtime package tests. Expected PASS; no large-response allowance regression.

## Task 3: Authenticated bounded node HTTP route

Files: create internal/web/controller/node_authority.go and node_authority_test.go; modify api.go and middleware/config_envelope.go/test; create internal/sub/node_authority_http_runtime_test.go.
Interfaces: consumes Task1 service/DTO and Task2 Remote. Dedicated authenticated subgroup POST /server/clientPolicyAuthority before ordinary envelope chain, with32KiB wire/decompressed cap and CSRF. Existing node-sync POST allowlist gains only this route.

- [x] Step1: add TestNodeAuthorityDiscoveryHTTPAuthenticationAndBounds and TestConfigEnvelopeExplicitLimit; actual token scopes, missing/monitor/wrong-method/HTTP reject, TLS positive reaches owned validation, partial/unknown/trailing/oversized and compressed bodies rejected. Expected boundary RED.
- [x] Step2: implement typed JSON handler, existing auth/mTLS scope/CSRF, early cap middleware and response envelope. Expected route/middleware parents and existing API auth/envelope regressions PASS.
- [x] Step3: add TestNodeAuthorityDiscoveryHTTPActualOwnedCore with existing native HTTP harness, real TLS pin and actual challenge through Remote; after real core restart old binding refuses and initial discovery has newboot; actual Tunnel payload proves owned core still operates. Expected actual RED if integration missing, then PASS; document auth-route evidence separately from harness session.
- [x] Step4: commit/task-done named controller/middleware/actual HTTP parents with verified core. Expected real no-skip PASS.

## Task 4: Required acceptance and publication evidence

Files: CI custom-core.yml; docs/custom-core/node-authority-discovery-testing.md and parent implementation-plan.md.
Interfaces: consumes all named Task1–3 parents; same core unchanged.

- [x] Step1: require exact actual service/HTTP discovery parents in existing both-backend CI with no FAIL/SKIP/no-tests, preserve native gates. Expected YAML/bash and gate behavior checks PASS.
- [x] Step2: freeze changed executable inputs; run affected controller/runtime/middleware/service/sub races on both database labels and native five-parent gates in internal/sub both backends; full make test-go with old-writer probes, fresh vet/diff and source/core hash checks. Expected actual required parents PASS0fail/skip, full package exit0.
- [x] Step3: document exact RED/GREEN and scope/limitations, commit/task-done required named actual parents. Expected no premature coordinated-policy claim.

## Phase finishing

One sole fresh review of this entire bounded phase after completed tasks; regrade every finding/declined item, one author Critical/Important correction pass with meaningful RED→GREEN/full final verification, document all rulings and costs. Normal authorized feature push, independent SHA equality, bounded ledger closed; continue delegated node/coordinator/global-node model/API/UI and remaining original parent work.
