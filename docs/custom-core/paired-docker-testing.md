# Paired Docker distribution evidence

Docker uses `tools/build-paired-package.sh` for the panel, managed Custom Xray,
static package verifier, resources and manifest. The native Snell, mieru and SSH
capabilities, fixed-point billing, shared directional rates and Tunnel controls
remain required by that verifier. No Docker stage downloads official Xray.

The frontend builds from the same context in a native `BUILDPLATFORM` stage,
using Node 26.10.0. The target-platform Go 1.27.1 stage supplies the validated
frontend through `PAIRED_FRONTEND_DIR` and `PAIRED_NODE_VERSION`, then compiles the
CGO panel and managed core on the target CPU. The final Alpine image verifies the
complete package; its entrypoint verifies again before helper or service startup.
Requested offline CLI commands skip fail2ban and certificate renewal.

## Image pins and platform limits

The three Docker Official Image index digests were read from the public registry
on 2026-10-02. Raw manifests and platform lists are retained in
`/tmp/xui-paired-docker-review/image-pins.json` and `*-image-manifest.json`.

| Stage | Immutable image |
| --- | --- |
| Frontend | `node:26.10.0-alpine3.24@sha256:0b36e8c136b94cd4fcf02188228e76c31ad5872eef3fec8cbd2eee500cfd9e80` |
| Builder | `golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414` |
| Runtime | `alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6` |

Official build definitions: [Node](https://github.com/docker-library/official-images/blob/master/library/node),
[Go](https://github.com/docker-library/official-images/blob/master/library/golang)
and [Alpine](https://github.com/docker-library/official-images/blob/master/library/alpine).
Node 26's Alpine images cover amd64 and arm64 build hosts. Native or emulated
Docker targets remain linux/amd64, linux/arm64/v8, linux/386, linux/arm/v6 and
linux/arm/v7. DockerInit explicitly maps all seven package targets, including
ARMv5 and s390x, but the pinned Go/Alpine images have no ARMv5 manifest. ARMv5
packages continue through the release builder. Platform mappings do not establish
successful emulated or device execution; those gates require separate runs.

MTProto stays the existing sidecar, pinned to mtg-multi v1.15.0 and checked against
its published archive checksum. ARM packages retain both architecture resource
names and the `mtg-linux-arm` name expected by the panel. TUIC stays at its existing
1.0.0 release for the four supported CPUs. Six geodata resources remain from the
existing upstream feeds; their exact downloaded bytes are bound by the package
manifest. Legacy native migration and immutable upstream geodata release pins
remain separate work.

## Retained local checks

A generated fixture harness at
`/tmp/xui-paired-docker-review/script-contract-test.py` executes the scripts.
It tests seven target mappings, five invalid target rejections before any network
access, package-verification failure before panel startup, and requested offline
commands without helper or database initialization. The original script failed
all 14 cases in `script-contract-red.json`; the revised scripts passed all 14 in
`script-contract-green.json`. An additional fail2ban-enabled probe reproduced
helper startup during offline commands in `offline-command-red.json` and passed
in `offline-command-green.json`. The tests use generated executable fixtures,
never management/Git SSH keys. `sh -n DockerInit.sh DockerEntrypoint.sh` and
`git diff --check` also pass.
`source-preflight.json` records five missing/malformed source revisions rejected
before resource downloads or package creation.

## Isolated container review build

The Docker CLI in this environment aliases Podman. Its default storage cannot
write `/run/libpod/events`, so the proof uses only this private store:

```sh
podman --root /tmp/xui-paired-container-proof/storage \
  --runroot /tmp/xui-paired-container-proof/runroot --events-backend=file \
  info --format '{{.Host.Arch}}/{{.Host.OS}}'
```

It reported `arm64/linux`. No existing container, service or store is inspected
or changed. The review context is an archive of committed base
`0c11a956234b7b3c1ab4851995b132161426d7f2` plus prospective Docker/shared-builder
changes, without `.git`. It is explicitly a **dirty review build**, not a final
source-stamped artifact. `context-initial-dirty-review.json` records every overlay
hash. A clean final rebuild after integration remains required.

```sh
podman --root /tmp/xui-paired-container-proof/storage \
  --runroot /tmp/xui-paired-container-proof/runroot --events-backend=file \
  build --platform linux/arm64/v8 \
  --build-arg TARGETOS=linux --build-arg TARGETARCH=arm64 \
  --build-arg TARGETVARIANT=v8 \
  --build-arg SOURCE_REVISION=0c11a956234b7b3c1ab4851995b132161426d7f2 \
  --build-arg PAIRED_DEV_BUILD=1 \
  --tag localhost/xui-paired-docker:initial-dirty-review \
  /tmp/xui-paired-docker-review/context-initial-dirty-review
```

`build-initial-dirty-review.log` preserves the complete **passing** output,
including npm's existing audit result (one low and one high finding) and the
environment's ambient-capability warnings. The resulting local image ID is
`8e86dfb085047a2df19bbe3995fcd9602e839aaa2149f7bf19da4fc025d48f89`.
`build-missing-source-revision.log` records an actual Docker build rejecting a
missing revision at the first frontend check, before npm or package compilation.

`image-smoke-receipt.json` records four successful actual container probes and
one expected corruption rejection:

| Actual image probe | Result |
| --- | --- |
| `/app/x-ui-package verify /app` | Passed all 164 declared package files |
| `/app/x-ui package info` | Source revision, linux/arm64 target and Go 1.27.1 agree with package/core |
| `/app/bin/xray-linux-arm64 capabilities` | All nine required native3/billing/directional/Tunnel compiled features present |
| Resource probe | Matching core, MTProto/TUIC, six geodata files and corresponding source retained |
| Modified core then entrypoint startup | Rejected changed core before panel/helper startup |

All probes use `--network none --cap-drop ALL`, retain their new containers and
run with generated business-state sentinels mounted at `/etc/x-ui`. Their file
names and SHA-256 values were unchanged, with no database created. These are
offline packaging checks; they do not establish live protocol traffic, SQL
schema lifecycle, commercial device coverage or legacy native migration.

The first harness run used `--cgroups disabled`, which this environment's runc
rejects before startup. That failure remains in `disabled-cgroups-*`; the minimal
default-cgroup probe passed and the complete harness passed after using that
default. `smoke-image.py`, every probe's stdout/stderr and the retained generated
fixtures are under `/tmp/xui-paired-docker-review`. No image was pushed, released
or deployed.
