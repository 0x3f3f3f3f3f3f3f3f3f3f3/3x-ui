package service

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func sshHostRuntimeFixture(t *testing.T) (*XrayService, *model.Inbound, model.NativeSSHHostKey, string) {
	t.Helper()
	svc, _, _, _ := setupManagedActivationService(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	inbound, _, err := (&InboundService{}).AddInbound(&model.Inbound{Tag: "ssh-host-runtime", Protocol: model.SSH, Listen: "127.0.0.1", Port: port, Settings: `{"clients":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	var key model.NativeSSHHostKey
	if err := database.GetDB().First(&key, "id = ?", inbound.SSHHostKeyID).Error; err != nil {
		t.Fatal(err)
	}
	path, err := sshManagedHostKeyPath(key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(inbound).UpdateColumn("enable", true).Error; err != nil {
		t.Fatal(err)
	}
	return svc, inbound, key, path
}

func TestSSHPanelPrecedingCoreRefusesBeforePrivateKeyPreparation(t *testing.T) {
	preceding := os.Getenv("XRAY_PRE_SSH_MARKER_E2E_BINARY")
	if preceding == "" {
		t.Skip("set XRAY_PRE_SSH_MARKER_E2E_BINARY to immutable preceding native checkpoint")
	}
	t.Setenv("XRAY_E2E_BINARY", preceding)
	svc, _, _, path := sshHostRuntimeFixture(t)
	err := svc.RestartXray(true)
	if !errors.Is(err, xray.ErrClientPolicyCapability) || !strings.Contains(err.Error(), "trusted-ssh-client-id-v1") {
		t.Fatalf("preceding native core crossed SSH capability fence: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("missing SSH capability still prepared private host material")
	}
	if process := currentXrayProcess(); process != nil && (process.IsRunning() || process.IsControlReady()) {
		t.Fatal("rejected SSH activation retained an active child")
	}
}

func TestSSHPanelRestoredDatabaseRecreatesOriginalHostTrust(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	svc, inbound, key, path := sshHostRuntimeFixture(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "restored-panel.db")
	if err := database.BackupSQLite(backup); err != nil {
		t.Fatal(err)
	}
	if err := currentXrayProcess().Stop(); err != nil {
		t.Fatal(err)
	}
	StopTrafficWriter()
	if err := database.CloseDB(); err != nil {
		t.Fatal(err)
	}
	if err := database.InitDB(backup); err != nil {
		t.Fatal(err)
	}
	StartTrafficWriter()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != key.PrivateKeyPEM {
		t.Fatal("restored panel rotated the SSH business host key")
	}
	observed := ""
	client, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", inbound.Port), &ssh.ClientConfig{User: "no-account", Timeout: time.Second, HostKeyCallback: func(_ string, _ net.Addr, public ssh.PublicKey) error {
		observed = ssh.FingerprintSHA256(public)
		return nil
	}})
	if client != nil {
		_ = client.Close()
	}
	if err == nil || observed != key.Fingerprint {
		t.Fatal("restored database changed real SSH host trust")
	}
}

func TestSSHPanelManagedStartupMaterializesAndRecoversSameHostTrust(t *testing.T) {
	svc, inbound, key, path := sshHostRuntimeFixture(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatalf("native SSH managed startup: %v", err)
	}
	check := func() {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil || string(data) != key.PrivateKeyPEM {
			t.Fatal("SSH runtime does not use its exact database-owned business key")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatal("SSH managed key file is not a private regular file")
		}
		for _, dir := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
			info, err := os.Lstat(dir)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Fatal("SSH key directory is not private")
			}
		}
		observed := ""
		client, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", inbound.Port), &ssh.ClientConfig{User: "no-account", Timeout: time.Second, HostKeyCallback: func(_ string, _ net.Addr, public ssh.PublicKey) error {
			observed = ssh.FingerprintSHA256(public)
			return nil
		}})
		if client != nil {
			_ = client.Close()
		}
		if err == nil || observed != key.Fingerprint {
			t.Fatal("real native SSH handshake did not present the persisted public fingerprint")
		}
	}
	check()
	if err := currentXrayProcess().Stop(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestSSHPanelManagedStartupRejectsUnsafeBusinessKeyPaths(t *testing.T) {
	for _, mode := range []string{"symlink-directory", "permissive-directory", "symlink-file", "permissive-file", "conflicting-file"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, key, path := sshHostRuntimeFixture(t)
			root := filepath.Join(config.GetDBFolderPath(), "native-ssh")
			hosts := filepath.Dir(path)
			if mode == "symlink-directory" {
				if err := os.Symlink(t.TempDir(), root); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(hosts, 0o700); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "permissive-directory":
					if err := os.Chmod(hosts, 0o755); err != nil {
						t.Fatal(err)
					}
				case "symlink-file":
					ownTemporaryFile := filepath.Join(t.TempDir(), "test-business.pem")
					if err := os.WriteFile(ownTemporaryFile, []byte(key.PrivateKeyPEM), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(ownTemporaryFile, path); err != nil {
						t.Fatal(err)
					}
				case "permissive-file":
					if err := os.WriteFile(path, []byte(key.PrivateKeyPEM), 0o644); err != nil {
						t.Fatal(err)
					}
				case "conflicting-file":
					if err := os.WriteFile(path, []byte("test-owned conflicting business material"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := svc.RestartXray(true); err == nil {
				t.Fatal("SSH activation accepted an unsafe/conflicting business key path")
			}
			if process := currentXrayProcess(); process != nil && process.IsControlReady() {
				t.Fatal("failed SSH private-path preparation retained ready business service")
			}
		})
	}
}
