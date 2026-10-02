# Activation and interrupted-installer corrections

This is the installer/runtime portion of the single correction batch following
review of `83f9c08e..a980ef95`. It preserves the original packages, review
reproductions and failed test logs. It does not close the remaining global
policy, restore fencing, legacy protocol migration or device acceptance work.

A successful system service start can leave the panel alive while its managed
core has failed. Source-stamped Linux panels now publish a private
`panel-health.json` in their configured database folder once per second. The
record contains only source, installation path, readiness and process identity.
Readiness requires the managed control interface and no held-back configuration.
The installer checks the immutable installed pair, a fresh private regular
record, both executable inodes and kernel process start ticks. Dead children,
PID reuse, another source/root and stale or unready records fail within the
20-second activation bound. Shutdown publishes unready immediately. Windows
does not publish this Linux-specific file.

`package complete INSTALLED HEALTH_FILE` clears the pending journal only after
that check. `complete-offline` records `installed-offline`, which makes no
runtime activation claim and belongs only to the explicitly quiescent
package-only route. Completion binds the exact package-manifest SHA-256 as well
as the source revision. A prepared fresh transaction whose candidate was already
renamed remains pending and requires activation; the absence of a previous tree
cannot establish success.

Normal retries validate the original private installer workspace and completed
control snapshot before stopping anything. An interrupted upgrade is rolled
back using current business state and its original menu/unit backups before a
new request is promoted. An interrupted fresh installation completes its
configuration and real activation before becoming a rollback baseline. If that
configuration has not finished, the install entrypoint is required rather than
a configuration-free update invocation. Missing original snapshots fail with
all trees and journals retained. No recovery path restores old accounting data.

Local installation checks existing dependencies and does not refresh package
repositories. Missing dependencies and unsupported service-template paths fail
before service stop. Local update does not require curl. Control-file copying
uses numeric modes from portable `stat -c %a` and ordinary `chmod`; it supports
Alpine BusyBox for both installation and restoration.

Component evidence in `/root/task-evidence/`:

- `paired-distribution-runtime-health-red.log` preserves the original missing
  health API test failure. Current live-process tests accept a matching pair and
  reject stale/future/unready records, wrong source/root, reused start ticks,
  wrong executable, symlinks, public permissions and a dead core behind a live
  panel. They use copied test executables, not a real native protocol core.
- `paired-distribution-corrections-integration-second.log` and
  `paired-distribution-corrections-race.log` passed the then-current complete
  distribution package. The later binding tests initially failed in
  `paired-distribution-recovery-activation-binding-red.log` and
  `paired-distribution-recovery-package-binding-red.log`; their corrected full
  package passed in `paired-distribution-recovery-activation-binding-green.log`.
  An initial binding fixture failed to reach its assertion because generation
  correctly refused an existing manifest; the separate package-binding red log
  records the actual reproduced defect.
- `paired-distribution-health-runtime-regression.log` passed affected panel
  service and core process policy/startup tests using isolated loopback sockets.
- `/tmp/paired-installer-corrections-fymoe4g8/receipt.json` proves local dependency
  checks without curl/package-manager calls, numeric mode copying and rejection
  of a missing dependency. This component test does not prove full activation.
- `/tmp/paired-busybox-controls.ZnBNyNk4/receipt.json` proves actual menu/unit
  copying and restoration in the retained private Alpine image with BusyBox
  1.37.0; it records the exact tested library hash. No host service was changed.

These are source/component checks. The earlier clean `a980ef95` package has no
new readiness reporter and remains an immutable historical proof. Fresh clean
package, service failure/retry, container and exact-core SQL acceptance are
still required for the integrated corrected artifact.
