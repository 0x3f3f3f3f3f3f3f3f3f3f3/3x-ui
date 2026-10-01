# Custom core architecture and decisions

Status: design and source audit, not a deployment claim. Full acceptance is [requirements.md](requirements.md).

## Baseline and ownership

Work is isolated on a new branch from fork main. A managed `core/xray/` source directory retains upstream files, license and commit provenance. A root Go module replacement makes panel protobuf/config imports resolve to this source. This is preferable to module-cache editing (not reproducible) or a submodule in an unapproved additional remote repository. A patch-only representation would be smaller but harder to review/build offline.

The panel remains the only configuration authority and long-term ledger. Custom core owns authenticated identity binding, session registry, two directional per-client schedulers, metering and budget enforcement. gRPC extends existing control interfaces without renumbering any old fields. Protocol libraries may own handshake/encryption/transport but not independently select or dial ultimate destinations.

## Data flow

Authenticated account / exclusive trusted listener → stable client ID → per-session metadata → decoded payload policy admission → Xray Dispatcher/Router/DNS/balancer → selected outbound. Download payload crosses the same engine in reverse. Original and effective destination are retained. Control/health traffic uses separate unmanaged internal identities and is not billed as a user. A configured managed identity without an installed policy must fail closed.

The policy engine is instance-scoped, not a process-global singleton. All credentials and listeners owned by a client share its state. Policy updates wake blocked admission, re-evaluate active sessions and close affected TCP/channel/UDP resources outside engine locks. Registry cleanup must be idempotent. Admission wrappers must not expose zero-copy interfaces that bypass metering; explicitly guard raw-copy/XTLS decisions for managed sessions. Mux physical framing is not billed in addition to decoded child payload.

## Tunnel audit at v26.9.9

`infra/conf/xray.go` registers both `tunnel` and `dokodemo-door` to `DokodemoConfig`. `infra/conf/dokodemo.go` accepts `allowedNetwork`, `rewriteAddress`, `rewritePort`, `portMap`, `followRedirect`, `userLevel`; legacy `network`, `address`, `port` overwrite the respective new fields if present. Generated configuration will use one schema, not duplicate aliases.

`proxy/dokodemo/dokodemo.go` rewrites targets, creates an anonymous `MemoryUser` containing only Level, sets `CanSpliceCopy=1`, then calls `Dispatcher.DispatchLink`. TCP uses stream readers/writers; UDP uses packet readers/sequential writers and has a distinct transparent-forwarding branch. This proves existing userLevel is not a client identity. Required extensions: trusted client ID/legacy email, listener ACL and lifecycle binding, common runtime policy, and a managed fast-path guard. Default forwarding is explicit userspace L4, without transparent redirect/NAT or claimed source-address preservation.

## Creating clients before listeners

Canonical clients may be created before any listener exists. Empty or omitted
`inboundIds` on the existing client create API produce a new server-generated
stable UUID in one SQL transaction, without protocol credentials, traffic rows
or runtime activation. Duplicate email or subscription identity is an error,
never an implicit update. Tunnel owner selection subsequently links that same
canonical account; policy activation follows the existing local attachment path.

## Local Tunnel source access

A nonempty `settings.allowedSourceCidrs` admits only the physical TCP/UDP peer
before target selection, identity assignment, dispatch or payload accounting.
The canonical listener owner remains the billing identity; an allowed source
never becomes a client identity. The list is limited to 256 native IPv4/IPv6
CIDRs. Empty, omitted or null means unrestricted. IPv4-mapped IPv6 prefixes are
rejected; observed mapped IPv4 peers are unwrapped before membership checks.

The first implementation supports raw TCP/UDP and ordinary TCP TLS, including
its normal no-op header. It rejects PROXY protocol, non-raw transports, other
security/header wrappers, transport masks and Unix listeners whenever the ACL
is nonempty. Both JSON configuration and typed handler construction validate
these constraints. Configuration without the ACL retains legacy transport modes.

