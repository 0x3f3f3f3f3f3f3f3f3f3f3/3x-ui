# Authenticated node authority discovery

Parent objective remains native Snell/mieru/SSH followed by shared multiplier billing, directional limits and Xray Tunnel forwarding. This checkpoint begins Task12 transport prerequisites; it does not issue remote allowances or complete global/node product policy.

## Choice

Three options were considered: expose the private core RPC directly on a new network port; forward arbitrary core RPC through the panel; or add a typed, bounded authority interface to the existing authenticated node HTTPS transport. Choose the third. It reuses token/mTLS, node certificate verification, private-address opt-in and envelope integrity. Existing private core socket remains private. Discovery precedes delegated execution mode and central budget/rate partitioning, so managed remote guards remain until those products pass.

## Contract

Add POST /panel/api/server/clientPolicyAuthority with explicit JSON expectedInstanceId/expectedBootId. Both empty permits initial discovery; subsequent calls require both or reject the partial binding. Only existing admin/node-sync authentication may reach it; monitor remains denied. Require actual TLS for this authority endpoint. Requests and responses have32KiB bounds. Unknown/trailing JSON and invalid identity bounds fail. Responses use the existing success/obj envelope and contain copied core capabilities plus a newly issued monotonic authority challenge. No credentials, socket paths, accounting database or grant mutation are returned.

Server service holds restart serialization, refuses an active restore, and uses the currently retained owned authority. It validates running/control-ready process, stable instance and same private socket inode/current boot under authority locking. A mismatched expected instance/boot rejects before requesting a nonce. Actual challenge must match the discovered instance and boot; nonce is a16-byte hex value and lease bound is at most10000ms. Caller cancellation is observed. Foreign, stopped, changed-socket or restore owners cannot produce success. Discovery does not transfer local grant ownership.

Remote.DiscoverAuthority(ctx, expectedInstance, expectedBoot) requires enabled direct HTTPS node with certificate verify/pin/mTLS (skip refused). It uses the existing encrypted token or mTLS client and body integrity, with an endpoint-specific32KiB response cap. Decode strictly and validate API1, bounded capabilities, fresh16-byte boot/challenge IDs, matching identities and required fresh-incarnation/monotonic-challenge/boot-bound-grants features. A bound request must reject a stale/mismatched response. No retry turns an old expected boot into initial discovery.

## Acceptance

Observe route/auth/TLS/body boundaries in focused RED→GREEN tests, actual owned-core discovery then restart/stale binding, stopped/restore/foreign/socket identity refusal, and real TLS node HTTP calls with a pinned synthetic certificate and actual core challenge. Keep native five-parent gates on both databases, backend-aware discovery tests, full Go/race/vet and mandatory CI no-skip checks. Test malformed/oversize and wrong-boot remote responses without reducing ordinary remote behavior.

One inline implementation plan, tasks complete before one sole fresh review, one author correction pass, retained evidence, normal authorized feature push and independently matching remote SHA. Next parent phase implements explicit delegated node mode, coordinator budgets/rate shares, scope model/API/UI and real two-node outage/recovery acceptance.

The route registers on an authenticated API subgroup before the ordinary configuration-envelope middleware. A32KiB limit applies to both compressed wire input and decompressed JSON before decoding/hashing. Default existing envelope behavior remains unchanged. This checkpoint has no policyScope/UI changes or remote grant mutation; these follow real delegated-mode and coordinator acceptance.
