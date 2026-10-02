# Paired Custom Xray distribution implementation plan

> Execution: use superpowers:executing-plans inline, TDD for behavior changes and verification-before-completion. Ordinary engineering decisions and isolated worktrees are already authorized.

**Goal:** Install, upgrade and package the panel with its matching Custom Xray source so native Snell, mieru, SSH and shared policy cannot be lost to an official-core replacement.

**Architecture:** Build both executables from one source revision and local module replacement. A bounded package manifest binds their checksums, target and required compiled features. Validate the complete staged candidate before stopping or replacing an existing installation; retain its configuration, business keys and accounting state. Docker and CI use the same builder. No release or deployment is authorized by this work.

**Stack:** Existing Go/pinned toolchains, shell installer/updater, Docker/Podman and GitHub build workflows. New packaging validation uses Go's standard library so installation does not need Python or jq.

**Spec:** `requirements.md` section 14, `architecture.md`, `accounting.md`, `deployment.md`, and the published native/shared-policy checkpoint `83f9c08e9ac027525e6dfd294d0f81853d19c90f`.

## Constraints and review focus

- Preserve all seven existing Linux release architecture targets and existing non-native resources. Never download official Xray as a fallback. Core/panel source stamps, hashes, toolchains and compiled dependencies must be recorded.
- Preserve databases, configuration, business SSH host keys and custom binaries. Management/Git authentication keys are never inspected, packaged or reused for business traffic.
- Missing/incompatible features, malformed manifests, wrong architecture, checksum mismatch, unsafe archive paths or symlinks fail before the installed tree or services change.
- Candidate validation is bounded and operates on compiled-feature information. It must not initialize/reset a policy store or contact an existing instance; runtime capability/authentication checks remain intact.
- Source archives without `.git` require an explicit verified source revision, not an `unknown` stamp. Dirty engineering builds are review artifacts, not final install packages.
- Test interrupted replacement and rollback; retain previous resources and state. Packaging alone does not solve coordinated-node or restore allocation fencing.
- Legacy MTProto/TUIC/AmneziaWG execution remains original migration work. Packaging must preserve existing functionality and must not claim that those data paths have moved into the core.
- Keep one whole-stage independent review and one correction batch. All worktrees, artifacts, failed logs and prior checksums remain retained. Push only the already-authorized fork feature; no default merge, force, release or production deployment.

## Task 1: Build and verify a source-matched package

Files: `core/xray/app/clientpolicy/command/command.go`, new `core/xray/main/commands/all/capabilities.go`, `internal/distribution/`, the panel CLI in `main.go`, `tools/build-custom-core.sh`, and new `tools/build-paired-package.sh`.

- [ ] Observe the actual preceding core rejecting the new offline `capabilities` command. Add a compiled-feature report from the same feature list used by authenticated `GetCapabilities`; never advertise instance durability in the offline report.
- [ ] Add meaningful tests for native/shared feature coverage, unchanged RPC authorization and absence of state mutation/listeners during the actual CLI probe.
- [ ] Implement strict bounded manifest decoding and streamed checksum verification. Require one panel/core pair, source revision, compatibility v1, normalized runtime binary path, target and native3/billing/rate/Tunnel compiled features. Reject missing/duplicate roles, unsafe paths, symlinks, changed files, wrong target, old/official binaries and mismatched source stamps.
- [ ] Build frontend, panel and core from one clean source. Generate compatibility/checksum/toolchain/source manifests and required license/origin resources; preserve existing auxiliary resources. Reject dirty source and nonempty output rather than replacing earlier artifacts.
- [ ] Exercise the verifier on a real clean paired package, tampered/missing files, an actual preceding core and malformed manifests. Record all failures and passing evidence; commit the working deliverable.

## Task 2: Wire safe distribution and replacement

Files: `Dockerfile`, `DockerInit.sh`, `DockerEntrypoint.sh`, `install.sh`, `update.sh`, `x-ui.sh`, `.github/workflows/release.yml`, `.github/workflows/docker.yml`, `.github/workflows/custom-core.yml`, and distribution staging tests.

- [ ] Replace official Xray downloads with the shared source builder and normalized target names. Pin Go/Node versions and validate image/platform inputs, including 386 and ARM variants; do not silently choose amd64 for an unknown target.
- [ ] Require a source revision in Docker without `.git`; verify the paired manifest in the image. Exercise an actual isolated Podman build and local container native3/shared-policy smoke, with private storage and no existing-container mutation.
- [ ] Resolve installer/updater/menu sources to this fork. Require checksums and a staged validated package before service stop or installed-directory changes. Supply an explicit local-package route for artifacts built without publishing a release.
- [ ] Safely stage archives, excluding traversal, links and unbounded extraction. Perform replacement in the installation parent's filesystem; retain a previous tree and preserve data, host keys and unknown custom resources. Inject preflight, extraction, promotion and interrupted replacement failures, proving the old installation remains usable or rolls back.
- [ ] Update all seven release targets to compile the managed core with the panel. Feature CI uploads review artifacts only; this task does not trigger a release workflow or publish images.
- [ ] Run actual temporary-prefix fresh-install/upgrade/rollback proof, SQL/core-state preservation checks and affected shell/Go/static tests; commit the working integration.

## Task 3: Close distribution acceptance and publish

- [ ] Run one whole-stage review, address its Important/Critical findings in one correction batch, then rerun appropriate regressions.
- [ ] Rebuild immutable paired artifacts from a new clean clone and verify versions, hashes, compiled native3 features, Docker contents and all existing Linux target builds. State unavailable platform/device gates accurately.
- [ ] Reuse real public native3 and bulk/Tunnel HTTP acceptance against the exact distributed core on SQLite/PostgreSQL; exercise fresh install, update and failed update with retained canonical identity/usage/business keys.
- [ ] Preserve the 39 prior panel/core artifact hashes and the completed shared-policy checkpoint. Record a requirement-to-evidence matrix without marking broader migration/global/restore work complete.
- [ ] Integrate clean source and documentation into the authorized feature branch, push and verify exact remote SHA; continue the original remaining requirements.
