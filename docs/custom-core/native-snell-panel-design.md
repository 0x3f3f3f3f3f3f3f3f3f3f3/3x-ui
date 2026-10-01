# Native Snell panel design

This implementation follows the published local SSH/mieru panel checkpoint
d1aeb919. It executes inline in /tmp/3x-ui-native-snell-panel. Snell panel
capabilities become verified only through the acceptance below.

Use one protocol `snell` with explicit native version 4, 5 or 6. Every local
Snell service owns exactly one canonical SQL client. Its independent `snellPsk`
column never aliases UUID, password, SSH or mieru credentials. Existing client
attach consumes an unoccupied listener; create/bulk cannot place two owners on
one physical listener. Disabled owners still reserve membership/port. Empty
listeners are disabled; removing the final owner disables the listener and
releases its runtime sockets before another owner can acquire the port. SQL
stable UUID, rather than a supplied wire/runtime clientId, is the policy owner.

Resolve omitted PSK and generate an initial independent URL-safe PSK under the
serialized SQL writer. Validate a rotation against all linked Snell listeners,
including version-6 minimum length and disabled listeners. Runtime candidates
are pure projections of current canonical rows. Add trusted-snell-client-id-v1
capability negotiation before private/runtime preparation and use typed Snell
users for remove/add credential rotation. Preserve shared counters, directional
rates, multiplier/quota, disable/expiry and unrelated listener survival.

Validate physical/native settings before normalization can discard wrappers.
v4/v5 support off/http legacy obfs. v6 supports default/unshaped and no legacy
obfs; unsafe-raw is rejected because it cannot authenticate an owner. Version-5
core listeners require TCP+UDP on the same physical port even when the optional
outbound QUIC selection is absent. Version-4/6 ordinary UDP uses authenticated
TCP. No stream TLS/REALITY wrappers or global mux may be silently added.

Use existing inbound/outbound/client/attach/bulk/download workflows. Client
fields and protocol forms must preserve omission and independent credentials;
exclusive listener quantity/eligibility must be clear in both UI and server.
Native outbound exposes version, endpoint, independent PSK, version-compatible
obfs/mode/reuse and native v5 QUIC options with no fabricated wrapper fields.

Export native Surge profile text and actual Custom Xray JSON through existing
download controls, using canonical credentials and validated endpoint mappings.
Official Surge v5 selects QUIC Proxy Mode automatically: do not invent a profile
quic flag or snell:// URI. Explicitly refuse unsupported formats/host wrappers
and unrepresentable profile values; keep native JSON available when it can
represent the actual configuration. See https://manual.nssurge.com/policies/snell.html.

Verify real API-to-child native TCP, UDP and v5 QUIC alongside a shared-owner
Tunnel, exact counters and lifecycle/rotation/restart/port reuse on SQLite and
actual isolated PostgreSQL. Keep pinned source/native and official-server
interop gates. Real proprietary Surge inbound/device use remains separately
unverified. The official v5.0.1 large first UDP-over-TCP reply truncation is an
observed reference-server limitation, not proof of full reply interoperability;
strengthen the previously recorded v6 fixture reply assertion when touching
that acceptance path. Use one final whole-panel review and one correction pass.

The official profile format now specifies quoted values and literal quote/backslash
escapes (iOS5.21.0+/Mac6.8.0+): https://manual.nssurge.com/profile/format.html.
Proxy policy overview explicitly requires quotes for comma-containing values:
https://manual.nssurge.com/policies/overview.html. Preserve allowed literal UTF-8
rather than Go Unicode escapes, quote comma/comment/space-containing PSKs, and
reject multiline/control values in the text representation while retaining a
truthful native JSON export. Escaped-value minimum versions belong in download
instructions where they help users choose a compatible client.

Owner disable/expiry/quota retains the SQL owner and canonical PSK; the native
policy engine closes its flows and refuses business traffic. Credential rotation
uses typed remove/add and preserves the physical listener. Final detach/delete
disables an empty Snell resource and releases runtime TCP/UDP sockets before
reuse. Bound owners remain reserved even when disabled. An enabled SQL resource
without exactly one owner is rejected; a disabled empty resource may be staged.
Omitted PSK is preserved; removing authentication requires removing ownership,
not clearing a still-bound PSK. Generate an initial independent random32-byte
URL-safe PSK only inside the writer. Validate native PSKs as UTF-8 <=255bytes,
nonempty when bound, minimum12bytes on every linked v6 listener.

Only local resources/owners are in this increment; reject an uncoordinated
local/remote shared attachment. Accept native version4/5/6 explicitly and
preserve TCP+UDP reservation forv5. UI inbound does not invent a QUIC toggle
needed to enable the physical listener; outbound can choose native v5 QUIC.
Use one whole-panel reviewer after all3 tasks, with one correction pass.
Keep all earlier artifacts/evidence, normal authorized fork feature publishing,
and the existing prohibition on reading/reusing/modifying Git management keys.
