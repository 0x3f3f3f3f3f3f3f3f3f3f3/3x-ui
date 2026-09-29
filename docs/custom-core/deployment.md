# Build, upgrade and recovery

Status: no deployment/release performed; installation changes pending. Existing installer/Docker currently fetch official Xray plus sidecars, so they do **not** yet deploy the requested final architecture.

Target reproducible workflow: clone this fork's feature branch; install pinned Go/Node toolchains; build `core/xray` from managed source and panel against its local module replacement; build frontend into the existing embedded dist; record panel/core revisions, compatibility version and SHA-256 for artifacts. CI must use these same steps and source. No developer cache edits or separately hosted custom-core repo.

Upgrade must validate custom capability v1 and new config before applying it, persist policy/accounting state before replacing binaries, retain the prior known-good binary/config, and restore those if validation/startup fails. User policy changes use hot control updates and should not restart all listeners. Official-core update paths must not silently replace a required custom core.

Backup must include SQLite/PostgreSQL data, policy/identity mappings, ledger/cursors, node allocations, separate business SSH host keys and recoverable core budget state. Keep all credentials protected. Restore requires an epoch/lease fence so a restored snapshot cannot issue quota already granted to a live or disconnected node.

Rollback after schema/ledger migration uses a coordinated pre-upgrade backup and matching panel/core binaries; never promise safe in-place DB downgrade. Revoke/fence outstanding node allocations before restoring. Exact tested commands will be added when packaging and recovery are implemented.


## Development initialization (not a completed installer)

Build with `bash tools/build-custom-core.sh`. For a **new** panel-assigned instance, initialize a private persistent path once:

```sh
build/custom-xray policy-init -file /private/path/policy.db -instance panel-assigned-instance-id
```

Set `clientPolicy.stateFile` to that path and `clientPolicy.instanceId` to the same ID, alongside `policies`. Existing, missing, corrupt, mismatched or locked state must not be deleted/reinitialized to bypass an error. Lost state requires authoritative ledger reconciliation; that workflow is not implemented yet. Keep the state on durable local storage; installer/Docker volume setup and backup fencing remain open.


## Interrupted live handoff

A live legacy-to-managed transition records its old child boot in the panel SQL
source before draining. Back up this source together with `legacy_traffic_receipts`,
client traffic, identities and the private policy state; do not omit the source's
handoff fields when moving between SQLite and PostgreSQL.

After a transient SQL failure, keep the original panel process alive and retry
activation: it retains the frozen final snapshot and receipt ID. Once final SQL
has committed, a panel restart can finish managed activation using that committed
usage. If the original panel and snapshot are lost before final SQL commits, the
new panel refuses to start business listeners. Preserve the database, core state
and available logs for authoritative reconciliation. Do not clear the handoff
marker or create a fresh policy store to get past the error: doing so can restore
already-spent quota. Automated reconciliation of an irretrievably lost legacy
snapshot is not implemented. A rollback still requires the coordinated
pre-upgrade backup and matching binaries described above.
