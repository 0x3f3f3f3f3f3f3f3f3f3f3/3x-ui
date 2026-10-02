# Corrected paired-distribution acceptance

The single whole-stage correction batch is committed as
`2b36bc23e4faf374a8dc7bea3dced250d279132f`. All eight Important review findings
have source/component regressions and integrated artifact evidence below. No
second whole-stage review was run. The earlier clean packages and failed logs
remain historical evidence, unchanged.

A new clean clone at `/tmp/paired-distribution-corrected-clean-source-25op_wr7`
built `/root/3x-ui/build/paired-distribution-corrected-acceptance` in 121.817
seconds and remained clean afterward. Its manifest binds the actual compiler,
Go 1.27.1, Node 26.10.0, panel/core source stamps, all nine required compiled
features and frontend source/notices. The bounded archive is 335,193,628 bytes,
SHA-256 `896f631b754571089d34733bc4026715b5a6e08566c6c93d4521d97497da77f1`.
Actual extraction and strict verification passed for 566 declared files totaling
535,574,587 bytes, below the existing compressed/extracted limits.

| Requirement or correction | Observed evidence |
| --- | --- |
| Native Snell v4/v5/v6 including v5 QUIC, mieru TCP/UDP, SSH real OpenSSH/reverse/strict outbound; bulk policy and Tunnel billing | Exact packaged core SHA-256 `5c2a251ce758dd77efc5a6cb94551fe312d90ebc23ec31a9b5df423fd858dd14`; required HTTP/race acceptance passed **11 cases with zero skips on each SQLite/PostgreSQL backend**. Receipts `paired-distribution-corrected-native-http-{sqlite,postgres}.json` in `/root/task-evidence/`. |
| Immutable incoming package and mutable installed geodata | Nine actual CLI results: pristine/installed-geodata successes, seven rejection cases including rehashed previous actual core, wrong target, duplicate JSON and undeclared control. Original package hashes and configured state remained unchanged. `/tmp/paired-corrected-preflight-fyzqc_5w/receipt.json`. The earlier cross-mount hardlink fixture failed before its negative checks; its log remains preserved. Successful fixtures use one independent full copy with retained case deltas. |
| Fresh install, upgrade, external start failure, active panel with actual core bind failure, interrupted directory-swap retry | `/tmp/paired-corrected-lifecycle-qe18bu4x/receipt.json`: five private SQLite prefixes, actual panel/core subprocesses, newly generated business host keys and a service-manager substitute that operates only on those processes. Canonical identity/native credentials and custom resources survived. Failure/retry kept CURRENT usage `(5000,4000,11111)`, rather than old `(1024,2048,3792)`, and original menu/unit backups. This is not a host systemd/OpenRC deployment. |
| Core health rather than panel-only success | Failed-core update waited for readiness, rejected it and restored old code/current state. Separate authenticated status requests after all five outcomes showed core `running`, empty error and version `26.9.9`: `paired-distribution-corrected-core-health.json`. No production accounts were used. |
| Resource rollback, interrupted journals and completion fencing | Full distribution regressions/race/vet passed. Tests include omitted-old-resource collisions, no deleted-key resurrection, orphan temporary journals, prepared fresh rename before journal update, missing real health and different manifests sharing a source stamp. `paired-distribution-correction-batch-{race,vet}.log`; no actual power-cut test is claimed. |
| Local dependencies and BusyBox controls | Curl-absent/local-dependency test passed without package-manager calls. Native Alpine BusyBox 1.37.0 copied/restored menu/unit bytes and numeric permissions. Component receipts and boundaries are in `paired-activation-corrections.md`. |
| Complete embedded frontend source/notices | 182 locked npm packages, 399 regular closure files (398 declared plus metadata); source SRI/original notices, first-party inputs and emitted outputs verified. Linux/Docker/prebuilt/Windows assembly requires the closure. React's complete original notice was verified inside the new image. Supplements retain exact published attribution and identify their canonical terms. |
| Whole source regressions | Root Go: 50 tested packages passed; core Go: 98 tested packages passed. These totals do not claim zero optional skips. Distribution/frontend verifier race and vet passed; edited workflow actionlint and shell syntax passed. Full frontend suite including Storybook: **205 files / 1,916 tests**, exit 0, 593.72 seconds. |
| Seven Linux bootstrap architectures | New clean-source static verifier cross-builds passed for amd64, arm64, armv5/v6/v7, 386 and s390x, with build info, checksums and no ELF interpreter. `/tmp/paired-corrected-cross-verifiers.tvuKJu5Y`. This does not establish full CGO package/runtime acceptance on seven CPUs. |
| Earlier artifacts | All 41 previously recorded panel/core/helper artifact hashes verified unchanged: `paired-distribution-corrected-prior-artifact-preservation.json`. New packages, helpers, archive, fixtures and image use distinct names. |

The immutable native arm64 image is
`localhost/xui-paired-docker:corrected-2b36bc23-cache-bound`, image ID
`d91557020860a05d36faefb971b9e66fb674d4431d042aacd85ff41bf812f030`,
613,505,848 bytes. Its actual offline entrypoint/package/native3/source/resource
probes and corruption rejection passed; configured sentinels were unchanged.
Geodata rotation passes installed verification but is rejected as changed
incoming data. Evidence is under
`/tmp/paired-docker-corrected-cache-bound.HXaRhXKk`, including the build log,
`image-smoke-receipt.json` and `addendum/receipt.json`. These are offline image
checks, not native traffic or SQL runtime inside the container.

The first ordinary layered image build compiled the package but failed while
committing a container layer with `io: read/write on closed pipe`. Its log and
private storage remain at `/tmp/paired-docker-corrected.ifK0zyla`. The host then
had about 4.8 GiB free on a 145 GiB filesystem; that observation does not prove
the precise failure cause. The unchanged clean source succeeded with
`--layers=false` and shared Go module/build/npm content caches, avoiding another
copy of those caches into build layers. No old image or artifact was deleted.

Independent rate measurements used two connections, upload/download and rates
0/262144/1048576 bytes per second. All six original throughput/burst assertions
passed in `paired-distribution-corrected-rates-diagnostic.log`; retained fixture
paths are recorded there. Five additional debug baseline measurements passed.
However, the initial probe reported two broken pipes, and a separate ten-repeat
baseline attempt stopped on its first sender's two-second socket timeout while
the core was alive. These failures are retained in
`paired-distribution-corrected-independent-rates.log` and
`paired-distribution-corrected-rate-baseline-repeats.log`. The first probe's old
temporary-directory helper did not retain its core log; later diagnostic copies
do. The timing/connection anomaly remains unexplained. No rate business code or
throughput assertion was relaxed, and these results do not establish sustained
reliability under host load. The passing six-window result is narrower than that
claim.

Full seven-target CGO packages, non-native container execution, native Windows
runtime, physical client/device acceptance, and remote CI execution remain
unverified here. The seven-target CI definitions and read-only feature review
artifacts are wired; a source push is not itself evidence that they ran. Global
coordinated-node policy/credit fencing, restored-allocation fencing and legacy
MTProto/TUIC/AmneziaWG migration remain original development work. This checkpoint
protects the completed native/shared-policy path during distribution; it does
not mark the whole project complete or authorize a release/default merge or
production deployment.