The panel accepts only the exact `allowedSourceCidrs` spelling to avoid
case-insensitive core JSON decoding and map reordering producing different
policies. Create/update validate before committing. An enabled ACL requires a
local Tunnel with exactly one canonical owner; startup also validates restored
rows. Last-owner detach/delete disables the listener while preserving its ACL,
including final canonical memberships added during deletion fanout. Runtime
reconciliation releases its ports after committing and revoking the identity.
Re-enable rechecks ownership in the write transaction.

`tunnel-source-acl-v1` is required independently of the trusted-owner capability
before startup preparation or hot handler mutations. ACL edits replace only the
affected handler, draining existing TCP/UDP flows. Other listeners remain live.
This increment does not claim source-address preservation, transparent NAT,
all transport wrappers or complete forwarding-mode/routing UI coverage.

## Local Tunnel outbound selection

`settings.outboundTag` is a custom field, carried as additive Tunnel protobuf
field 11. Empty, null or omitted selects normal Xray routing, including balancers.
A nonempty tag selects one concrete outbound through the existing dispatcher's
forced-detour context. That choice is consumed on the first dispatch: a selected
loopback then uses normal routing for its next hop. A missing selected handler
closes the link; it never selects the default handler. Unsupported outbound
network types retain that outbound's explicit failure behavior.

The panel validates the exact field spelling and string type. Enabled fixed
selections require one canonical local owner and one available template or active
subscription outbound. Creation, editing and re-enable validate transactionally.
An outbound removed later leaves the saved tag intact; traffic fails when the
corresponding configuration is applied, and re-enable refuses the unavailable
selection. Startup validates restored canonical ownership.

The existing Tunnel form shares the outbound configuration cache, lists concrete
tags, preserves unavailable saved choices visibly, and clears to normal routing.
Remote selection is read-only until remote managed policy support exists.
Changing or clearing a selection replaces only the listener, draining its old
TCP connections and UDP sessions without restarting the core or closing sibling
listeners. Stable owner identity and cumulative accounting are retained.

`tunnel-fixed-outbound-v1` is negotiated independently before startup preparation
and hot handler mutation. An older core cannot silently discard the new field.
An owned or explicitly selected Tunnel remains an opaque L4 forwarder even when
its configured target is the internal `v1.mux.cool` address; the mux wrapper passes
it to Dispatcher instead of interpreting the payload as internal channels. A
legacy Tunnel with neither owner nor fixed selection retains the upstream internal
mux gateway. Authenticated protocol mux paths retain their existing behavior and
shared policy enforcement.

## Existing paths requiring migration

### Native Mixed/SOCKS and HTTP identity increment

Task 5A implements the native core identity/lifecycle path and negotiates it in
the panel's low-level adapter. Real scoped socket/race tests verify this path;
panel canonical-account binding and production activation remain unimplemented. Existing
password accounts gain optional server-configured `clientId` and canonical
`email`. Username/password authentication returns an immutable `MemoryUser`
with the typed authenticated account and stable ID. A wire username never acts
as a client ID. Legacy accounts retain username-based statistics; anonymous
SOCKS/HTTP and SOCKS4 retain their existing behavior without a managed identity.
Managed identity on no-auth SOCKS is rejected. Anonymous resource ownership,
panel account-to-client binding/migration and remote/global policy remain later
work; the managed compiler must continue to reject unsupported activation.

Keep the existing protobuf `accounts` credential maps and all field numbers.
Add SOCKS fields 7 `client_ids` and 8 `account_emails`; HTTP fields 5
`client_ids`, 6 `account_emails` and 7 `require_authentication`. The latter keeps
an intentionally password-protected empty HTTP listener closed. Omitted legacy
settings preserve their old behavior. Metadata must resolve to an existing
credential, and managed duplicate usernames are rejected before construction.
HTTP's required-auth mode cannot turn anonymous after deleting its last user.
Changing a no-auth listener's mode requires configuration replacement rather
than an AddUser RPC silently changing authentication for existing streams.

