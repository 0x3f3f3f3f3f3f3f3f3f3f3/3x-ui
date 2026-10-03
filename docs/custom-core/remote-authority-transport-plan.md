# Typed remote authority transport implementation plan

> For agentic workers: use superpowers:executing-plans inline, sole fresh final review and one author correction pass. No implementer subagents. Original autonomous engineering/feature publication authorization persists; preserve all evidence/workspaces.

Goal: execute real coordinator journal grants over authenticated HTTPS on an owned delegated node.
Architecture: six typed control methods with retained owned admission; exact bounded request/result encoding; pinned RemoteAuthorityAPI reuses existing authorityDemandAPI. No generic RPC and no global-policy completion claim.
Tech: existing Go/Gin/core1/private Unix/journal/TLS frontend catalog; no new dependency.
Spec: [remote-authority-transport-design.md](remote-authority-transport-design.md). Durable delegated-mode correction084ecb9a is already published and independently SHA-verified; this plan executes now.

## Global Constraints

Snell/mieru/SSH, shared multiplier/directional limits/TCP-UDP Tunnel remain mandatory. Only journal-pinned delegated role may receive remote authority operations. Mandatory expected source/boot and exact coordinator tuple,32KiB verified direct HTTPS/auth/CSRF, no redirects, exact unique non-null JSON at every identity boundary. Uncertain mutation holds capacity conservatively; never infer nonexecution from a lost reply. Keep remote policy guards until global/node mapping/product passes. Normal existing fork feature push only, independent exact SHA, no default merge/force/release/deploy.

## Review Focus

Lost success after a core mutation must not let coordinator reuse uncertain capacity. Every nested identity/uint64/share rejects alias/duplicate/null/type ambiguity. An old boot or replaced SQL/socket/role refuses before and after RPC. No local owner grant mutation or autonomous delegated issuance. Response core/client/grant identity and exact monotonic counters cannot be overwritten or rounded. Add tests to the owning tasks for each class.

## Task 1: Typed service control and owned admission

Files create runtime/node_authority_control.go and service/node_authority_control.go; tests service/node_authority_control_test.go. Reuse node_execution_owner.go/node_authority.go ownership and node_execution_role.go role DTO.
Interfaces: binding envelope contains required expectedInstanceId/expectedBootId and authorityId/generation/nodeId. Typed requests for ReadAuthorityRequests(limit1..128), InstallAuthorityGrant(grant), GetAuthorityGrant(clientId,grantId), PauseAuthorityGrant, SealAuthorityGrant, RenewAuthorityGrant(renewal). Reply binds instanceId/bootId/executionRole and typed request page/grant state/renewal acknowledgement. The Go DTO contains a common NodeAuthorityControlBinding with expectedInstanceId/expectedBootId/authorityId/generation/nodeId; Read request adds limit, Install adds *command.ExecutionGrant, Get/Pause/Seal add clientId/grantId, Renew adds *command.AuthorityRenewalRequest. Distinct typed result structs share binding metadata and carry Requests/State/Renewal respectively. JSON serialization of protobuf fields is supplied by Task2 canonical wire helpers, not permissive generated snake_case struct decoding. Distinct methods, no operation string relay.
- [x] Add TestNodeAuthorityControlRequiresOwnedDelegatedCore / TestNodeAuthorityControlBindsEveryOperation. Reuse setupManagedActivationServiceWithUsage(t,0,0) and a genuinely independent coordinator journal created before ConfigureDelegation; use its actual identity in the durable role, not the fixed bootstrap-only fixture identity. It may start as an empty journal so its identity exists before setup, then AddAccount the proven zero-usage canonical seed after actual startup exposes the same client/window/version; register the exact current NodeBoot before Issue. No node-owned local grant can substitute for the independent allocation. Cover all six methods with stale source/boot/coordinator/gen/node, local owner, nil/canceled context, lifecycle/owner busy, restore admission, foreign process, socket replacement and stopped core; at least demand and real journal install must succeed after implementation. Behavioral RED before service support; a fail-closed compile stub is not acceptance.
- [x] Implement common retained owner admission and typed calls, require inner/outer identities match before core mutation and verify same owner after. Deep-copy operation inputs before retained admission, validate bounded IDs/integers/lease/shares, clone returned protobuf data and execution-role values, and confirm returned identities/count/sequence/usage bounds. Reuse the existing direct SQL connection lease and exact owned API rather than redial an arbitrary endpoint. Keep core policy/challenge/sequence/capacity semantics authoritative. Required actual parents GREEN0skip.
- [x] Commit/task-done exact parents with verified core; ledger lost-reply/uncertainty boundaries and all deviations.


Completion contract: with actual XRAY_E2E_BINARY, run `go test -race -count=1 -v -timeout 5m ./internal/web/service -run '^TestNodeAuthorityControl(RequiresOwnedDelegatedCore|BindsEveryOperation)$'`. Expected: both literal parents PASS, zero FAIL/SKIP/no-tests. Broader existing owned/delegation/local regressions must also pass before commit.

## Task 2: Verified remote client and pinned adapter

