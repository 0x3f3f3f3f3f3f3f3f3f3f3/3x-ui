# Paired distribution CI verification

This change wires distribution builds to the local managed source and adds review
artifacts. No workflow was triggered, release published, registry image pushed or
installation deployed during this work. The existing native Snell, mieru, SSH,
Tunnel, accounting and shared-policy test jobs remain intact. MTProto and TUIC
remain separately downloaded legacy helpers; this packaging work does not claim
that either data path has migrated into Custom Xray.

## Workflow behavior

`release.yml` retains Linux `amd64`, `arm64`, `armv7`, `armv6`, `386`, `armv5` and
`s390x`. Each job checks out one revision and invokes
`tools/build-paired-package.sh` with exact Go 1.27.1 / Node 26.10.0 and a musl
cross-compiler. The panel uses CGO and static external linking for SQLite; the
managed core and installer verifier use CGO=0. ARM runtime core names normalize
to `xray-linux-arm32`, while the i386 target uses `xray-linux-386`.

The shared builder requires clean whole-source Git input, or an explicitly
verified full `SOURCE_REVISION` in a source context without Git. The generated
Linux manifest hashes the panel, core, verifier, service scripts, translations,
licenses, source/dependency archives, geodata and optional legacy helpers.
`licenses/build-toolchains.json` records the actual compiler path and version,
its pinned Bootlin download URL/SHA-256, and actual Go/Node versions. CI passes
this record through `PAIRED_RESOURCE_DIR` before manifest generation.

Each Linux review/release artifact contains four assets:

- `x-ui-linux-<platform>.tar.gz`
- `x-ui-linux-<platform>.tar.gz.sha256`
- `x-ui-package-linux-<platform>`
- `x-ui-package-linux-<platform>.sha256`

Both archive and standalone verifier checksums are required for remote
installation. The version-tag and rolling main publisher jobs upload the whole
asset set, including the standalone verifier. Feature pushes and release
`workflow_dispatch` only build/upload artifacts; neither publisher job receives
an eligible condition. Linux and Windows build jobs have read-only repository
permission.

Windows remains a separate native runner build because the paired Linux builder
and verifier format explicitly reject Windows. Its CGO panel and CGO=0 managed
core are both built from the clean local revision with source stamps. The runner
executes their offline source/target and compiled-feature probes, records file
hashes in `licenses/windows-build.json`, and includes corresponding source,
compiled module dependency archives, license/origin notices and actual MSYS2
compiler/package versions. This document does not claim Windows installer
manifest validation or configured Windows runtime readiness.

`custom-core.yml` pins all Go/Node setup inputs and adds distribution unit/race tests
and a native paired review build. The latter executes both offline verifiers,
rejects a tampered core, restores only the isolated candidate, re-verifies it and
uploads the same four bootstrap/archive assets.

`docker.yml` retains its five existing platforms:
`linux/amd64`, `linux/arm64/v8`, `linux/arm/v7`, `linux/arm/v6` and `linux/386`.
Review builds export an OCI archive with its checksum and pass the full
`SOURCE_REVISION` to the Docker build. Feature pushes have no login/push path;
only a version-tag push or an explicit publish dispatch from main enters the
separate publishing job. A dispatch defaults to review mode. This work does not
add armv5/s390x Docker support or claim that multi-platform images were executed.

## Pinned Bootlin inputs