A shared username/password validator holds immutable credential objects behind
a mutex. Authentication is a direct username lookup; removal removes both
indexes, then revokes the credential outside the lock. SOCKS and its built-in
HTTP fallback share this validator, so deleting either account view cannot leave
another protocol path active. Native UserManager CRUD/list APIs use typed SOCKS
or HTTP accounts. Closing a handler revokes all accounts and prevents later adds.
`trusted-socks-client-id-v1` and `trusted-http-client-id-v1` are advertised only
with the complete corresponding adapter; HTTP's empty required-auth setting
also requires the latter capability before handler/startup mutation.

SOCKS UDP retains the selected core's per-authenticated-TCP ephemeral listener.
It inherits the exact authenticated user, not a source-IP lookup. Credential
tracking covers the associated TCP connection before the first datagram, and
closing it must release its UDP listener safely even during timer setup. UDP
payload still passes through the existing Dispatcher and common policy engine.

Each HTTP request receives a separate inbound context and a connection lease.
An active request may close its physical connection on quota/disable/revocation;
after its response completes, the lease becomes inactive. Thus delayed cleanup
of user A's upstream cannot close a later user B request/CONNECT on the same
keep-alive client socket. Plain HTTP accounting retains the existing dispatched
HTTP-message boundary (serialized request/response headers plus body); CONNECT
and SOCKS meter the target payload. CONNECT owns the remaining connection
lifetime, so its dispatcher retains the original connection for unmanaged
Linux splice; earlier plain requests retain their inactive lease. Managed
connections retain the existing common policy restriction on splice.
No target dialer is added outside Xray.

Acceptance uses independent standard SOCKS5/HTTP clients, observable targets,
exact shared totals with Tunnel, wrong credentials, deletion/re-add, idle and
active UDP association cleanup, same-IP different users, last-account HTTP
refusal, ordinary anonymous regressions and delayed HTTP upstream cleanup.
Ordinary mux and native handler construction tests remain part of regression.

Source evidence: `internal/mtproto` supervises mtg-multi (one process per inbound); `internal/tuic` supervises tuic-server behind a panel UDP relay; `internal/amneziawgnet` runs AmneziaWG/gVisor inside the panel and bridges per-peer authenticated SOCKS into Xray. Their current behavior/data must be retained while moving to core adapters. Until all three migrations are tested, the installation is not fully single-core. Host administrative SSH and existing security services remain untouched.

## Protocol choices

Evaluate OpenSnell's GPLv3 implementation at a pinned commit, separately for each wire version; v5 QUIC Proxy Mode is its own gate. Official Surge documents v6 beta, deployment-derived shaping and no QUIC Proxy Mode. Use official mieru embedded server/client API, intercept authenticated requests before dialing. Use `golang.org/x/crypto/ssh` for SSH; reject session/shell/subsystem channels and verify outbound host keys. Reverse forwarding is disabled by default, bounded and authenticated, with no invented knowledge of the client's final target.

## Scope decisions

Local budgets/rates are not global limits. Multi-node work must allocate disjoint budgets and rate shares with bounded leases; expired/lost control cannot turn a limited user unlimited. Snapshot restore must be fenced from outstanding node leases. A recoverable core store is execution state under panel-issued policy, not a second administrator/control plane.

## Canonical password account ownership increment (database foundation)

The original task authorizes routine engineering decisions. This increment
uses the existing inbound settings and normalized `client_inbounds` membership
rather than a second credential store: each password account accepts an explicit
`ownerClientId` naming an existing stable client UUID. The command resolves that
UUID against canonical records inside the same SQL transaction that saves the
listener and reconciles distinct owner links. Wire usernames and passwords stay
resource-specific; they never pass through `applyClientRecordMerge`.

Do not infer owners from account names, supplied email or raw core `clientId`.
Strip transport identity fields from saved account commands; generated runtime
identity must be constructed from canonical records and verified memberships.
Owned accounts require a local authenticated listener. A managed listener must
bind every credential; mixing owned and unowned accounts is rejected. Multiple
aliases for one owner produce one membership. Duplicate managed usernames,
missing owners, noauth ownership and supplied client-stat mirrors are rejected
before persistence. Legacy unowned account lists retain their prior semantics.

