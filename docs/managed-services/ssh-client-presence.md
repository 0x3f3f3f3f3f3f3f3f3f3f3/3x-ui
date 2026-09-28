# SSH client online and source IP integration

This continues Task 5 of [the main plan](plan.md), under the already approved
[requirements](requirements.zh-CN.md). Execute inline with real connections and
RED-first tests. The remaining protocols and full SSH management stay required.

## Design

Observe admitted SSH transports, including idle `-L`, `-D` and `-R` sessions.
Only signed authentication followed by successful policy admission counts as a
client online observation. Take the source IP from the accepted TCP connection,
never from the client-supplied forwarding origin or the internal bridge.
Multiple transports/channels from one address remain one IP observation for
that client; different authenticated clients behind the same IP remain distinct.
This identifies connections and source IPs, not devices or physical users.

Read copied server snapshots through the existing manager's lock order, without
starting or reconciling a runtime. Reject a replaced database handle. Resolve
stable policy IDs against canonical client/inbound membership so removed,
renamed or reassigned identities cannot inherit another session's observation.
The current local SSH runtime depends on its matched running Xray router.

Fold observations into the existing traffic job's online/last-online and active
inbound sets and into the existing IP collector. Preserve native observations,
online grace periods, IP retention and the operator's IP tracking setting.
SSH collection must not depend on the core's online-stats RPC capability.
No new endpoint, stored model, device identifier or enforcement claim is added.
IP/device enforcement and remote SSH execution remain open requirements.

## Steps and evidence

- [x] Server snapshot: real unsigned, admission-pending, admitted and revoked
  transports; same-client duplicates, independent same-IP clients, copied data.
- [x] Manager/service: actual Xray/SSH lifecycle on SQLite and PostgreSQL;
  idle sessions, exact identity/IP/tag attribution, membership/DB replacement.
- [x] Existing online/IP collectors: merge SSH with native observations,
  preserve native fallback/error handling and persist through existing paths.
- [x] Focused/race and Go regression suites, static checks, real panel evidence,
  operations/matrix/validation records, including initial failures and rechecks.

Deliver this milestone through a logical commit on the approved feature branch,
then push and independently verify its remote SHA. The full task stays open.

The native ban publisher is intentionally bypassed for SSH observations: a
host-wide IP ban would also affect independent users sharing the source address.
An actual collector regression checks that SSH IPs persist without a ban log.
The existing native traffic poll still gates online-list refresh; SSH IP
collection can proceed when the native online RPC is unavailable.
