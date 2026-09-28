# Build, upgrade and recovery

Status: no deployment/release performed; installation changes pending. Existing installer/Docker currently fetch official Xray plus sidecars, so they do **not** yet deploy the requested final architecture.

Target reproducible workflow: clone this fork's feature branch; install pinned Go/Node toolchains; build `core/xray` from managed source and panel against its local module replacement; build frontend into the existing embedded dist; record panel/core revisions, compatibility version and SHA-256 for artifacts. CI must use these same steps and source. No developer cache edits or separately hosted custom-core repo.

Upgrade must validate custom capability v1 and new config before applying it, persist policy/accounting state before replacing binaries, retain the prior known-good binary/config, and restore those if validation/startup fails. User policy changes use hot control updates and should not restart all listeners. Official-core update paths must not silently replace a required custom core.

Backup must include SQLite/PostgreSQL data, policy/identity mappings, ledger/cursors, node allocations, separate business SSH host keys and recoverable core budget state. Keep all credentials protected. Restore requires an epoch/lease fence so a restored snapshot cannot issue quota already granted to a live or disconnected node.

Rollback after schema/ledger migration uses a coordinated pre-upgrade backup and matching panel/core binaries; never promise safe in-place DB downgrade. Revoke/fence outstanding node allocations before restoring. Exact tested commands will be added when packaging and recovery are implemented.
