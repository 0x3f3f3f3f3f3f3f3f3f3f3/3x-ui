package snell

import (
	"context"
	"fmt"
	"testing"

	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
)

func TestNativeSnellV5QUICNetworkDistinctFromV4AndV6(t *testing.T) {
	for _, version := range []uint32{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			config := &ServerConfig{Version: version, User: &protocol.User{ClientId: "d395b8b1-31ab-47ea-9a67-e46dcc84cd96", Email: "owner", Account: serial.ToTypedMessage(&Account{Psk: "native-quic-test-secret"})}}
			in, err := NewServer(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			if !X.HasNetwork(in.Network(), X.Network_TCP) {
				t.Fatal("native TCP path missing")
			}
			if has := X.HasNetwork(in.Network(), X.Network_UDP); has != (version == 5) {
				t.Fatalf("v%d native QUIC UDP capability=%v want=%v", version, has, version == 5)
			}
		})
	}
}

func TestNativeSnellV5ExplicitQUICConfig(t *testing.T) {
	c := &ClientConfig{Version: 5, Psk: "native-quic-test-secret", Address: X.NewIPOrDomain(X.LocalHostIP), Port: 7177, Quic: true}
	if err := ValidateClient(c); err != nil {
		t.Fatalf("native v5 QUIC config unavailable: %v", err)
	}
}
