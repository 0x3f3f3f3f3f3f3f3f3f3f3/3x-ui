# Native Snell panel forms checkpoint

Task 2 follows backend commit `88aa5ddc56d261cefb1b266c3f9605bd7075c2e6`.
The existing account, inbound, outbound, JSON editor and bulk attachment forms
now support native Snell versions 4, 5 and 6. Explicit native settings preserve
the independent PSK, endpoint, obfuscation, shaping and connection reuse.
TLS/REALITY, wrapped transports and global mux are rejected before normalization.
Only the v5 outbound offers a QUIC choice; the inbound retains the native v5
TCP/UDP reservation without inventing a separate activation switch.

Accounts edit `snellPsk` independently of ordinary, SSH and mieru credentials.
A blank value preserves an existing PSK or delegates initial generation to the
canonical SQL writer. UTF-8 byte limits and v6 minimum length are enforced.
Staged disabled empty services remain available for their first account.
The listener owner selector submits the outer canonical UUID command and never
constructs a raw runtime identity.

Inbound options now carry non-secret `snellVersion`, `snellOwnerCount` and
`snellOwnerClientId`. Occupancy comes from SQL links, including disabled owners.
Another account cannot select an occupied service; bulk creation is limited
to one owner. A bulk attach target becomes invalid when account count or
occupancy changes. SSH and mieru remain available for multi-account attachment.
The five generated API contracts are byte-identical on repeat generation.
All 13 translation bundles carry the new keys; English fallback wording is
included alongside Simplified and Traditional Chinese translations.

An options regression exposed a stale-mirror removal defect: a canonical owner
could remain bound when its old JSON label disagreed with SQL. Single and bulk
Snell removal now check the fresh canonical numeric ID and stable UUID inside
the locked SQL writer. Final removal disables the resource, releases its
runtime listener and preserves the account credential and policy ledger.

Verification passed: 27 native SQLite parents, 30 native PostgreSQL parents,
86 retained SSH/mieru PostgreSQL parents, root Go tests, Go lint and vet.
The PostgreSQL gates had no applicable skips. SQLite skipped only the three
explicit PostgreSQL migration/concurrent-connection tests.
The shared frontend suite passed 174 files and 1806 tests. After the final
private Snell QUIC-control visibility correction, all 27 native form tests and
fresh typecheck, lint, format and Vite build gates passed.

Evidence is retained at
`/root/task-evidence/native-snell-panel-task2-final-receipt.json`.
The initial all-source freeze wrapper detected frontend fixture changes and
exited 1; it is retained as a failed wrapper, not a passing command. Each of
its four Go commands exited 0, and all backend/generated-contract hashes
remained exact. The receipt records the bounded frontend changes and final
native validation. Earlier failed fixtures, RED tests and the canceled extra
Storybook invocation are preserved and excluded from passing evidence.

Task 3 remains: native Surge and Custom Xray configuration downloads, public
HTTP-to-core TCP/UDP/QUIC acceptance, pinned official-server checks, the single
whole-panel review, clean artifacts and authorized fork publication. This
checkpoint does not claim proprietary Surge device validation or completion
of the original broader project.
