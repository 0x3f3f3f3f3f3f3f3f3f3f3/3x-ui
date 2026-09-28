package commander_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	policycommand "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/app/commander"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
)

func TestPrivateControlRejectsWorldAccessibleDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := commander.ValidatePrivateUnixSocket(filepath.Join(dir, "policy.sock")); err == nil {
		t.Fatal("accepted world-accessible directory")
	}
}

func TestProtobufConfigCannotBypassPrivateControlTransport(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:0", "0.0.0.0:0", "@abstract-policy", ""} {
		config := &commander.Config{Tag: "private", Listen: listen, Service: []*serial.TypedMessage{serial.ToTypedMessage(&policycommand.Config{})}}
		instance, err := core.New(&core.Config{App: []*serial.TypedMessage{serial.ToTypedMessage(config)}})
		if err == nil {
			instance.Close()
			t.Fatal("accepted unprotected protobuf API")
		}
		if !strings.Contains(err.Error(), "private Unix socket") {
			t.Fatalf("transport validation bypassed: %v", err)
		}
	}
}
