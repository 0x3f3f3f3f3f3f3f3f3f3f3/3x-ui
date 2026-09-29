package xray

import "testing"

func TestTrafficControlEndpointChangesRequireRestart(t *testing.T) {
	for _, next := range []string{"", `{"listen":"/private/new.sock"}`} {
		before, after := makeHotConfig(), makeHotConfig()
		before.TrafficControl = []byte(`{"listen":"/private/old.sock"}`)
		after.TrafficControl = []byte(next)
		if before.Equals(after) {
			t.Fatal("endpoint change ignored by equality")
		}
		if _, ok := ComputeHotDiff(before, after); ok {
			t.Fatal("control endpoint change was accepted without restart")
		}
		if _, ok := ComputeHotDiff(after, before); ok {
			t.Fatal("control endpoint addition was accepted without restart")
		}
	}
	before, after := makeHotConfig(), makeHotConfig()
	before.TrafficControl = []byte(`{"listen":"/private/old.sock"}`)
	after.TrafficControl = []byte("{\n\"listen\": \"/private/old.sock\"\n}")
	if diff, ok := ComputeHotDiff(before, after); !ok || !diff.Empty() {
		t.Fatal("format-only change restarted control")
	}
}