Lock an existing listener and selected clients in deterministic order. Resolve
all owners before saving; a late SQL failure rolls back settings, memberships
and stat association together. Reassignment or credential rotation preserves
stable client records, quota history, shared credentials and policy. Detaching
the final account does not restore HTTP anonymous access; preserve an explicit
required-auth marker. Move detached stat associations to a sibling membership
or detached history instead of deleting shared usage.

The database foundation implements authoritative write/read guards, owner-link
reconciliation, detached-history retention and local/remote attachment
restrictions. A real PostgreSQL competing attachment waits on the canonical
client row lock and then rejects the newly local-only owner after the first
transaction commits. Local ownership requires this restriction even before an
explicit rate or billing policy exists.

Normalize native `users` aliases and JSON field case folding before validation,
including Unicode case equivalents accepted by Go's JSON decoder. Preserve the
native precedence: non-null `accounts` overrides `users`, including
`accounts: []`; null or omitted `accounts` leaves `users` active. Canonicalize active users
to accounts on writes. Reject ambiguous case spellings and owners on dormant
users, while retaining dormant legacy credentials. Strip supplied transport
identity from both arrays. Bare legacy email without a core client ID remains
readable because native authentication ignores it; the next write removes it.

Owned Mixed-to-empty-HTTP conversion retains required authentication. Ordinary
unowned conversion retains upstream compatibility without introducing a custom
core requirement. Frontend schemas preserve owner UUIDs and required-auth
markers through normal form edits. The later owner selection checkpoint verifies
the picker and preservation paths; generic lifecycle remains unfinished.

Task 5B1 kept both managed compilation and unmanaged activation of owned
accounts gated. Task 5B2 verifies canonical managed runtime generation;
owned accounts continue to require managed activation. Owner selection UI,
generic client lifecycle fanout and legacy username-counter migration remain
open. Task 5B3 verifies scoped grouped credential hot diffs. API/import/export continue using
existing settings JSON; restores must
preserve canonical stable IDs and links rather than minting replacement owners.

The runtime configuration increment derives every active account's native
email/stable-ID pair from that verified owner set and the same SQL snapshot as
its resource credentials. Global UUID/password fields do not supply password
proxy credentials. Runtime JSON removes panel-only owner selection and dormant
aliases. Policies include disabled linked owners; disabled account credentials
are omitted, with authentication protection retained when none remain.
Unowned credentials and anonymous empty HTTP listeners cannot enter this scoped
managed adapter. Protected empty listeners need no invented owner. Live legacy
username-counter handoff retains its protocol gate until an unambiguous mapping
and final settlement are independently verified.

Validate complete owner coverage before filtering disabled credentials. The
compiler never rewrites persisted resource credentials or canonical shared
credentials. Restored nonempty owned HTTP accounts imply protected
authentication even when their explicit marker was omitted or false; removing
the last account preserves protection. Ordinary unowned restores keep their
legacy removal behavior. Real generated Mixed SOCKS/HTTP, standalone HTTP and
Tunnel listeners share one core ledger, with independent target byte counts
and separate owner attribution verified on SQLite and PostgreSQL.

## Grouped managed password credential changes

The managed credential diff path avoids whole-listener replacement for scoped
account changes, preserving other owners' sessions. Keep legacy
ComputeHotDiff and its SOCKS restart guard; use a managed entry point subject
to native capability negotiation. A new per-username removal RPC is unnecessary:
the verified native removal revokes a canonical email's complete alias group.
Remove each changed old group once, then add every remaining new credential
in that group. All removals precede additions, including username transfers
between owners. Unchanged aliases of the changed owner also close; unrelated
owners and the same owner's Tunnel bindings remain live.

Only account-list changes on otherwise unchanged canonical Mixed/HTTP
listeners qualify. Complete exact user/pass plus canonical ID/email are
required; duplicate usernames or inconsistent ID/email groups are refused.
Compare groups independent of array order, preserve exact-case usernames and
sort operations. Address, protocol, transport, sniffing, authentication mode
and other settings changes retain existing replacement/restart behavior.

