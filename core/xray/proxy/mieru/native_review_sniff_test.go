package mieru_test

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestReviewNativeInboundHonorsSniffing(t *testing.T) {
	port := reservePort(t, "TCP")
	nativeCoreConfigured(t, port, "TCP", func(config map[string]any) {
		config["inbounds"].([]any)[0].(map[string]any)["sniffing"] = map[string]any{"enabled": true, "destOverride": []string{"http"}, "routeOnly": true}
		config["outbounds"] = append(config["outbounds"].([]any), map[string]any{"protocol": "blackhole", "tag": "deny"})
		config["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "domain": []string{"full:denied.example.org"}, "outboundTag": "deny"}}}
	})
	client := referenceClient(t, port, "TCP")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, echoTarget(t, "tcp"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := []byte("GET / HTTP/1.1\r\nHost: denied.example.org\r\n\r\n")
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err = conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, response); err == nil {
		t.Fatalf("configured HTTP sniffing/domain deny rule was ignored; original target echoed forbidden request %q", response)
	}
}
