# Typed remote authority transport decisions

Spec: [remote-authority-transport-design.md](remote-authority-transport-design.md). Plan: [remote-authority-transport-plan.md](remote-authority-transport-plan.md). Original full autonomous development and normal authorized fork feature publication persist.

| Ruling | Why | Cost or remaining requirement |
| --- | --- | --- |
| Continue inline in retained isolated named worktree; no renewed permission menu. | Full original engineering/publication instructions already supply authorization. | No force/default merge/release/deployment; sole phase review and independent remote SHA still required. |
| Retain all plan workspaces and failed evidence. | Prevent loss of source provenance and prior old-writer fixtures. | Disk maintenance only removes separately audited stale compiler archives. |
| Canonical protobuf payload/result JSON uses camelCase and decimal-string uint64. | Preserve exact billing/grant amounts across Go and JavaScript. | New peers must speak exact fields/types; aliases/duplicates/null/exponents/leading zeros refuse. |
| Common request binding is one required nested object with five exact fields. | All six typed operations share expected source/boot/coordinator identity without exposing arbitrary RPC. | New control peers require binding envelope; existing discovery/setup schemas stay compatible. |
| Use independently created coordinator journal identity before fresh node setup. | Real grant evidence must not originate in a node-owned local allocator. | Zero canonical seed/account/window/version is installed afterward; consumed local-source handoff remains following work. |
| Hold lifecycle, current database connection and owned authority for one bounded operation, revalidate after RPC. | Prevent mutation crossing restart/restore/socket/role replacement. | Busy requests fail, bounded demand long poll can delay lifecycle briefly, uncertain mutations never acknowledge capacity reuse. |
| Renew acknowledgement echoes exact submitted renewal, not usage or another allocation. | Existing private core renewal returns Empty and proves only successful mutation. | Settlement still requires an actual validated report; lost acknowledgement stays uncertain. |

All task Expected contracts, sole fresh phase review, exhaustive findings/declined rulings and one meaningful author correction pass remain required. Global mapping/model/API/UI, real two-node partitions and the rest of the original parent follow this transport prerequisite.

Task2 ruling: shared tls_client.go must partition cached clients by AllowPrivateAddress and default verify/HTTP pools by private opt-in. The existing guarded dial does not run when reusing a live connection; real control and direct ordinary-pool REDs prove opt-out could reach a private peer. Cost: separate pools and connection setup after permission changes; current full runtime passes without altering existing TLS/mTLS trust. Protocol serialization helpers live in node_authority_control_json.go so all six typed DTOs share exact bounded encoding without widening generic RPC.

Task2 evidence ruling: the original pre-proto.Clone full run is superseded and FAILED on a Snell test-port collision; keep it without acceptance credit. Final corrected frozen inputs independently pass52 packages. Task4 will exclude already configured SQL inbound ports in the native test helper, with deterministic regression; no listener collision is hidden by retries. Cost: test-only extra database lookup, and broader native regression before phase completion.

Task3 catalog rulings: initial module split was superseded after Node direct TS resolution failed while compiler settings disallow.ts imports; shared exact schemas remain inline. Dynamic paths were superseded after existing actual router registry contract failed; six literal method/path entries retain shared definitions. Cost: a longer existing catalog and six explicit entries, guarded by bidirectional route and generated request/result contracts. No parser/compiler settings weakened, no new dependency. Complete32KiB response envelope is encoded before writing; encoding/size errors never acknowledge uncertain mutations.
