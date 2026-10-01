package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var sshHostFilesMu sync.Mutex

// Called only from negotiated runtime preparation. Compiling a candidate never
// writes private material or opens a request-provided key path.
func prepareSSHManagedResources(caps *command.Capabilities, candidate *xray.Config) error {
	var listeners []xray.InboundConfig
	for _, inbound := range candidate.InboundConfigs {
		if inbound.Protocol == string(model.SSH) {
			listeners = append(listeners, inbound)
		}
	}
	if len(listeners) == 0 {
		return nil
	}
	if caps == nil || !slices.Contains(caps.Capabilities, "trusted-ssh-client-id-v1") {
		return fmt.Errorf("%w: missing trusted-ssh-client-id-v1", xray.ErrClientPolicyCapability)
	}
	sshHostFilesMu.Lock()
	defer sshHostFilesMu.Unlock()
	for _, listener := range listeners {
		var inbound model.Inbound
		if err := database.GetDB().Where("tag = ? AND protocol = ? AND node_id IS NULL AND enable = ?", listener.Tag, model.SSH, true).Take(&inbound).Error; err != nil {
			return ErrManagedConfigStale
		}
		var settings struct {
			HostKeyFile string `json:"hostKeyFile"`
		}
		if err := json.Unmarshal(listener.Settings, &settings); err != nil {
			return err
		}
		path, err := sshManagedHostKeyPath(inbound.SSHHostKeyID)
		if err != nil {
			return err
		}
		if path != settings.HostKeyFile {
			return ErrManagedConfigStale
		}
		key, err := loadSSHHostKey(database.GetDB(), inbound.SSHHostKeyID)
		if err != nil {
			return err
		}
		if err := materializeSSHHostKey(key); err != nil {
			return err
		}
	}
	return nil
}

func materializeSSHHostKey(key *model.NativeSSHHostKey) (resultErr error) {
	root, err := os.OpenRoot(config.GetDBFolderPath())
	if err != nil {
		return fmt.Errorf("SSH business key directory unavailable: %w", err)
	}
	defer root.Close()
	for _, name := range []string{"native-ssh", filepath.Join("native-ssh", "hosts")} {
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			if err := root.Mkdir(name, 0o700); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = root.Lstat(name)
		}
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			return fmt.Errorf("SSH business key directories must be real private 0700 directories")
		}
	}
	hosts, err := root.OpenRoot(filepath.Join("native-ssh", "hosts"))
	if err != nil {
		return err
	}
	defer hosts.Close()
	name := key.ID + ".pem"
	info, err := hosts.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 65536 {
			return fmt.Errorf("SSH business key must be a private 0600 regular file")
		}
		data, err := hosts.ReadFile(name)
		if err != nil {
			return err
		}
		if string(data) != key.PrivateKeyPEM {
			return fmt.Errorf("SSH business key file conflicts with its authoritative database key")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	temporary := "." + key.ID + "-" + uuid.NewString() + ".tmp"
	file, err := hosts.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, hosts.Remove(temporary)) }()
	if _, err := file.Write([]byte(key.PrivateKeyPEM)); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	// Link creates the final name atomically without replacing an existing file.
	if err := hosts.Link(temporary, name); err != nil {
		return err
	}
	directory, err := hosts.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func sshManagedHostKeyPath(id string) (string, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("invalid SSH managed host-key reference")
	}
	return filepath.Abs(filepath.Join(config.GetDBFolderPath(), "native-ssh", "hosts", id+".pem"))
}

func resolveSSHHostKey(tx *gorm.DB, inbound, previous *model.Inbound) error {
	if inbound.Protocol != model.SSH {
		inbound.SSHHostKeyID = ""
		return nil
	}
	if previous != nil && previous.Protocol == model.SSH {
		inbound.SSHHostKeyID = previous.SSHHostKeyID
		_, err := loadSSHHostKey(tx, inbound.SSHHostKeyID)
		return err
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return err
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return err
	}
	record := model.NativeSSHHostKey{
		ID:            uuid.NewString(),
		PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		PublicKey:     strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))),
		Fingerprint:   ssh.FingerprintSHA256(signer.PublicKey()),
	}
	if err := tx.Create(&record).Error; err != nil {
		return err
	}
	inbound.SSHHostKeyID = record.ID
	return nil
}

func loadSSHHostKey(tx *gorm.DB, id string) (*model.NativeSSHHostKey, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, fmt.Errorf("SSH service has an invalid managed host-key reference")
	}
	var record model.NativeSSHHostKey
	if err := tx.Where("id = ?", id).Take(&record).Error; err != nil {
		return nil, fmt.Errorf("SSH managed host key is missing: %w", err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(record.PrivateKeyPEM))
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("SSH managed host key is corrupt")
	}
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if record.PublicKey != publicKey || record.Fingerprint != ssh.FingerprintSHA256(signer.PublicKey()) {
		return nil, fmt.Errorf("SSH managed host trust metadata is corrupt")
	}
	return &record, nil
}