Negotiate password protocol identity, credential revocation and inbound-close
capabilities before preparation or handler writes, including removals without
additions. Resolve each removed identity from its original runtime account
group before removal. Native startup assigns listener userLevel; typed hot
additions preserve that level and reject malformed numeric input. Existing
partial-apply handling stops unsafe access and acknowledges only a fully
applied candidate. Verified real runtime tests preserve another owner's
sessions, Tunnel, the core boot ID and exact historical/ongoing usage on
successful saves. A lost acknowledgement after actual handler mutation stops
the whole core through the existing safety boundary; restart uses saved
credentials without replaying usage. UI, generic lifecycle and legacy
username-counter handoff remain separate work.

## Password account owner selection

Use the existing Mixed/HTTP account editor and client pagination API. Each
resource credential selects a canonical client UUID; labels are display-only.
Two aliases may select the same client. Never copy that client's shared
password, UUID, policy or traffic into the resource command. Legacy unowned
accounts remain editable. Once any row selects an owner, every active row
requires an owner. Mixed ownership requires password authentication; switching
authentication does not silently delete ownership or credentials.

Owner choices use 25-row pages and 300 ms server-search debounce, keyed below
the existing clients query root so client mutations invalidate them. Preserve
a selected UUID even when its row is outside the loaded/search result, and
preserve its selected display label across searches. Remote listeners and
Mixed noauth listeners do not request local choices and keep selection read-only.
Use Chinese/English help explaining shared limits and resource credentials;
maintain the repository's complete locale key set.

Keep the existing JSON API settings contract; document nested
`settings.accounts[].ownerClientId`, whole-list ownership and local-only
validation. The server remains authoritative for UUID resolution and scope.
JSON configuration export preserves resource credentials and owner UUIDs.
Single-inbound import still validates destination owners and rejects supplied
canonical traffic mirrors; it does not infer foreign-panel identity. A complete
database backup/restore or cross-database migration preserves canonical UUIDs,
memberships, credentials, policy and history together. Verify those paths
separately. Portable ownership remapping and generic lifecycle are later work.

Task 5B4 verifies this form and preservation contract with actual modal, API,
SQLite backup and PostgreSQL migration/export/restore tests. Generic owner
lifecycle, live legacy handoff and foreign-owner remapping remain unfinished.

## Password owner detach and deletion

Single/bulk client detach and deletion route through
explicit canonical owner UUIDs. Remove every matching resource alias; retain
other owners' credentials and account fields. Detach keeps the canonical client,
policy, stable ledger and all other memberships. Global deletion retains the
existing keepTraffic behavior and durable identity tombstone/revocation path.
Empty HTTP remains authentication-protected and Mixed retains password auth.
Repeated detach is idempotent. Never add a settings.clients credential mirror.

Lock and read the current inbound in the serialized SQL transaction, validate
the full saved ownership graph and resolve current canonical records. Filter
the current account list, preserving unrelated metadata and concurrent changes.
The generic settings merge only handles clients by email and cannot safely
merge password aliases; do not write a stale accounts array through it. Save
settings, specialized memberships and detached traffic in the same transaction.
Retain native users/case normalization and restored HTTP protection helpers.

Before filtering or fanout, public multi-inbound operations validate each
selected password owner and all its bindings. Reject unsupported remote scope,
malformed settings and stale memberships before another resource is changed.
Detect explicit UUID references separately from credential type validation,
including native case aliases and dormant users. A malformed password or
missing membership must not hide an affected owner. Single operations carry
the originally selected record rather than resolving its mutable email again.
Recheck authoritative scope and identity inside each transaction. Preserve
ordinary legacy-client behavior and existing bulk result reporting.

Before final global cleanup, lock canonical records in stable UUID order and
fence the captured UUID/email/subscription snapshot. A concurrent rename
returns a stale-command error before old-email traffic or subscription data
can affect a replacement owner; retry uses the current record. Recheck saved
password aliases at this boundary, so late attachments retain the canonical
record for ordinary acknowledged removal on retry. Already committed resource
removals are not rolled back by a later cleanup refusal.

