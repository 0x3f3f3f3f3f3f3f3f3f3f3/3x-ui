# Durable delegated node mode implementation plan

> Required: execute inline with superpowers:executing-plans, one sole fresh final review and one author correction pass. Original autonomous ordinary-engineering authorization persists; preserve all workspaces/evidence.

Goal: create a durable explicit delegated node role whose owned core executes only coordinator grants, without running local allocation.
Architecture: add schema2 role to the existing independent authority manifest; owned startup binds its current boot before business listeners start; typed authenticated HTTPS setup and discovery expose the exact role. Grant transport/coordinator policy product follows this prerequisite.
Tech: existing Go1.27.1/Gin/custom core1 API/private journal; no new dependency/core/schema SQL migration.
Spec: docs/custom-core/delegated-node-mode-design.md.

## Global Constraints

Snell/mieru/SSH, multiplier billing, directional rates and TCP/UDP Tunnel remain mandatory. Keep one owned core and private Unix control socket. Fresh stopped source only; never convert consumed local state or silently recreate a missing delegated role. Preserve legacy schema1 local behavior and strict verified HTTPS32KiB/auth/CSRF transport. Keep managed remote policy guards until coordinator product passes. No whole-parent completion claim; no force/default merge/release/deploy. Normal authorized feature push and independent remote SHA.

## Review Focus

Role/schema/identity mismatches after SQL/core rollback must refuse, never default to local. Preparing/committed publication retries must preserve the exact role. A delegated owner with no local controller must still validate same process/socket/boot and close safely. Actual admission with no grant cannot pass business bytes, even after restart. Setup/response JSON must reject duplicate/case-alias/null fields and require actual verified TLS/auth.

## Task 1: Durable fresh execution role

Files: runtime/node_execution_role.go and node_authority_json.go; policyauthority/migration.go; service/client_policy_authority_state.go; service/node_delegation.go; service/client_policy_authority_migration.go; service/client_policy_authority_fresh.go; tests service/node_delegation_test.go and existing authority state tests.
Interfaces: NodeExecutionRole{Mode,AuthorityID,Generation,NodeID}, Validate(); NodeDelegationRequest{AuthorityID,Generation,NodeID}, Validate(); ClientPolicyNodeService.ConfigureDelegation(ctx,request) returns *NodeDelegationResult{InstanceID,Role}/error. durableAuthorityState.Role explicit local/delegated value; authorityManifest schema2 contains ExecutionRole, schema1 has no role and is legacy local; initializeAuthorityStateWithRole consumes role while existing initializer selects local.
- [x] Add TestNodeDelegationFreshManifestAndRetries / TestNodeDelegationRejectsActivatedOrUnsafeState. Fresh stopped source, exact retry, changed tuple/source, interrupted preparing publication, activated SQL/core/journal, active restore, bad private file/identity/missing schema2 role refuse. Expected behavior RED before role support.
- [x] Implement strict schema2 manifest role plus valid schema1 legacy local, create-only fresh setup under lifecycle/restore/source admission, immutable role checks/resume. Expected named parents PASS without reducing old journal/identity assertions.
- [x] Commit/task-done named parents plus authority state tests; log all rulings. Expected pass.

## Task 2: Owned delegated bootstrap

Files: service/node_execution_owner.go and node_delegation.go; test-only client_policy_activation_test.go; service/client_policy_authority_factory.go, client_policy_authority_startup_completion.go, client_policy_authority_update.go, client_policy_authority_reset_execution.go, client_policy_authority_reset_capture.go, node_authority.go; runtime/node_authority.go and remote_authority_test.go; service/node_delegation_runtime_test.go.
Interfaces: Task1 role consumed by managedAuthority. Delegated bootstrap initializes existing immutable policy seeds, binds the actual core to role tuple and enables demand before business listeners, without newAuthorityController. DiscoverAuthority produces executionRole and delegated tuple; Validate accepts omitted legacy local role and requires valid delegated tuple.
- [x] Add TestManagedDelegatedNodeStartsWithoutLocalIssuer / TestManagedDelegatedNodeRestartRetainsRole. Actual core/no local controller/no local allocations, Tunnel without a grant passes0 bytes, exact current challenge, stable instance/new boot after restart, stopped/restore/socket/role corruption refusal. Expected RED before startup support.
- [x] Implement role-specific startup/owned validation/stop/checkpoint with safe refusal of unsupported delegated local reset/hot-update paths; retain ordinary local behavior. Expected required actual parents pass0skip, no background local issuer.
- [x] Commit/task-done actual parents with verified core plus local discovery parents. Expected no-skip pass.

## Task 3: Typed HTTPS delegation setup

Files: controller/node_authority.go and node_delegation_test.go; runtime/remote_authority.go and remote_delegation_test.go; frontend API source/generated OpenAPI; exact api_auth_test inventory.
Interfaces: POST /panel/api/server/clientPolicyDelegation consumes strict exact flat fields authorityId/generation/nodeId, shared existing TLS/auth32KiB/envelope/CSRF chain. Remote.ConfigureDelegation(ctx,request) verified direct HTTPS and strict bounded response; no generic core RPC.
- [ ] Add TestNodeDelegationHTTPAuthenticationAndBounds / TestRemoteNodeDelegationRequiresVerifiedTLS / TestRemoteNodeDelegationRejectsInvalidResponse. Real token/certificate/pin/private opt-in, admin/node-sync/monitor/method/HTTP/CSRF/duplicate-alias-null/size/identity semantics. Expected behavioral RED.
- [ ] Implement typed setup and catalog/schema/inventory contracts. Expected both HTTP/client boundaries plus existing auth/envelope/catalog/frontend tests PASS.
- [ ] Commit/task-done affected named parents. Expected pass.

## Task 4: Actual HTTPS startup and acceptance

Files: sub/node_delegation_http_runtime_test.go, custom-core.yml, testing/decisions docs and parent plan.
Interfaces: consumes Tasks1-3, runs setup before first managed source activation, separate real production API auth router.
- [ ] Add TestNodeDelegationHTTPActualOwnedCore: real SQL node-sync token/pinned TLS configures fresh stopped source; real managed core starts delegated, discovery has exact tuple, no-grant Tunnel passes0 bytes, restart retains role and rejects old boot; malformed/changed/activated setup refuses; literal backend marker. Expected behavior RED if missing integration, or honest characterization pass if already correct.
- [ ] Require exact service/HTTP parents in both-backend CI with no FAIL/SKIP/no-tests and real backend marker, preserve native gates. Freeze inputs; run actual both-backend owned/HTTPS races, native5 each, full Go with writer probes, affected vet/frontend/YAML/hash checks. Expected required no-skip pass and full exit0.
- [ ] Document precise evidence and remaining scope, commit/task-done actual parents. Expected pass without global-policy claim.

## Finish and continue

All task contracts before sole fresh phase review; regrade every finding/declined item, one author meaningful RED→GREEN correction pass and complete final verification; record exhaustive decisions/costs. Normal authorized feature push and independent exact SHA; close ledger then continue typed remote grants/coordinator scopes/model/API/UI/multi-node acceptance and remaining full parent. Retain all workspaces.
