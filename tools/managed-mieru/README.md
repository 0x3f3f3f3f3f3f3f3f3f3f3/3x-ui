# Managed mieru protocol source

The panel's internal mieru adapter uses a maintained copy of the official
`pkg/protocol` package. The official client and all other mieru dependencies
remain the unchanged Go module. This is an internal server extension, not a
claim that public service creation, deployment or all acceptance tests are done.

Source: [mieru v3.38.0](https://github.com/enfein/mieru/tree/b961978c3be9dd26b94158487c760858e19d1db2/pkg/protocol).
[source.json](source.json) pins the module, commit, Go checksums and the SHA-256
of each copied source/test file and license. The 12 production files and 12
upstream test files preserve their original copyright and license headers.
The code and modifications are GPL-3.0-or-later; the full license is included
in [internal/mieru/native/LICENSE](../../internal/mieru/native/LICENSE).
Distribute the corresponding modified source and license with derived binaries.

Use Go 1.27+, Python 3 and Git from the repository:

```sh
python3 tools/managed-mieru/prepare.py --verify
python3 tools/managed-mieru/prepare.py --output /tmp/new-mieru-source
go test -race -count=1 ./internal/mieru/native
go test -race -count=1 ./internal/mieru
```

Preparation verifies the pinned module and file checksums, creates a new
directory and applies [managed-resources.patch](managed-resources.patch) after
`git apply --check`. Existing destinations are refused. Verification reproduces
all checked-in Go files and the license byte for byte; it does not edit the
module cache or the working sources. To update the dependency, review upstream
changes, refresh the manifest and patch, then rerun both package suites and the
actual official-client rate, billing, routing, failure and resource tests.
The repository lint configuration preserves formatting and existing lint
conventions only for the enumerated copied upstream files. The new authored
`managed_resources.go` and its tests keep the full repository lint rules;
compiler and `go vet` checks still cover every copied file.

The extension adds opt-in server limits. The adapter selects 256 active native
sessions in total and 128 per authentication generation across all listeners,
before allocating session queues. Each of four segment trees has a limit of
256 segments and 128 KiB of payload; the receive staging channel has at most
64 segments and 128 KiB. These are queue payload limits, not a process RSS cap.
An active read can retain one partial segment, and protocol input/output workers,
encryption, socket buffers and metadata have separate finite allocations.
Each underlay's ready queue and the mux accept queue hold at most 64 references.
Closed queued sessions retain no payload after worker cleanup. TCP underlays
remain separately capped at 256 by the adapter.

TCP receive queues apply backpressure. UDP overload discards unacknowledged
native segments for retransmission without blocking the shared UDP socket;
ordered delivery and read-triggered window updates preserve application payload.
Session startup is serialized with underlay shutdown. Finished workers clear
owned buffers and remove session metadata before releasing the admission slot.
Closing the mux waits for native session and accept workers.

Managed mode disables native per-generation diagnostic time series, which the
official registry cannot remove. Users carrying native rolling quotas cannot
obtain managed session slots in that mode. The panel's fixed-point policy ledger
remains the billing/quota owner;
aggregate native transport diagnostics remain available. `ServerResourceStats`
reports active leases, their peak/rejections and payload retained in the five
bounded queues. It does not count application buffers, encryption or kernel
memory. Default unconfigured native behavior remains available to upstream tests.