After commit, use the verified managed reconcile/grouped credential path.
SQL failure changes no runtime state; uncertain runtime application retains the
saved command and follows existing stop/recovery semantics. Verify public
operations with real sockets, retained sibling/Tunnel flows, unchanged boot ID,
exact independently counted payload/ledger and restart recovery. Shared client
Update/enable/rename/quota/expiry, generic credential creation and live legacy
alias-counter handoff are later increments; this contract does not open them.

## Single shared password-owner updates

The next scoped increment extends ClientService.Update for an explicitly owned
local Mixed/HTTP resource, including password-only clients and ordinary/Tunnel
siblings. Shared name, credentials, quota, expiry, policy and enable fields
belong to the canonical record. Editing them must preserve the stable UUID,
lifetime ledger, resource usernames/passwords and specialized memberships.
Never mirror the update into password settings.clients or infer credentials
from the canonical shared password.

Validate all saved owner references, memberships and local-only scope before
optional inbound filtering or fanout. Preserve the original canonical ID/UUID
through ordinary sibling writes. Recheck that identity and the current scope
under SQL locks before email-based traffic or subscription updates; a changed
or reused email must not select a replacement owner. Omitted policy inherits
the current locked canonical value. Preserve existing credential omission,
clearable scalar fields and per-inbound flow override behavior.

Reuse ordinary resource writers and their existing partial-success/retry
contract. A shared rename updates every attached ordinary mirror's name,
including resources excluded by an inbound filter. Those resources retain
wire credentials, and preserve current canonical credentials even when a
selected resource has already committed a rotation. The first rename reserves
the unique email and migrates local/global/node metadata in one transaction.
Later writes use the locked current label and never sweep the freed old label
or detach memberships by it.

Subscription IDs retain their existing nonunique/shared-subscription model.
A changed subscription ID is checked against other records inside each
serialized canonical writer, including after a sibling applied the change.
This does not reserve that ID against independent raw SQL writes after the
final check; no cross-writer subscription uniqueness is claimed.

Skip password credential-list construction, then persist shared
canonical fields even when all attachments are password resources. Keep
canonical traffic/IP/global/node email metadata consistent while retaining
history. A concurrent resource password rotation must not be overwritten by
an old account array: shared-field updates leave that array untouched.

After persistence, use the existing managed candidate/grouped credential and
policy application path. A policy-only callback cannot update authentication
labels after a canonical rename. Successful changes retain unrelated owners
and the core boot; uncertain runtime application follows saved-command
stop/recovery semantics. Verify through actual public Update calls, independent
payload counts, stable ledger history and SQL row-lock interleavings.

Bulk enable/by-email field writers, generic resource credential creation,
live legacy alias-counter handoff, anonymous ownership and portable import
remapping remain later work. This spec alone establishes no implementation
or acceptance claim.


## Canonical password-owner field and bulk lifecycle

Extend the public shared enable read/set/toggle, IP limit, quota and expiry setters, plus BulkSetEnable, for explicit local Mixed/HTTP owners. Password-only and valid empty-Tunnel owners use authoritative membership and canonical enable rather than a nonexistent clients mirror. Preserve public return/Changed/Skipped/missing/duplicate contracts. A matching saved enable still reconciles pending managed intent, including stopped-core recovery, without pretending that a previous uncertain application succeeded.

Use a narrow intended-field mutation against the current locked canonical record. Do not replay stale unrelated canonical fields or merge old per-resource wire credentials back into the shared record. Preserve current policy, stable identity, lifetime raw/billed ledger, traffic counters and other restriction reasons. Update the corresponding traffic metadata field without zeroing counters. Ordinary mirrors receive only that intended shared field and timestamp; password account arrays, wire credentials, owner links and unrelated owners remain intact. Empty Tunnel clients mirrors are valid only with the sole selected canonical owner.

Before filtering/fanout, validate affected full password graphs and local-only scope. For the scoped local field batch, collect every attached inbound, acquire inbound locks in sorted order, reload/lock current inbound rows, then lock selected canonical identities in stable order. Revalidate current identity/scope/membership; if the captured graph changed, reject/retry rather than applying stale resource lists. Persist intended canonical/stat/mirror fields transactionally for this local subset. Follow commit with one existing managed candidate/grouped-policy application per bounded batch. Legacy ordinary/remote clients retain their existing independently reported fanout behavior.

