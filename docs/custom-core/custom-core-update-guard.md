# Custom core replacement protection

The panel requires the paired Custom Xray-core distribution for native Snell,
mieru, SSH and shared client traffic policy. Its previous official-core update
flow could replace that data plane with an incompatible standalone binary.

`GetXrayVersions`, its cached entry point and `UpdateXray` now refuse individual
official replacements before network access, process changes or file writes.
Existing fresh or stale release caches cannot offer a replacement. The version
window displays the current core version and explains the paired package
upgrade. Existing geofile update controls remain available. All thirteen locales
carry the new instructions. The HTTP refusal message uses a neutral custom-core
update title rather than the former success announcement.

The API registry, frontend OpenAPI, documentation-site OpenAPI and generated API
pages describe the refusal. Synchronizing the documentation-site spec also
includes the previously implemented native Snell API descriptions from the
authoritative frontend spec.

## Verification

The service regression exercises actual SQLite and isolated PostgreSQL databases,
retains sentinel core bytes, counts HTTP requests and seeds both cache ages. The
controller regression uses the real Gin handler and shipped English translations.
It first reproduced `success: false` with an incorrect success announcement,
then passed with the corrected title. The modal checks package instructions,
absence of official release controls and the retained geofile confirmation and
request. Its version fixture matches the backend's numeric status formatting.

Local evidence is retained under `/root/task-evidence/custom-core-distribution-*`:
the original failing runs, passing focused race runs, complete root Go regression,
frontend checks and generated-documentation commands are preserved separately.
One independent whole-change review found the response-title and documentation
consistency issues; both were corrected together. The response-title finding was
treated as important because users receive a contradictory success claim.

This guard does not implement or prove installer, release or Docker upgrades.
Those remain original requirements. The next user-prioritized work is the shared
multiplier/rate/Tunnel vertical, beginning with its missing bulk policy form and
remaining observable acceptance gaps.
