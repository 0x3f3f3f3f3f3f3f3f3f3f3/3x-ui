# Verification record

Only executed evidence counts. `unverified`, `unimplemented` and justified `not applicable` are not passing tests. Full required tests are in requirements §15 A–H and the implementation plan.

Environment: Linux arm64, isolated `/root/3x-ui` checkout from fork main `17d7dd46`; Go 1.27.1 downloaded via verified Go toolchain mechanism; Node initially 22.23.1. Never use live host management SSH, firewall or production listeners for protocol testing. Use loopback/temp directories and ephemeral test endpoints.

| Check | Command / input | Expected | Actual |
| --- | --- | --- | --- |
| Branch origin | `git rev-parse HEAD main` immediately after branch creation | equal main SHA | both `17d7dd46b512d0a9c22921a6094f30c672e436c9`; clean |
| Git SSH | `ssh -T` with BatchMode, strict host checking and supplied key | authenticated GitHub identity | success greeting; exit 1 is GitHub's no-shell convention |
| Go dependencies | `go mod download` | pinned graph resolves | passed with network permission; initial sandbox DNS denial documented |
| Panel baseline | `make test-go` | all applicable backend tests pass; skips separately reviewed | sandbox run failed on denied socket creation; identical authorized rerun in progress (`panel-baseline-tests-authorized.log`) |
| Frontend baseline | Node 26, `npm ci`, `npm test`, build | all tests/build | not yet run |
| Custom core build | `tools/build-custom-core.sh` | distinctive binary and source SHA | not yet implemented/run |
| Protocol and policy tests | requirements §15 | real data-path evidence | not yet implemented/run |

Keep credentials out of logs and reports. Record command, input, expected/actual, environment and revision with every new row. A baseline test failure is not caused by this feature unless a before/after comparison proves it. Restricted socket permissions require rerunning in an authorized test environment, not changing expectations or skipping tests.