Pin names to captured ID/UUID/email/SubID for the operation; an old-name replacement cannot be selected after a race. Bulk preflight reports invalid records before any affected resource write and excludes those identities from candidate batches. Valid selected aliases are grouped by UUID. Uncertain runtime application keeps saved field intent, stops the unacknowledged core and resumes through the same saved-command recovery; no fresh ledger seed or billing replay.

Capture membership before classifying password ownership. Validate that original selection after classification and inside every legacy writer transaction as well as the owned writer; gaining or losing the last password resource cannot turn a stale selection into permission for legacy fanout. Bulk errors after an owned commit retain its Changed, restart requirement and skipped reports.

Relative expiry requires successful first-use ledger settlement. Expiry propagation skips password account arrays, updates ordinary mirrors narrowly, and permits an empty Tunnel only when its sole authoritative owner is selected. Invalid late Tunnel membership rolls back expiry, receipt and totals together; repeated settlement preserves lifetime history.

Verify actual public operations on SQLite and PostgreSQL, negative scope/malformed/missing/ambiguous graphs, late rollback and label reuse, current-field preservation and PG locks. Real core proves active/idle TCP and UDP across A aliases/Tunnel and surviving B at the same source IP, exact independent target bytes/lifetime ledger, quota/expiry/enable and lost-response recovery. No other protocol family or live legacy alias-counter handoff is accepted by this increment.


## Unmatched legacy counter retention prerequisite

Normal native-stat settlement currently ignores usernames with no matching
client_traffics row, then advances the Process cursor. A final-only password
alias translation cannot recover those consumed deltas. Preserve currently
unmatched counters before opening Mixed/HTTP live handoff; this prerequisite
does not assign ownership or enable that gate.

Persist each exact raw label with its source Process ID, source accounting mode
and managed instance, raw upload/download, and a SHA-256 label key. Keep the
original label as reversibly encoded TEXT to support every valid UTF-8 label,
including NUL, long usernames and case-sensitive aliases; reject a different
original label at the same key. JSON string serialization preserves exact bytes
and distinguishes literal escape-looking labels. PostgreSQL NUL labels cannot
match client email TEXT and remain unassigned without binding an invalid query
parameter. Native parsing includes newline labels in normal and final batches. Nonnegative int64 additions must be exact:
overflow or late persistence failure rolls back the whole settlement. Use sorted,
bounded writes. Source metadata must remain consistent within a retained key.

Inside the established serialized settlement transaction, classify matching
traffic rows once under sorted row locks. Feed only that captured matched subset
to legacy accumulation and preserve its first-use/renewal behavior. Store the
unmatched subset with inbound/outbound amounts and the receipt atomically.
Later client/email creation never adopts retained historical usage automatically.

New receipts bind a deterministic original payload digest: process/sequence/batch,
final flag, source mode/instance and every relevant label/direction/amount. Retry
accepts only the same intent; ordering may canonicalize, changed effect cannot.
Validate identity, negative/duplicate/conflicting counter inputs before writes.
The applied effect must match the digest even when a validation callback runs.
Old receipt rows with no digest retain explicit identity-only retry compatibility;
never invent a prior digest or backfill missing old unknown bytes from a replay.
The next committed sequence gains an authentic digest.

Capture legacy/managed source mode and instance while creating the immutable
Process pending batch, before its SQL callback. Native username counters also
exist for a managed child; future handoff must never charge those bytes again.
Missing historical/manual source metadata is unknown provenance, not proof of
legacy mode. Retain that uncertainty rather than consulting a later current
child. Respect trafficMu/process.mu order in normal and final-drain paths.

Include the bucket and digest in schema migration, full backup and cross-database
export. Missing old tables/columns preserve existing data without modifying the
source or fabricating history. Verify actual ordinary polling, committed-but-lost
SQL acknowledgement, cursor retry/growth, late rollback, long/case-sensitive
labels, many labels, known siblings, and SQLite/PostgreSQL migration.