The previous workflow resolved whichever stable tarball sorted last in a remote
directory. These jobs use the fixed `2025.08-1` stable musl release, verified
against Bootlin's own checksum endpoints before writing the workflow. The
[published aarch64 package summary](https://toolchains.bootlin.com/downloads/releases/toolchains/aarch64/summaries/aarch64--musl--stable-2025.08-1.csv)
and matching summaries for the other six targets identify GCC 14.3.0, binutils
2.43.1 and musl 1.2.5. Runtime compiler output is still recorded instead of
substituting those metadata values for an actual probe.

| Release platform | Bootlin target | Archive SHA-256 |
| --- | --- | --- |
| amd64 | x86-64 | `09fca3aa89540f1b01b5f4210d488cbeb00f522044c53e9989b1dd8a38076912` |
| arm64 | aarch64 | `defba831ffa1175236f137069333e21ed46d4d19feb5080a90cf248b6fc2cb08` |
| armv7 | armv7-eabihf | `2f3a34458c3a8b961bd09f89669130fcdc4c1dbc6e31ada720527e4ad3741c11` |
| armv6 | armv6-eabihf | `1a283f0936953c139d7171187414839c119997ea816de201f97e79ea3cfd6947` |
| 386 | x86-i686 | `3a340ebc386a057f5ddb24d8f4bbde07d2c013c99cebc02ac38565e2849bfda0` |
| armv5 | armv5-eabi | `8cdb4ad70c6b5a66427fa3315fe3ddde1c19c90232674dec59045e04d2a36cf1` |
| s390x | s390x-z13 | `23f536ff2bf1a9d3b93210465471996bc7c918fbb5702a277d8e1e42ffab8559` |

For each row, the archive name is
`<target>--musl--stable-2025.08-1.tar.xz`. Its official checksum URL is
`https://toolchains.bootlin.com/downloads/releases/toolchains/<target>/tarballs/<target>--musl--stable-2025.08-1.sha256`.
The workflow embeds that exact digest and fails on a changed download.

Bootlin's [official FAQ](https://toolchains.bootlin.com/faq.html) states that its
prebuilt compilers run on x86-64 hosts. This workspace is Linux arm64 and has no
available x86-64 QEMU executable or installed Bootlin compiler. The seven full
Linux CGO paired-package builds are therefore **unverified here**; preserving
and statically checking their CI matrix is not a claim that the matrix executed.

## Local evidence, 2026-10-02

Evidence is retained in `/tmp/paired-ci-evidence-20261002`; prior panel/core
outputs under `/root/3x-ui/build` were never replaced. These checks use the shared
engineering tree based on `0c11a956234b7b3c1ab4851995b132161426d7f2`, with concurrent
installer/Docker integration edits. They are review evidence, not immutable
installable packages or a clean-source final release build.

| Check | Observed result |
| --- | --- |
| Bootlin checksum and summary metadata, all seven targets | Fetch and comparison passed; exact public `.sha256`/`.csv` files retained. The first restricted network fetch failed DNS resolution; the authorized read-only fetch succeeded. No compiler archive download/execution is claimed. |
| actionlint 1.7.12 | Passed for all three edited workflows; downloaded public arm64 tool checksum verified against its release metadata. ShellCheck/Pyflakes are unavailable and disabled for this tool run. |
| YAML parsing, every non-PowerShell script `bash -n`, embedded Python compile checks | Passed via retained `validate_workflows.py`. PowerShell syntax/runtime execution is unavailable locally. |
| Matrix/resource/bootstrap contract checks | Passed: all seven Linux target mappings, seven official pins, both per-platform checksum names, all original Docker platforms, full source revision build arg, no official Xray distribution downloads, and read-only build permissions. |
| Publication conditions | Six representative cases passed: feature push/feature dispatch cannot publish; main push reaches only the dev release; version tag reaches tagged release and image publication; main dispatch defaults to review and only explicit Docker `publish=true` permits its publisher. |
| Seven CGO=0 static installer-verifier cross-builds | Passed for amd64/arm64/armv7/armv6/386/armv5/s390x from a frozen engineering snapshot. `file`, ELF no-interpreter checks, Go build metadata, all seven individual SHA-256 checks and native arm64 offline `info` passed. These are verifier builds, not full paired Linux package builds. |
| Windows amd64 CGO=0 managed-core cross-build | Passed from the frozen engineering snapshot. `file` reports PE32+ x86-64; `go version -m` reports Go 1.27.1, windows/amd64 and CGO=0 with local dependency replacements; the full managed-core source stamp is present in the PE binary. SHA-256 verification passed. No native Windows runner, offline execution or CGO panel execution is available here. |

The final verifier matrix outputs and per-target logs are retained at
`/tmp/paired-ci-evidence-20261002/cross-verifiers-if4Q7ve8`. The source snapshot
is recorded in `final-engineering-snapshot-directory.txt`; it consists of the
whole base Git archive plus the current distribution integration source. The
initial shared-tree attempt compiled five verifiers and failed on armv5 when the
new CLI briefly referenced an as-yet-unwritten `Rollback` function. Its exact
failure and five outputs are retained at `cross-verifiers-szaQSPU8`. A second
pre-final-CLI seven-target pass is retained at `cross-verifiers-XJYiA0Aj`.
Neither engineering attempt is presented as an immutable clean-source package.
The Windows managed-core executable, build log, build metadata and checksum are
retained at `windows-managed-core-uTKN4ERL`; its SHA-256 is
`46b6375b57bf8b43c7389e533034f361929e6ae7f0893de338572a791a65ff36`.
The build uses GOPROXY=off, GOFLAGS=-p=1 and GOMAXPROCS=1 and was held until the
Docker native build's heavy Go phase finished.

Workflow behavior is further checked by the root's whole-stage review and
clean-source final build. Actual remote workflow runs, seven full musl CGO
packages, native Windows runner probes and five-platform image execution remain
separate platform gates unless fresh evidence records them.

## Review-trigger correction, 2026-10-02

The authorized remote destination is `feature/custom-xray-unified-policy`;
`feature/paired-custom-core-distribution` is the local worktree branch. Both
names are now included in the release and Docker review push triggers. The
custom-core workflow already includes both. Docker's relevant path filters also
include `install.sh`, `update.sh` and `x-ui.sh`, since these bundled scripts alter
the image.

Actionlint 1.7.12 and the YAML/shell/Python/bootstrap/matrix contracts passed
again. Seven publication cases now include a push to the authorized remote
branch, which reaches no publisher. Structured before/after comparison confirms
that all release and Docker job definitions, including every publication gate,
remain unchanged. Logs, script fixtures and comparison records are retained at
`/tmp/paired-ci-evidence-20261002/review-trigger-correction-l3hk2je5`.

The distribution CLI subsequently gained incoming-only checks and inherited
lifecycle file-descriptor handling. The earlier verifier matrix remains evidence
for its frozen engineering snapshot; it does not validate these later CLI
changes. The final clean build will establish evidence for the complete source
revision. No commit, push or workflow trigger was performed for this correction.