Files create runtime/remote_authority_control.go, remote_authority_api.go and corresponding tests; extend existing node_authority_json.go exact object helpers as needed without weakening setup/manifest behavior.
Interfaces: six typed Remote methods plus RemoteAuthorityAPI implementing the existing service authorityDemandAPI via structural Go interface; immutable cloned Capabilities, bound discovery challenge, BindAuthority verifies configured tuple/current boot only.
- [x] Add TestRemoteAuthorityControlRequiresVerifiedTLS / TestRemoteAuthorityControlRejectsAmbiguousPayloads / TestRemoteAuthorityAPIPreservesBinding. Actual pinned TLS/encrypted tokens/private opt-in/redirect/cancel/oversize/full uint64/current role/source/client/grant/duplicate aliases. Behavioral RED.
- [x] Implement exact request/result/nested protobuf field validation and verified transport; adapter pins source/boot/role and never rebinds or exposes mutable metadata. Existing remote/setup/discovery/ordinary envelopes GREEN.
- [x] Commit/task-done required named parents; document canonical wire field names and exact integers.


Completion contract: run `go test -race -count=1 -v -timeout 5m ./internal/web/runtime -run '^Test(RemoteAuthorityControlRequiresVerifiedTLS|RemoteAuthorityControlRejectsAmbiguousPayloads|RemoteAuthorityAPIPreservesBinding)$'`. Expected: all three literal parents PASS, zero FAIL/SKIP/no-tests; full runtime regression remains required. Canonical uint64 wire strings are lossless, not floats or permissive aliases.

## Task 3: Production HTTP and catalog

Files controller/node_authority_control.go and tests, node_authority.go route wiring, api.go/api_auth_test.go exact inventory, frontend endpoint source/generated artifacts/contract tests.
Interfaces: six distinct POST endpoints (/server/clientPolicyAuthority/{requests,install,get,pause,seal,renew}) consume typed bounded envelopes in existing auth/TLS/CSRF chain and return opaque public errors.
- [x] Add TestNodeAuthorityControlHTTPAuthenticationAndBounds, all six methods/scopes/session-CSRF/duplicates/aliases/null/type/wire-decoded limits. RED before route support; catalog contracts RED before declaration.
- [x] Implement routes/strict decode/opaque errors and exact node-sync inventory + complete request/result schemas. Both database boundary races and runtime/catalog/typecheck pass.
- [x] Commit/task-done literal parents and exact scope inventory.


Completion contract: run `go test -race -count=1 -v -timeout 5m ./internal/web/controller ./internal/web/runtime -run '^Test(NodeAuthorityControlHTTPAuthenticationAndBounds|NodeSyncScopeAllowlistMatchesRemoteInventory|RemoteAuthorityControlRequiresVerifiedTLS|RemoteAuthorityControlRejectsAmbiguousPayloads|RemoteAuthorityAPIPreservesBinding)$'` on both databases. Expected: five named parents PASS across the packages, no FAIL/SKIP/no-tests. Run owning frontend request/runtime contracts, generation+artifact equality and typecheck. Expected: all named contract tests PASS, compiler exit0, exact generated file match.

## Task 4: Real journal/HTTPS business execution

Files sub/node_authority_control_http_runtime_test.go, custom-core.yml, testing/decisions/parent plan.
Interfaces: independent coordinator journal identity configures fresh node before real managed startup; actual canonical client/window/version, journal allocation then boot challenge/install through production TLS. No synthetic full-global allowance shortcut.
- [ ] Add TestNodeAuthorityControlHTTPActualJournalGrant: all six operations, literal2x raw/billed Tunnel bytes, demand, duplicate install/report, pause/seal, renewal/retry, expiry/outage, real restart rejects old boot, no grant0bytes. Actual SQLite/PostgreSQL markers. Add real local-owner HTTP refusal using its actual local issuer tuple and a complete valid grant/renewal payload, so refusal cannot be explained only by a foreign core binding or malformed body. Honest RED or prior-correct characterization.
- [ ] Add exact both-backend CI parents/noFAIL-SKIP-no-tests + native5 gates; freeze all current inputs/core SHA; both-backend actual races/native5, full Go+writer probes/vet/frontend/YAML/hash pass.
- [ ] Record scope/uncertain reply costs; commit/task-done. Sole fresh review, exhaustive findings/declined rulings, one author meaningful correction pass/full suite if required, normal authorized push+independent exact SHA. Continue canonical global/node client mapping/model/API/UI and real two-node quota/rate/outage acceptance; original full parent remains.

Completion contract: with actual core run `go test -race -count=1 -v -timeout 10m ./internal/sub -run '^TestNodeAuthorityControlHTTPActualJournalGrant$'` on SQLite and PostgreSQL. Expected: literal parent PASS each, exact actual backend marker, zero FAIL/SKIP/no-tests. All six operations and conservation assertions must execute; fixture-only synthetic success does not satisfy it. Both existing native5-parent gates, full `make test-go` with old-writer probes, affected vet, frontend/contracts/YAML and every frozen source/core hash must pass before task completion and sole phase review. Normal authorized push must be independently SHA-verified before starting following global/node mapping work.