Password handoff still requires an unambiguous complete running-config mapping,
credential/level/resource/UUID fences, historical configuration proof, mapping
fingerprints and atomic bucket consumption with final settlement. A username
aggregated across different owners or colliding with another canonical label
must reject before drain. Already-dropped historic counters cannot be recovered
without independent evidence. No source IP, display name or case folding grants
ownership. This spec establishes no implementation or acceptance claim.


## Native startup configuration provenance prerequisite

Task5B8A preserves unmatched native counters and source accounting, but does not
prove the configuration under which their labels were emitted. Password handoff
therefore remains closed. Task5B8B records conservative startup evidence before
future mapping/consumption work. It grants no owner, rewrites no counter intent,
and enables no protocol or handoff gate. The user authorizes ordinary engineering
decisions autonomously; use the established inline plan and repository docs.

Capture two SHA-256 digests before starting the child: the logical configuration
passed to startConfig, and the effective configuration actually written after
automatic traffic-control injection. Canonical JSON removes object-key/whitespace
differences while preserving array order and every configuration field. Store
digests only, never additional raw credentials. A direct helper command with no
written configuration has unknown evidence. Managed control-only bootstrap
starts with ConfigStable=false because its business listeners are added after
the written snapshot; it remains managed and is never a legacy-adoption source.

The Process owns an immutable per-child proof with logical/effective digests and
a monotonic ConfigStable flag. Configuration replacement or policy replacement
that differs from the logical startup snapshot marks it false; restoring the old
configuration cannot make it true. A semantically identical object/whitespace
replacement preserves true. Conservative array-order invalidation is intentional:
it affects future proof eligibility, never normal counter conservation. Capture
proof before business traffic and reset it on each new child. Its fields are
guarded by process.mu; retain trafficMu→process.mu order and never add the reverse
order to SetConfig/CompareAndSetClientPolicy. Ordinary and final pending batches
copy the proof value before SQL, preserving it across later config changes/retry.

TrafficBatch carries optional ConfigProof {ConfigDigest, EffectiveConfigDigest,
ConfigStable}. Missing proof means unknown, including historical/manual batches.
Validate both present digests as lowercase 64-character hex; a partially populated
proof is invalid. Normalization clones the pointer value so a callback cannot
change caller evidence. Receipt hashing appends the optional proof after existing
intent fields and omits it when nil: historical Task5B8A nonempty digests remain
byte-for-byte retry-compatible. Present proof, including false stability, is
bound to original intent. Never fabricate proof while replaying an old receipt.

Persist LegacyTrafficConfigSource in the same settlement transaction as bucket,
known/inbound/outbound counters and receipt. ProcessID is the primary key;
ConfigDigest/EffectiveConfigDigest default to empty (unknown), ConfigStable to
false. Digests cannot change within a source row; empty historical evidence cannot
be promoted by a later configuration. Stability can only remain or become false.
Sort/lock source acquisition consistently after the process receipt and before
traffic row locks. A later failure rolls back source changes too. Changed proof
under the same committed receipt rejects; pending retry does not backfill a
changed current config. No bucket primary key or existing raw amounts change.

Include the optional source table in schema, SQLite backup and cross-database
migration/native export. Old schemas acquire an empty table; existing buckets
and receipts acquire no invented startup proof. Source schema stays unchanged
during export. Cross-process/panel restart recovery, owner mapping, historical
source eligibility and atomic bucket consumption remain later requirements.

Acceptance includes real legacy child startup and private API flow, proof captured
without a first poll, hot/config restore monotonicity, unknown direct command,
managed bootstrap exclusion, pending retries, ordinary/final snapshots, concurrent
config/traffic access, source drift and late transaction rollback. SQLite/PG
contracts must prove exact old-digest compatibility and backup/migration with old
table absence. Finish one read-only review, frozen production hashes, required
generation/lint/vet/full Go/affected race, scoped docs, exact fork push and distinct
clean artifact provenance before accepting this prerequisite.
