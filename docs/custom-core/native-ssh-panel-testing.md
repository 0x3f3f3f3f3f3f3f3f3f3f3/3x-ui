# Native SSH panel verification

## Backend increment — 2026-10-01

The isolated backend increment extends the existing client/inbound APIs with
separate SSH username, authorized-key lines and password. SQL stable identity
owns policy and usage. Omissions and explicit clear commands are resolved inside
the serial SQL writer; attachment reads current credentials rather than restoring
an older snapshot. Disabled usernames remain reserved on every linked listener.
Both directions of uncoordinated local/remote binding are refused before writes.

Each service owns a generated Ed25519 host key through an immutable SQL UUID.
Create/clone generates a new trust identity; edit, file loss, restart and full
database restore retain the original key. Candidate compilation references a
confined path without writing PEM. After native capability negotiation, runtime
preparation uses anchored os.Root access, real 0700 directories and 0600 files,
rejects links/conflicting contents and installs a synced file atomically without
replacing an existing file. Public options expose only the host public key and
fingerprint. No management/Git SSH key is read or reused.

Native users contain SQL-owned clientId/email and independent authentication.
Hot account rotation removes its old generation and preserves other users and
the owner's Tunnel. Empty listeners and removals require
trusted-ssh-client-id-v1; the immutable preceding native core is refused before
private file preparation. Native server option validation uses the core's pure
resource/reverse validator. Incoming users, host paths, owner IDs, unsupported
stream wrappers and invalid native options are refused before SQL mutation.

Outbound validation requires a parsed public host-key pin. A configured business
private key must be an actual parsed 0600 regular file directly under the private
XUI_DB_FOLDER/native-ssh/outbound directory. Validation anchors access there;
request paths outside it are rejected before opening. UDP, TLS wrappers and
global mux are unsupported. Saved/restored templates receive the same guard.

Fresh evidence under /root/task-evidence/native-ssh-panel-task1-* includes:

- Behavioral account, clear, invalid key, concurrent omission/attachment,
  cross-listener username/method, private-path, typed-account, hot-diff, capability,
  runtime identity and public response RED -> GREEN logs. Failed logs are retained.
- Real child SSH channels through public service CRUD/attach/rotate/disable/delete/
  restart, sibling survival and exact shared Tunnel usage. Seeded 100 upload/
  200 download becomes 111/211 with 344 billed after SSH+Tunnel traffic, then
  127/227 with 408 billed after rotation/re-enable; history is not repriced.
- Real SSH handshakes present the original SQL public fingerprint after file loss
  and after restoring a panel database snapshot and recreating the missing file.
- SQLite old schema/backup/dump/restore and actual isolated PostgreSQL upgrade plus
  SQLite->PG->SQLite->dump/restore retain independent credentials, SQL stable ID,
  existing traffic, the exact private host key and public fingerprint. SQLite
  specific tests explicitly use SQLite even in a PG acceptance command.
- 35 SSH backend top-level cases: 33 applicable to the SQLite acceptance set; 35
  in the PG set including SQLite-specific recovery coverage. The final two cases
  have separate fresh correction/recovery logs. Every required name is checked
  against a top-level PASS; PostgreSQL-only SKIPs in SQLite are not passes.
- The existing 23 native SSH required names passed fresh core race checks with real
  OpenSSH forwarding/reverse, strict outbound, shared policy/usage and lifecycle.
- Full shuffled root Go/AWG regression passed; generation and generated TypeScript
  typecheck passed. A JSON slice parameter compilation issue was corrected before
  rerunning the full gate. Final affected lint/vet and response/path tests cover
  subsequent narrowly scoped corrections.

The first task's review child /tmp/native-ssh-panel-task1-marker-core is a local
development binary built while implementing the marker, before the pure option
validator was added. It is not a published clean checkpoint. Its predecessor
fixture is the preserved custom-xray-native-mieru-panel-snell-quic-checkpoint.
The final panel branch will build and verify a fresh core from its exact source.

## Existing forms increment — 2026-10-01

Task 2 adds native SSH to the existing client, bulk client, inbound and outbound
forms. Username, multiline authorized public keys and password remain separate
from other protocols. Clear-password and clear-public-key switches submit explicit
commands; blank inputs preserve stored credentials. Client creation/attachment
requires a public key or a password allowed by every selected SSH listener.
Public options now include sshAllowPassword alongside the public host key and
fingerprint, so mixed bindings can check actual listener authentication.

Bulk accounts use supplied public keys with distinct server-generated usernames.
Independent per-account SSH passwords require explicit opt-in and password
permission on all selected SSH listeners. No client private key is generated.
Listener forms expose bounded native resources and explicit reverse bind IP,
port-range and source-CIDR controls. Reverse descriptions state that the client
chooses its final target. Outbound forms require the actual host public key and
password or a provisioned business key path. UDP, TLS/REALITY, transport wrappers
and global mux are refused before submission; explicit disabled-mux UDP options
are retained as validation errors instead of silently discarded.

Real component tests exposed stale default-protocol initialization adding a TCP
wrapper to a reopened SSH outbound. Initialization now checks the current form
protocol; unsupported native mask controls are hidden. Native empty stream
values match the core, SSH listener ports must be one integer, and other
protocols retain their existing transport validation. EN/ZH messages cover all
new controls and validation errors.

Final Task 2 evidence under /root/task-evidence/native-ssh-panel-task2-*:

- 17 native SSH frontend cases cover actual create/edit/attach/bulk, credential
  bounds, explicit clear/last-method rejection, public trust, native resources,
  reverse controls, pinned business-key/password outbounds and JSON workflows.
- Final affected frontend regression: 28 files, 539 tests passed, including
  native mieru and existing client/inbound/outbound/Tunnel workflows.
- SQLite and isolated actual PostgreSQL public host-trust/authentication-option
  race checks passed; affected Go vet and repository lint passed (0 issues).
- Final generated TypeScript typecheck, frontend lint, affected formatting check
  and Vite build passed. All five generated API artifacts were byte-identical
  on repeat generation. The receipt records hashes of all 36 changed product
  and test files and confirms they did not change during the final gates.
- Initial localhost sandbox startup refusal and test selector/type mistakes are
  retained as environment/test-harness evidence, separate from behavioral RED
  failures and their corrections.

## Remaining acceptance

Existing client/inbound/outbound forms and EN/ZH validation passed Task 2. OpenSSH
config/known_hosts downloads and public HTTP -> real OpenSSH-L/-D/authorized-R
acceptance are Task 3; the current Go SSH service test does not substitute for
those checks. Whole-panel review, final clean artifacts and fork push follow
all three tasks. Remote key provisioning, coordinated remote budgets, Snell
panel integration and the broader original project remain open. No deployment
or default-branch merge is implied by these local backend checks.
