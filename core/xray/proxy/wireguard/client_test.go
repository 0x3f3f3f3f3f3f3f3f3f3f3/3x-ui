package wireguard_test

import (
	"strings"
	"testing"

	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/distro/all"
	"github.com/xtls/xray-core/proxy/wireguard"
)

func TestWireGuardWithoutSenderSettingsReturnsValidationErrorInsteadOfPanic(t *testing.T) {
	config := &core.Config{
		App:      []*serial.TypedMessage{serial.ToTypedMessage(&dispatcher.Config{}), serial.ToTypedMessage(&proxyman.InboundConfig{}), serial.ToTypedMessage(&proxyman.OutboundConfig{})},
		Outbound: []*core.OutboundHandlerConfig{{ProxySettings: serial.ToTypedMessage(&wireguard.DeviceConfig{IsClient: true})}},
	}
	instance, err := core.New(config)
	if err == nil {
		instance.Close()
		t.Fatal("accepted empty WireGuard peers")
	}
	if !strings.Contains(err.Error(), "empty peers") {
		t.Fatalf("unexpected error: %v", err)
	}
}
