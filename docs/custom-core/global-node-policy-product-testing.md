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
