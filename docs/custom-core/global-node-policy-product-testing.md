# Global/node policy product validation

Task 1 implements optional `node|global` policy scope and retained canonical node accounts. It does not activate a managed coordinator or claim two-node business acceptance; those remain tasks 2–4.

Missing scope preserves legacy JSON and node fingerprints. Nested edits inherit an existing explicit scope. The nullable SQL scope is stored on ClientRecord so GORM cannot turn an all-NULL policy into an explicit empty policy. Node accounts retain distinct UUIDs, parent/node/source identity, independent desired versions and reset fractions. Parent/node and parent/source uniqueness prevent replacement identities from receiving fresh allowance. Preparation remains under current-database serialized transaction and deletion/reset guards. SQL records alone grant no execution authority.

Current acceptance uses 120 frozen source inputs and five unchanged real binaries, recorded in `global-node-policy-task1-post-generator-final-source-inputs.json` under the retained execution evidence directory.

| Gate | Actual result |
| --- | --- |
| SQLite owning race tests | 20 literal parents, no failures or skips |
| PostgreSQL owning race tests | 22 literal parents, including both actual row-lock races; no failures or skips |
| SQLite → PostgreSQL → SQLite migration/export/dump/restore | TestClientPolicyCrossDatabaseMigration passed, preserving explicit scope, independent account identity and version9007199254740993 |
| Model absence/clone owners | Both passed; SQL all-NULL absence and ordinary saves preserved |
| Contract generator | Three parents passed; undefined internal scheduler alias refused |
| Frontend policy/API contracts | 28 tests passed |
| TypeScript / Go vet | Both completed successfully |
| Source/binary/catalog verification | All hashes unchanged; both OpenAPI catalogs identical |

The node-account owner also checks 12 concurrent retries, independent versions, database uniqueness, rollback after account creation, source spoofing, cancellation/nil context, exact decimal-string output, independent billed/fraction reset and retained account/parent tombstones. Existing remote attachment gates remain closed until original managed enrollment is available.

All failed and diagnostic runs remain in evidence. The first broad SQLite selection included two PostgreSQL-only conditional skips and is not accepted; the final SQLite selection excludes those owners and both execute successfully on PostgreSQL. Initial scope, merge, account, account-resolution, SQL absence and generator tests failed before their implementations or corrections. No skipped test supplies required acceptance.

Task 2 adds the retained managed coordinator, original account origins and bounded allocation. Its schema8 activation supports an empty mapping bucket and refuses seed-only account creation. Four actual prior writers (schemas4/5/6/7) reject both empty and populated activated journals without changing bytes. Original reset floors4/5/6 remain intact. SQL control activation and configured inventory connections are separate from execution source epochs and receipts, and join both database registries.

Actual TLS and Custom Xray owners prove global7/local1 enrollment, lost successful enrollment reply recovery, live adapter refresh after a second actual account enrollment, original manifest/source/role admission, exact raw4/down4/billed16 settlement, same-boot coordinator reopen, failed seal recovery retaining the complete original account, fresh core boot preserving unsealed capacity, persisted reconnect and disabled inventory refusal. Demand long-polls release locks during the read and require the same owned API/source/boot/role under admission afterward. Mutating node operations retain their owned critical section. Restore drains the coordinator even with no local core; successful owned restart resumes it after restore admission is released. A stop timeout retains unjoined controller tracking.

Two actual standalone core processes also prove shared rate/burst conservation, lost grant installation acknowledgement, original fractions and membership changes: an unavailable seal keeps the removed member's resources held, and a remaining member receives expanded shares only after the actual seal. Node-scoped accounts keep independent original UUIDs and full per-node quota. Restoring disposable node-account SQL preserves the original UUID and held60. Pending SQL policy changes and parent/source retargeting refuse new issuance.

Current Task2 acceptance is frozen in `global-node-policy-task2-current-source-inputs.json`:769 source inputs and six real core/probe binaries, with the exact verifier hash. Every required parent is derived from current source and checked after process completion; nested skips, missing/duplicate parents, wrong backend and changed hashes reject acceptance.

| Gate | Actual result |
| --- | --- |
| PostgreSQL owning race suite | 37 literal parents,132.492s, zero failures/skips |
| SQLite owning race suite | 36 literal parents,57.087s, zero failures/skips |
| Full original journal race suite | 84 literal parents,289.069s, including100000-record bounds and four actual old writers; zero failures/skips |
| Full runtime race suite | 76 Test/Fuzz parents,6.029s, zero failures/skips |
| Generator suite | Three parents,0.133s, zero failures/skips |
| Actual SQLite/PostgreSQL export/dump/restore | One parent,11.653s, preserving activation and connections; old missing tables create neither |
| Go vet / TypeScript | Both completed successfully |
| Generated contracts / hashes |61 schemas,193 paths,204 operations; both catalogs equal;769 source/six binary hashes unchanged |

