package core_test

import (
	"strings"
	"testing"

	"github.com/xtls/xray-core/core"
)

func TestCustomBinaryIdentifiesDistributionWithoutBreakingPanelVersionParser(t *testing.T) {
	line := core.VersionStatement()[0]
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "Xray" || fields[1] != "26.9.9" {
		t.Fatalf("panel cannot parse core version: %q", line)
	}
	if !strings.Contains(line, "Custom Xray-core 26.9.9-custom.1") {
		t.Fatalf("custom binary is indistinguishable from upstream: %q", line)
	}
}
