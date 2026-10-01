package xray

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func sshOutboundTestMaterial(t *testing.T) (string, []byte) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "ephemeral test-owned business key")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), pem.EncodeToMemory(block)
}

func sshOutboundTestJSON(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSSHOutboundRejectsUnpinnedAndUnsupportedOptions(t *testing.T) {
	pin, _ := sshOutboundTestMaterial(t)
	for _, name := range []string{"valid", "missing-pin", "invalid-pin", "udp-wrapper", "tls", "global-mux", "unknown-native-option", "outside-business-directory"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XUI_DB_FOLDER", t.TempDir())
			settings := map[string]any{"address": "127.0.0.1", "port": 2222, "username": "business", "password": "secret", "hostKey": pin}
			outbound := map[string]any{"tag": "native-ssh", "protocol": "ssh", "settings": settings}
			switch name {
			case "missing-pin":
				delete(settings, "hostKey")
			case "invalid-pin":
				settings["hostKey"] = "not a host key"
			case "udp-wrapper":
				outbound["streamSettings"] = map[string]any{"network": "kcp"}
			case "tls":
				outbound["streamSettings"] = map[string]any{"network": "tcp", "security": "tls"}
			case "global-mux":
				outbound["mux"] = map[string]any{"enabled": true}
			case "unknown-native-option":
				settings["insecureSkipHostKeyVerification"] = true
			case "outside-business-directory":
				settings["privateKeyFile"] = filepath.Join(t.TempDir(), "test-owned-outside.pem")
			}
			err := ValidateOutboundConfig(sshOutboundTestJSON(t, outbound))
			if name == "valid" {
				if err != nil {
					t.Fatalf("strict password SSH outbound: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid/unsupported native SSH outbound accepted")
			}
		})
	}
}

func TestSSHOutboundBusinessPrivateKeyConfinement(t *testing.T) {
	for _, mode := range []string{"valid", "permissive-file", "permissive-directory", "symlink-file", "invalid-key"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XUI_DB_FOLDER", dir)
			business := filepath.Join(dir, "native-ssh", "outbound")
			if err := os.MkdirAll(business, 0o700); err != nil {
				t.Fatal(err)
			}
			pin, private := sshOutboundTestMaterial(t)
			path := filepath.Join(business, "test-business.pem")
			if mode == "symlink-file" {
				outside := filepath.Join(t.TempDir(), "test-owned.pem")
				if err := os.WriteFile(outside, private, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			} else {
				if mode == "invalid-key" {
					private = []byte("test-owned malformed private key")
				}
				if err := os.WriteFile(path, private, 0o600); err != nil {
					t.Fatal(err)
				}
				if mode == "permissive-file" {
					if err := os.Chmod(path, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "permissive-directory" {
					if err := os.Chmod(business, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			outbound := map[string]any{"tag": "native-ssh", "protocol": "ssh", "settings": map[string]any{"address": "127.0.0.1", "port": 2222, "username": "business", "hostKey": pin, "privateKeyFile": path}}
			err := ValidateOutboundConfig(sshOutboundTestJSON(t, outbound))
			if mode == "valid" {
				if err != nil {
					t.Fatalf("private business SSH outbound: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe/malformed private business key accepted")
			}
		})
	}
}
