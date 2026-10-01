package snell

import (
	"context"
	"testing"

	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/tcp"
)

func TestNativeRejectsWrappedTransports(t *testing.T) {
	server := &ServerConfig{Version: 6, User: &protocol.User{ClientId: "d395b8b1-31ab-47ea-9a67-e46dcc84cd96", Email: "owner", Account: serial.ToTypedMessage(&Account{Psk: "native-snell-test-secret"})}}
	client := &ClientConfig{Version: 6, Psk: "native-snell-test-secret", Address: X.NewIPOrDomain(X.LocalHostIP), Port: 1}
	for _, settings := range []*internet.MemoryStreamConfig{{ProtocolName: "websocket"}, {ProtocolName: "tcp", SecurityType: "tls"}, {ProtocolName: "tcp", ProtocolSettings: &tcp.Config{AcceptProxyProtocol: true}}, {ProtocolName: "tcp", ProtocolSettings: &tcp.Config{HeaderSettings: &serial.TypedMessage{}}}, {ProtocolName: "tcp", DownloadSettings: &internet.MemoryStreamConfig{}}} {
		ctx := session.ContextWithStreamSettings(context.Background(), settings)
		if in, err := NewServer(ctx, server); err == nil {
			in.Close()
			t.Fatalf("wrapped native inbound accepted: %+v", settings)
		}
		if out, err := NewClient(ctx, client); err == nil {
			out.Close()
			t.Fatalf("wrapped native outbound accepted: %+v", settings)
		}
	}
}

func TestNativeCanonicalUUID(t *testing.T) {
	u := &protocol.MemoryUser{ClientID: "D395B8B1-31AB-47EA-9A67-E46DCC84CD96", Email: "owner", Account: &MemoryAccount{PSK: "native-snell-test-secret"}}
	if err := validateUser(u); err == nil {
		t.Fatal("noncanonical UUID creates a second spelling of one managed owner")
	}
}

func TestNativeMalformedAddressRejectsWithoutPanic(t *testing.T) {
	for _, address := range []*X.IPOrDomain{{}, X.NewIPOrDomain(X.DomainAddress(""))} {
		c := &ClientConfig{Version: 4, Psk: "native-snell-test-secret", Address: address, Port: 1}
		if err := ValidateClient(c); err == nil {
			t.Fatalf("malformed typed outbound address accepted: %v", address)
		}
	}
}
