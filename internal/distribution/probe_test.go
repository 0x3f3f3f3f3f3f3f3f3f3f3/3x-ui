package distribution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These scripts exercise the real subprocess boundary. Final package acceptance
// separately uses the compiled panel and core; scripted reports are not that proof.
func TestProbeBoundsActualSubprocessAndStrictlyDecodesReport(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("distribution subprocess tests require Linux")
	}
	report := BinaryReport{FormatVersion: 1, APIVersion: 1, Compatibility: "traffic-control-v1", SourceRevision: strings.Repeat("a", 40), Target: Target{OS: runtime.GOOS, Arch: runtime.GOARCH}, GoVersion: "go1.27.1", Capabilities: RequiredCapabilities()}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "unknown-field", "trailing", "duplicate", "exit", "oversize", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			script := "#!/bin/sh\nprintf '%s' '" + string(body) + "'\n"
			switch mode {
			case "unknown-field":
				script = "#!/bin/sh\nprintf '%s' '{\"unexpected\":true," + string(body[1:]) + "'\n"
			case "trailing":
				script += "printf '%s' '{}'\n"
			case "duplicate":
				script = "#!/bin/sh\nprintf '%s' '{\"formatVersion\":9," + string(body[1:]) + "'\n"
			case "exit":
				script += "exit 7\n"
			case "oversize":
				script = "#!/bin/sh\nwhile :; do printf '%01000d' 0; done\n"
			case "timeout":
				script = "#!/bin/sh\nwhile :; do :; done\n"
			}
			executable := filepath.Join(t.TempDir(), "candidate")
			if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			started := time.Now()
			got, err := probe(ctx, executable, "capabilities")
			if time.Since(started) > 3*time.Second {
				t.Fatal("probe did not bound child execution")
			}
			if mode == "valid" {
				if err != nil || got.SourceRevision != report.SourceRevision {
					t.Fatalf("valid probe: %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("accepted %s probe", mode)
			}
		})
	}
}

func TestValidateReportRejectsSourceTargetAndFeatureMismatch(t *testing.T) {
	_, manifest := manifestFixture(t)
	for _, mode := range []string{"valid", "format", "api", "compatibility", "source", "dirty", "target", "arm", "missing-native", "duplicate-capability", "instance-claim", "checkpoint-claim", "seed-claim", "expiry-claim"} {
		t.Run(mode, func(t *testing.T) {
			r := BinaryReport{FormatVersion: 1, APIVersion: 1, Compatibility: manifest.Compatibility, SourceRevision: manifest.SourceRevision, Target: manifest.Target, GoVersion: "go1.27.1", Capabilities: RequiredCapabilities()}
			switch mode {
			case "format":
				r.FormatVersion++
			case "api":
				r.APIVersion++
			case "compatibility":
				r.Compatibility = "official-xray"
			case "source":
				r.SourceRevision = strings.Repeat("b", 40)
			case "dirty":
				r.SourceRevision += "-dirty"
			case "target":
				r.Target.Arch = "wrong"
			case "arm":
				r.Target.ARM = "7"
			case "missing-native":
				r.Capabilities = []string{"fixed-point-billing-v1"}
			case "duplicate-capability":
				r.Capabilities = append(r.Capabilities, r.Capabilities[0])
			case "checkpoint-claim":
				r.Capabilities = append(r.Capabilities, "committed-cumulative-ledger-v1")
			case "seed-claim":
				r.Capabilities = append(r.Capabilities, "create-only-usage-seed-v1")
			case "expiry-claim":
				r.Capabilities = append(r.Capabilities, "durable-first-use-expiry-v1")
			case "instance-claim":
				r.Capabilities = append(r.Capabilities, "local-durable-reservations-v1")
			}
			err := validateReport(&manifest, r, true)
			if mode == "valid" && err != nil {
				t.Fatal(err)
			}
			if mode != "valid" && err == nil {
				t.Fatalf("accepted %s report", mode)
			}
		})
	}
}
