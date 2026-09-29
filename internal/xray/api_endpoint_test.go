package xray

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

func TestExplicitControlEndpointCannotFallBackToLegacyTCP(t *testing.T) {
	for _, endpoint := range []string{"192.0.2.1:10085", "0.0.0.0:10085", "localhost:10085", "127.0.0.1:0", "127.0.0.1:65536", "@abstract", "/missing/core.sock"} {
		t.Run(endpoint, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{"listen": endpoint})
			if err != nil {
				t.Fatal(err)
			}
			process := NewProcess(&Config{API: raw, InboundConfigs: []InboundConfig{{Tag: "api", Port: 10085}}})
			var api XrayAPI
			defer api.Close()
			if err := api.InitProcess(process); !errors.Is(err, ErrInvalidAPIEndpoint) {
				t.Fatalf("unsafe endpoint fell back to legacy TCP: %v", err)
			}
		})
	}
	process := NewProcess(&Config{API: json_util.RawMessage(`{"listen":42}`), InboundConfigs: []InboundConfig{{Tag: "api", Port: 10085}}})
	var api XrayAPI
	defer api.Close()
	if err := api.InitProcess(process); !errors.Is(err, ErrInvalidAPIEndpoint) {
		t.Fatalf("malformed API config fell back to legacy TCP: %v", err)
	}
}

func TestPrivateControlRejectsPublicSocketAndSymlink(t *testing.T) {
	dir, err := os.MkdirTemp("", "control-permissions-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "core.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, permissions := range []struct{ dir, socket os.FileMode }{{0755, 0600}, {0700, 0660}} {
		if err := os.Chmod(dir, permissions.dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(socket, permissions.socket); err != nil {
			t.Fatal(err)
		}
		var api XrayAPI
		if err := api.InitEndpoint(socket); !errors.Is(err, ErrInvalidAPIEndpoint) {
			api.Close()
			t.Fatalf("accepted unsafe socket permissions %o/%o: %v", permissions.dir, permissions.socket, err)
		}
	}
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.sock")
	if err := os.Symlink(socket, link); err != nil {
		t.Fatal(err)
	}
	var api XrayAPI
	defer api.Close()
	if err := api.InitEndpoint(link); !errors.Is(err, ErrInvalidAPIEndpoint) {
		t.Fatalf("accepted socket symlink: %v", err)
	}
}
