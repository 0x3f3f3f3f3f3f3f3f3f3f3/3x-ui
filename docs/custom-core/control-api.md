# Custom client-policy control API v1

Service: `xray.app.clientpolicy.command.v1.ClientPolicyService`. Existing Xray service names and protobuf field numbers remain intact. Enable `ClientPolicyServiceV1` in `api.services`, set `api.listen` to an absolute Unix socket path inside an existing directory inaccessible to other users, and configure the durable `clientPolicy` instance. The socket is mode 0600. TCP, abstract Unix sockets and a public parent directory are rejected in both JSON and protobuf construction; each RPC independently rejects a non-Unix peer.

This endpoint currently supports filesystem Unix sockets on supported Unix platforms. Windows protected local transport is unfinished; it is rejected explicitly rather than silently exposing TCP. Cross-node calls must continue through the panel's authenticated node transport; a direct public policy gRPC endpoint is not provided.

| Method | Semantics |
| --- | --- |
| GetCapabilities | API version 1, distinctive custom core version, stable instance ID, epoch, explicitly implemented capability names and 64 KiB raw reservation quantum |
| GetClient | current policy, live admitted counters, frozen uncertain bytes, restriction reasons and active session count |
| ApplyPolicies | 1–1000 fully validated policies, one atomic persisted batch; monotonic versions and identical replay semantics |
| InitializeClient | create-only policy plus exact historical usage; identical retries preserve current policy/usage, conflicting seeds or existing unseeded identities fail |
| RevokeClient | permanent client-ID tombstone; requires the exact expected current policy version |
| ListConnections | bounded active registry for a client, session ID, inbound/authenticated account/target metadata and effective policy version |
| CloseConnections | closes sessions present at collection time; reconnect requires a separate disable/revoke/quota restriction |
| CheckpointUsage | persists exact counters and releases unused local reservations |
| ReadLedger | 1–1000 cumulative committed records after a sequence, ordered by commit sequence; no counter reset and no multiplier recomputation |

`ReadLedger` returns the last committed record per client, not an append-only per-packet history. A record includes stable instance ID, its commit epoch/sequence, client ID, policy version, exact directional/billed totals and remainder, frozen uncertain bytes, outstanding reservation and revocation. All fields come from one database read transaction; raw/live Snapshot values must not be combined with a durable sequence to fabricate a ledger event. Updating the same client replaces its cumulative record, so sequence gaps are expected. A cursor ahead of durable state is rejected explicitly.

The panel adapter at `internal/xray/client_policy.go` negotiates version, all required capabilities (including `create-only-usage-seed-v1`) and expected instance before allowing operations. A missing service, wrong version/identity or missing enforcement capability returns a distinct error; it never falls back to legacy statistics for quota enforcement. Stable record identity migration and transactional panel DB settlement now have SQLite/PostgreSQL tests. Production policy activation, settlement scheduling and UI remain unfinished.

Local Runtime handler operations, traffic/online queries, routing/balancer operations and configuration hot apply now resolve the running configuration's control endpoint. An explicit `api.listen` takes precedence over the legacy API-tag inbound. Unix endpoints require a private directory and socket; symbolic-link socket paths are rejected. Explicit TCP endpoints require a literal loopback IP and a valid port. Invalid explicit configuration fails without falling back to another endpoint. Configurations without `api.listen` retain their existing API-tag loopback path. This transport integration does not itself enable managed clients or perform capability negotiation for legacy handler operations.

The panel process component now preserves `clientPolicy` and provides `StartManaged`: validate the desired configuration, start a child without configured inbounds or initial policies, negotiate the expected policy instance/capabilities, run the usage-preparation callback, apply the prepared policies, and add inbounds. Ordinary `Start` refuses a managed configuration. Runtime readiness is withheld until activation finishes; any preparation or listener error stops that child. The on-disk bootstrap intentionally contains no business inbounds, while the process retains the desired configuration for reconciliation. This component is tested, but production `XrayService` has not yet connected the DB cutover/preparation callback or automatic managed configuration generation. Supplying a managed template through the legacy startup path therefore returns an explicit error.

Verified real flow: a Tunnel stream exhausts a 65,536-byte upload burst at 1 B/s. An RPC changes the same client's existing flow to unlimited upload and multiplier 2; the blocked payload resumes within 2 seconds. After 65,536 bytes per direction at multiplier 1 and 8192 per direction at multiplier 2, the committed ledger is 73,728 raw bytes per direction and 163,840 billed bytes. Connection query returns that client's active flow; the close RPC terminates its real TCP socket. Stale version updates are rejected. These results do not establish other protocols or global budgets.

## Generation

Pinned generators: protoc 36.2, protoc-gen-go 1.36.12, protoc-gen-go-grpc 1.6.0 (matching the existing core gRPC generator).

```sh
cd core/xray
protoc --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  app/clientpolicy/command/command.proto
```

The core now closes all feature resources when startup fails. Durable policy configuration is committed last, after the other features have started. A listener conflict or a later feature failure therefore leaves the previously committed policy and usage intact. Only one durable startup commit barrier is allowed. If the final storage operation itself fails, the existing fail-closed storage/recovery rules apply; this is not a promise to reverse an ambiguously completed disk write.

Local Runtime connects negotiated startup to database preparation: a durable pending source precedes private state-file creation, existing files are preserved, and an activated source cannot silently recreate a missing store. The callback binds the negotiated epoch and prepares immutable historical seeds. Runtime rejects a core behind the panel cursor or historical seed before opening business listeners. Database preparation occurs outside the Runtime RPC mutex to preserve the traffic-writer lock order. This path has real child-process tests with both supported databases; automatic production configuration/cutover is still pending.

Remaining gates include production settlement scheduling, coordinated legacy cutover, restore fencing, mass-client performance and protected local transport on Windows. Existing target metadata limitations remain: the pre-rewrite Tunnel target is not yet independently preserved.
