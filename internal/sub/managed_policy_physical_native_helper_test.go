package sub

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	miClient "github.com/enfein/mieru/v3/apis/client"
	apicommon "github.com/enfein/mieru/v3/apis/common"
	pb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func newManagedPhysicalNativeCase(t *testing.T) (*managedPhysicalCase, string) {
	t.Helper()
	publicKey, privateKey := sshHTTPKey(t)
	t.Setenv("XUI_MANAGED_PHYSICAL_NATIVE", "1")
	t.Setenv("XUI_MANAGED_PHYSICAL_SSH_PUBLIC_KEY", publicKey)
	return newManagedPhysicalCase(t, model.ClientPolicyScopeGlobal, "1.5", 8192, 0, 0), privateKey
}

func managedPhysicalExportNative(t *testing.T, h *sshHTTPHarness, subID, dir string) map[string]string {
	t.Helper()
	paths := map[string]string{}
	for _, format := range []string{"snell-json", "mieru", "ssh", "ssh-known-hosts"} {
		req := httptest.NewRequest(http.MethodGet, "/sub/"+subID+"?format="+format, nil)
		req.Host = "127.0.0.1"
		response := httptest.NewRecorder()
		h.router.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("physical native subscription %s failed status=%d", format, response.Code)
		}
		path := filepath.Join(dir, "private-export-"+format)
		if err := os.WriteFile(path, response.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		paths[format] = path
	}
	return paths
}

func managedPhysicalNativeExport(t *testing.T, node *managedPhysicalNode, format string) []byte {
	t.Helper()
	path := node.manifest.NativeExports[format]
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("native export requires actual private0600 file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func managedPhysicalNativeFlow(t *testing.T, node *managedPhysicalNode, protocol, privateKey string) net.Conn {
	t.Helper()
	target := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: node.manifest.TargetPort}
	switch protocol {
	case "snell":
		return managedPhysicalSnellClient(t, node).tcp(t, target)
	case "mieru":
		config := &pb.ClientConfig{}
		if err := protojson.Unmarshal(managedPhysicalNativeExport(t, node, "mieru"), config); err != nil {
			t.Fatal("invalid actual mieru subscription", err)
		}
		if len(config.Profiles) != 1 {
			t.Fatal("actual mieru subscription must select one profile")
		}
		client := miClient.NewClient()
		if err := client.Store(&miClient.ClientConfig{Profile: config.Profiles[0], Resolver: apicommon.HostMapResolver{Hosts: map[string]net.IP{"localhost": net.IPv4(127, 0, 0, 1)}}}); err != nil {
			t.Fatal(err)
		}
		if err := client.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Stop() })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		flow, err := client.DialContext(ctx, target)
		if err != nil {
			t.Fatal("actual managed mieru dial failed", err)
		}
		return flow
	case "ssh":
		return managedPhysicalOpenSSH(t, node, privateKey)
	default:
		t.Fatal("unsupported managed native fixture protocol")
		return nil
	}
}

func managedPhysicalSnellClient(t *testing.T, node *managedPhysicalNode) *snellHTTPClient {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal(managedPhysicalNativeExport(t, node, "snell-json"), &config); err != nil {
		t.Fatal("invalid actual Snell subscription", err)
	}
	port := sshHTTPPort(t)
	config["inbounds"].([]any)[0].(map[string]any)["port"] = port
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "private-snell-client.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "private-snell-client.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, os.Getenv("XRAY_E2E_BINARY"), "run", "-c", path)
	cmd.Stdout, cmd.Stderr = log, log
	client := &snellHTTPClient{port: port, done: make(chan struct{}), log: logPath, stop: cancel}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = log.Close()
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait(); _ = log.Close(); close(client.done) }()
	t.Cleanup(func() { cancel(); <-client.done })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-client.done:
			t.Fatal("actual downloaded Snell client exited; private log retained")
		default:
		}
		flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err == nil {
			_ = flow.Close()
			return client
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("actual downloaded Snell client did not listen")
	return nil
}

func managedPhysicalOpenSSH(t *testing.T, node *managedPhysicalNode, privateKey string) net.Conn {
	t.Helper()
	config := managedPhysicalNativeExport(t, node, "ssh")
	_ = managedPhysicalNativeExport(t, node, "ssh-known-hosts")
	alias := ""
	for _, line := range strings.Split(string(config), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "Host" {
			alias = fields[1]
			break
		}
	}
	if alias == "" {
		t.Fatal("actual OpenSSH subscription lacks host alias")
	}
	port := sshHTTPPort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, "ssh", "-F", node.manifest.NativeExports["ssh"], "-o", "BatchMode=yes", "-o", "UserKnownHostsFile="+node.manifest.NativeExports["ssh-known-hosts"], "-i", privateKey, "-N", "-L", fmt.Sprintf("%d:127.0.0.1:%d", port, node.manifest.TargetPort), alias)
	process := &sshHTTPProcess{done: make(chan struct{})}
	cmd.Stderr = &process.output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() { process.err = cmd.Wait(); close(process.done) }()
	t.Cleanup(func() { cancel(); <-process.done })
	return sshHTTPFlow(t, port, process)
}
