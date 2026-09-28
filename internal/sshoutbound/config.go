package sshoutbound

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/util/netsafe"
)

var (
	ErrConfig          = errors.New("invalid SSH upstream configuration")
	ErrHostKeyMismatch = errors.New("SSH upstream host key mismatch")
	ErrNetwork         = errors.New("SSH upstream supports TCP only")
	ErrTarget          = errors.New("invalid SSH upstream target")
	ErrCapacity        = errors.New("SSH upstream connection capacity reached")
	ErrClosed          = errors.New("SSH upstream connector closed")
)

type Config struct {
	Address              string `json:"address"`
	Port                 int    `json:"port"`
	User                 string `json:"user"`
	PrivateKey           string `json:"privateKey"`
	PrivateKeyPassphrase string `json:"privateKeyPassphrase,omitempty"`
	HostKey              string `json:"hostKey"`
}

func compileConfig(config Config) (string, *ssh.ClientConfig, error) {
	host, err := netsafe.NormalizeHost(config.Address)
	if err != nil || config.Port < 1 || config.Port > 65535 {
		return "", nil, fmt.Errorf("%w: address and port are required", ErrConfig)
	}
	if config.User == "" || len(config.User) > 255 || strings.TrimSpace(config.User) != config.User || strings.ContainsFunc(config.User, unicode.IsControl) {
		return "", nil, fmt.Errorf("%w: invalid account name", ErrConfig)
	}
	if len(config.PrivateKey) > 64<<10 || len(config.PrivateKeyPassphrase) > 4096 || len(config.HostKey) > 16<<10 {
		return "", nil, fmt.Errorf("%w: key material exceeds size limit", ErrConfig)
	}
	var signer ssh.Signer
	if config.PrivateKeyPassphrase == "" {
		signer, err = ssh.ParsePrivateKey([]byte(config.PrivateKey))
	} else {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(config.PrivateKey), []byte(config.PrivateKeyPassphrase))
	}
	if err != nil {
		return "", nil, fmt.Errorf("%w: unreadable private key or passphrase", ErrConfig)
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(config.HostKey))
	if err != nil || len(options) > 0 || strings.TrimSpace(string(rest)) != "" {
		return "", nil, fmt.Errorf("%w: exactly one server public key is required", ErrConfig)
	}
	if _, certificate := key.(*ssh.Certificate); certificate {
		return "", nil, fmt.Errorf("%w: pin a server public key, not a host certificate", ErrConfig)
	}
	algorithms := []string{key.Type()}
	if key.Type() == ssh.KeyAlgoRSA {
		algorithms = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	} else if !slices.Contains(ssh.SupportedAlgorithms().HostKeys, key.Type()) {
		return "", nil, fmt.Errorf("%w: unsupported server key algorithm", ErrConfig)
	}
	checkHost := ssh.FixedHostKey(key)
	client := &ssh.ClientConfig{
		User: config.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		ClientVersion:     "SSH-2.0-3x-ui-upstream",
		HostKeyAlgorithms: algorithms,
		HostKeyCallback: func(hostname string, remote net.Addr, presented ssh.PublicKey) error {
			if err := checkHost(hostname, remote, presented); err != nil {
				return ErrHostKeyMismatch
			}
			return nil
		},
	}
	return net.JoinHostPort(host, strconv.Itoa(config.Port)), client, nil
}

func validTarget(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != strings.TrimSpace(host) {
		return false
	}
	if _, err := netsafe.NormalizeHost(strings.TrimSuffix(host, ".")); err != nil {
		return false
	}
	n, err := strconv.ParseUint(port, 10, 16)
	return err == nil && n > 0
}