These are Task2 controller/lifecycle owners. They do not substitute for Task3's two independent physical panel subprocesses with separate SQL/runtime/original journals and authenticated production TLS routes. Product APIs/UI, managed Snell/mieru/SSH business acceptance, full branch faults and publication remain subsequent tasks. Original consumed-source handoff and append-only policy-version proofs remain required before changing used clients' scope or policy. All failed, diagnostic and superseded primitive runs remain retained and excluded from current acceptance.

Task3 adds protected coordinator status, explicit activation, original account pages and enrollment. These admin/session endpoints retain TLS mutation, CSRF, strict JSON, canonical UUID/version and32KiB envelope boundaries. Status reads never activate a fresh coordinator. Account balances come from the original journal; changed SQL projections neither supply usage nor create credit. The client form preserves omitted legacy scope, exposes node/global scope and displays exact string amounts for pending, held, frozen, remaining and unallocated balances. English/Chinese labels and English fallback keys for all other existing catalogs are present.

Two independent physical helper processes each own separate SQL storage, runtime directory, original node journal and actual Custom Xray. The parent has its own coordinator SQL/journal. Actual production TLS handlers, pinned certificates and ephemeral private0600 fixture credentials perform enrollment. Readiness requires both actual local version1 and the corresponding prepared SQL version1; the global canonical version is7. Each process verifies its backend and actual core SHA before acceptance.

| Current gate | Observed result |
| --- | --- |
| SQLite physical/native race acceptance |9 exact literal parents,132.354s, zero failures or nested skips |
| PostgreSQL physical/native race acceptance |9 exact literal parents,210.310s, zero failures or nested skips |
| Protected API/query/generated-contract race acceptance |10 exact parents on each backend across four completed packages, zero failures or skips |
| Actual shared Tunnel billing |Simultaneous TCP/UDP: UP7, DOWN6, billed19.5 at1.5x, held0, remaining8172.5 |
| Actual managed Snell/mieru/SSH |Downloaded Snell TCP/UDP, official mieru API and real OpenSSH with exported known-hosts on both nodes; shared Tunnel; UP10, DOWN10, billed30 at1.5x, held0 |
| Global and independent node quotas |Each required quota assertion includes actual final paid echo and denies additional traffic; global account or two distinct node accounts each reach billed12 at2x |
| Actual shared directional limits/bursts |Two simultaneous65536-byte transfers; aggregate UP8192/s and DOWN16384/s with total burst65536; separate non-race measurements8.038998853s and4.00486847s |
| Paid final payload core regressions |Five new literal owners passed-race; full policy/dispatcher/SSH suite108 parents passed-race, zero failures or skips |
| Full frontend including real Chromium |206 files/1937 tests,638.52s, zero failures/skips; final build passed |
| Go vet, TypeScript, generated contracts |Passed;67 schemas,197 paths,208 operations; panel/docs catalogs identical |

Physical node-scope quota acceptance found that the old core charged its last downlink byte and closed the connection before delivery. The new payload guard denies further admission immediately while allowing the already charged payload to reach its forwarder. Disable, policy/lease expiry, revocation, retirement, storage failure and shutdown keep their direct close paths. Actual net.Pipe writer, reader-context and SSH reverse owners reproduce the lost byte before the change and prove delivery afterward. The physical final-echo assertion was preserved.

The actual new core is SHA256 `ae50f5a45d0a971bc27ba25eea9184e34765d03dd90d12ae3a6983e1bba50685`, built from1473 frozen hashed inputs on dirty7153a661 source. It is an acceptance binary, not a clean paired publication. Current evidence snapshots bind4185 repository inputs and seven real binaries/probes; exact parent/backend parsing occurs only after completion. All earlier binaries and failed logs remain preserved.

The first complete frontend run found missing fallback locale keys,28 browser setup failures caused by an external dependency symlink, and three5s interaction timeouts. Locale keys were corrected. An independent local dependency copy restored the existing locked-frontend distribution closure; required packaged browser .vite resources were preserved separately from root caches. A real Chromium probe passed and production build passed. Isolated single-worker forms/language rerun passed18 tests with original deadlines. The second complete run executed all browser suites but had four varying interaction timeouts. A complete serial run passed1936/1937 tests, with only the four-submission calendar workflow exceeding5s. That owner now has a15s total budget while retaining all individual waitFor deadlines and data assertions. The final complete suite passed206 files and1937 tests, zero failures/skips,638.52s, using one worker and all other original deadlines. Final production build and TypeScript also passed after the last catalog formatting; all failed and diagnostic logs remain retained.

Task4 physical outage/expiry/restart/deletion/restore semantics, exact CI, clean paired publication and the single phase review remain open. Append-only version proofs and consumed-source handoff remain required before used clients may change scope or mapped policy versions. Remaining original reset, sidecar, platform, scale, external-client and final acceptance requirements are not represented as complete here.
