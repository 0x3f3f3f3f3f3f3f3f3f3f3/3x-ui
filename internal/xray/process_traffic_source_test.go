package xray

import (
	"reflect"
	"strings"
	"testing"
)

func TestTrafficBatchCapturesSourceAccountingMode(t *testing.T) {
	p := NewProcess(&Config{})
	read := func(batch TrafficBatch, field string) string {
		t.Helper()
		value := reflect.ValueOf(batch).FieldByName(field)
		if !value.IsValid() || value.Kind() != reflect.String {
			t.Fatalf("pending batch has no immutable %s provenance", field)
		}
		return value.String()
	}
	legacy, err := p.trafficBatchFromCounters(map[string]int64{"user>>>alice>>>traffic>>>uplink": 7}, false)
	if err != nil {
		t.Fatal(err)
	}
	if read(legacy.batch, "SourceMode") != "legacy" || read(legacy.batch, "SourceInstanceID") != "" {
		t.Fatal("legacy source mislabeled")
	}
	p.SetConfig(&Config{ClientPolicy: []byte(`{"instanceId":"managed-instance-a"}`)})
	managed, err := p.trafficBatchFromCounters(map[string]int64{"user>>>alice>>>traffic>>>uplink": 11}, false)
	if err != nil {
		t.Fatal(err)
	}
	p.SetConfig(&Config{ClientPolicy: []byte(`{"instanceId":"managed-instance-b"}`)})
	if read(managed.batch, "SourceMode") != "managed" || read(managed.batch, "SourceInstanceID") != "managed-instance-a" || read(legacy.batch, "SourceMode") != "legacy" {
		t.Fatal("later config replayed over pending source provenance")
	}
}

func TestTrafficBatchRejectsMalformedSourceAccounting(t *testing.T) {
	for _, raw := range []string{`{`, `{}`, `{"instanceId":""}`, `{"instanceId":"` + strings.Repeat("a", 37) + `"}`, "{\"instanceId\":\"" + string([]byte{0xff}) + "\"}"} {
		p := NewProcess(&Config{ClientPolicy: []byte(raw)})
		if _, err := p.trafficBatchFromCounters(map[string]int64{"user>>>alice>>>traffic>>>uplink": 7}, false); err == nil {
			t.Fatalf("malformed declared managed source accepted: %s", raw)
		}
	}
}

func TestTrafficBatchAcceptsWhitespaceNullLegacySource(t *testing.T) {
	p := NewProcess(&Config{ClientPolicy: []byte(" \n null \t ")})
	pending, err := p.trafficBatchFromCounters(map[string]int64{"user>>>alice>>>traffic>>>uplink": 7}, false)
	if err != nil || pending.batch.SourceMode != "legacy" || pending.batch.SourceInstanceID != "" {
		t.Fatalf("valid absent managed policy did not capture legacy provenance: %+v/%v", pending, err)
	}
}

func TestTrafficBatchPreservesExactNativeLabels(t *testing.T) {
	for _, final := range []bool{false, true} {
		for _, label := range []string{"legacy\nusername", "用户\n>>>traffic>>>downlink", "user\r\nname"} {
			p := NewProcess(&Config{})
			values := map[string]int64{
				"user>>>" + label + ">>>traffic>>>uplink":       7,
				"user>>>" + label + ">>>traffic>>>downlink":     11,
				"inbound>>>" + label + ">>>traffic>>>uplink":    13,
				"outbound>>>" + label + ">>>traffic>>>downlink": 17,
			}
			pending, err := p.trafficBatchFromCounters(values, final)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending.batch.ClientTraffics) != 1 || pending.batch.ClientTraffics[0].Email != label || pending.batch.ClientTraffics[0].Up != 7 || pending.batch.ClientTraffics[0].Down != 11 {
				t.Fatalf("final=%t omitted valid exact label %q while advancing cursor: %+v", final, label, pending.batch.ClientTraffics)
			}
			if len(pending.batch.Traffics) != 2 {
				t.Fatalf("final=%t omitted multiline resource counters: %+v", final, pending.batch.Traffics)
			}
			for _, row := range pending.batch.Traffics {
				if row.Tag != label || row.IsInbound && row.Up != 13 || row.IsOutbound && row.Down != 17 {
					t.Fatalf("multiline resource label/direction changed: %+v", row)
				}
			}
		}
	}
}
