package sshoutbound

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseOutboundsRejectsUnsupportedAndAmbiguousConfiguration(t *testing.T) {
	private, public := testKey(t)
	settings := Config{Address: "upstream.invalid", Port: 22, User: "forward", PrivateKey: private, HostKey: public}
	valid := map[string]any{"tag": "exit", "protocol": "ssh", "settings": settings}
	encode := func(value any) json.RawMessage {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, test := range []struct {
		name   string
		change func(map[string]any)
		extra  json.RawMessage
	}{
		{name: "mux", change: func(m map[string]any) { m["mux"] = map[string]any{"enabled": true} }},
		{name: "transport", change: func(m map[string]any) { m["streamSettings"] = map[string]any{"security": "tls"} }},
		{name: "chain", change: func(m map[string]any) { m["proxySettings"] = map[string]any{"tag": "direct"} }},
		{name: "send-through", change: func(m map[string]any) { m["sendThrough"] = "127.0.0.2" }},
		{name: "unknown-settings", change: func(m map[string]any) {
			raw := map[string]any{}
			_ = json.Unmarshal(encode(settings), &raw)
			raw["password"] = "never-log-this-secret"
			m["settings"] = raw
		}},
		{name: "empty-tag", change: func(m map[string]any) { m["tag"] = "" }},
		{name: "control-tag", change: func(m map[string]any) { m["tag"] = "exit\nother" }},
		{name: "missing-pin", change: func(m map[string]any) { bad := settings; bad.HostKey = ""; m["settings"] = bad }},
		{name: "duplicate-ssh", extra: encode(valid)},
		{name: "duplicate-native", extra: json.RawMessage(`{"tag":"exit","protocol":"freedom"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := map[string]any{}
			for k, v := range valid {
				value[k] = v
			}
			if test.change != nil {
				test.change(value)
			}
			raws := []json.RawMessage{encode(value)}
			if test.extra != nil {
				raws = append(raws, test.extra)
			}
			got, err := ParseOutbounds(raws)
			if !errors.Is(err, ErrConfig) || got != nil {
				t.Fatalf("invalid outbound accepted: count=%d err=%v", len(got), err)
			}
			if strings.Contains(err.Error(), "never-log-this-secret") || strings.Contains(err.Error(), strings.Split(private, "\n")[1]) {
				t.Fatal("configuration validation leaked private material")
			}
		})
	}
	raws := []json.RawMessage{json.RawMessage(`{"tag":"direct","protocol":"freedom"}`), encode(valid)}
	got, err := ParseOutbounds(raws)
	if err != nil || len(got) != 1 || got[0].Tag != "exit" || got[0].Settings != settings {
		t.Fatalf("valid SSH/native config lost routing identity: count=%d err=%v", len(got), err)
	}
	for i := range MaxOutbounds {
		clone := map[string]any{"tag": fmt.Sprintf("extra-%d", i), "protocol": "ssh", "settings": settings}
		raws = append(raws, encode(clone))
	}
	got, err = ParseOutbounds(raws)
	if !errors.Is(err, ErrConfig) || got != nil {
		t.Fatalf("unbounded outbound count accepted: %d %v", len(got), err)
	}
}
