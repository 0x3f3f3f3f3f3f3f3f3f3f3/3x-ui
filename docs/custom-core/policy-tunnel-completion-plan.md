# Shared client policy and Tunnel completion

The user's priority after native Snell, mieru and SSH is multiplier billing,
per-client upload/download limits and TCP/UDP port forwarding through Xray
Tunnel/dokodemo-door. The existing accounting design and original acceptance
requirements remain authoritative. Execute inline, preserving all evidence and
prior artifacts. Ordinary engineering choices are already authorized.

## Scope and design

Reuse the canonical SQL client, exact multiplier parser, directional shared core
token buckets, durable accounting ledger and trusted Tunnel owner binding.
Adding listeners, proxy connections or SSH channels must not grant another
budget. Forwarding follows existing Xray routing. UI values must reach SQL,
generated config and the real data path; billing and remaining quota are distinct
from raw upload/download statistics. No additional policy engine is introduced.

The confirmed product gap is bulk client creation: the individual form already
supports traffic policy, while the bulk form omits it. Add the same nullable
whole-byte rate fields and exact decimal-string multiplier. Blank settings retain
legacy omission; explicit zero removes a directional limit. Every created client
receives its own policy object and canonical identity. Remote or missing listener
bindings must refuse explicit policy rather than silently omit it. Existing
Snell exclusive ownership and SSH authentication rules remain enforced.

## Task 1: Complete bulk policy controls

Files: `frontend/src/pages/clients/ClientBulkAddModal.tsx`,
`frontend/src/schemas/client.ts`,
`frontend/src/test/client-bulk-policy-form.test.tsx`.

Write and observe failing component tests before implementation. Verify exact
values on all generated payloads, independent credentials, native attachments,
legacy omission, unsupported-scope rejection and invalid multiplier handling.
Reuse existing policy translations. Run existing individual policy, native bulk,
renewal and schema validation tests plus frontend static checks. Commit this
working product change as one logical unit.

## Task 2: Close API and data-path acceptance gaps

Audit named existing tests and retained independent measurements against the
original Tunnel, rate and multiplier requirements before adding new tests.
Extend actual authenticated public API/SQL/core acceptance where coverage is
missing, including bulk-created policies and owned forwarding. Run against
SQLite and real isolated PostgreSQL. Exercise TCP and UDP, aggregate identity,
both directional limits, hot updates, quota/expiry/disable, exact lifetime and
period billing, restart/reset boundaries and sibling survival. Existing native
protocol acceptance supplies protocol-specific transport coverage; do not replace
it with mocks or count skipped gates as passing.

Retain and rerun applicable independent socket-byte measurements for at least two
nonzero rate tiers and an unlimited control, plus the 100 MiB quota at multiplier
2 boundary. State admission versus delivered-byte semantics and uncertainty
explicitly. Fix any reproduced product defect with a regression first.

## Task 3: Verify and publish the vertical

Update accounting/testing/deployment documentation and a requirement-to-evidence
matrix with precise pass, limitation and pending status. Perform one whole-stage
independent review and one correction batch, then appropriate regression checks,
generated-file checks and fresh builds. Preserve previous artifact hashes and
failed logs. Integrate only into the authorized feature branch and push only when
remote-write authorization permits it. Do not claim completion of unrelated
installation, distribution or multi-node requirements from this local vertical.
