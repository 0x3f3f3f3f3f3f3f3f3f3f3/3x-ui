package ssh

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/xtls/xray-core/common/protocol"
	gossh "golang.org/x/crypto/ssh"
	"google.golang.org/protobuf/proto"
)

var (
	ErrConfiguration  = errors.New("invalid SSH configuration")
	ErrAuthentication = errors.New("SSH authentication denied")
	ErrResourceLimit  = errors.New("SSH resource limit exceeded")
)

func (a *Account) Equals(other protocol.Account) bool {
	value, ok := other.(*Account)
	return ok && a.Username == value.Username
}

func (a *Account) ToProto() proto.Message               { return a }
func (a *Account) AsAccount() (protocol.Account, error) { return a, nil }

type limits struct {
	handshake, idle, channelOpen                                     time.Duration
	authTries, connections, userConnections, channels, totalChannels int
}

func bounded(value, fallback, ceiling uint32) (int, error) {
	if value == 0 {
		value = fallback
	}
	if value > ceiling {
		return 0, ErrConfiguration
	}
	return int(value), nil
}

func serverLimits(c *ServerConfig) (limits, error) {
	var l limits
	values := []struct {
		value, fallback, ceiling uint32
		target                   *int
	}{
		{c.MaxAuthTries, 6, 16, &l.authTries},
		{c.MaxConnections, 64, 1024, &l.connections},
		{c.MaxConnectionsPerUser, 4, 64, &l.userConnections},
		{c.MaxChannelsPerConnection, 16, 64, &l.channels},
		{c.MaxChannels, 128, 512, &l.totalChannels},
	}
	for _, v := range values {
		n, err := bounded(v.value, v.fallback, v.ceiling)
		if err != nil {
			return l, err
		}
		*v.target = n
	}
	for _, v := range []struct {
		value, fallback, ceiling uint32
		target                   *time.Duration
	}{
		{c.HandshakeTimeoutSeconds, 10, 120, &l.handshake},
		{c.IdleTimeoutSeconds, 300, 86400, &l.idle},
		{c.ChannelOpenTimeoutSeconds, 10, 120, &l.channelOpen},
	} {
		n, err := bounded(v.value, v.fallback, v.ceiling)
		if err != nil {
			return l, err
		}
		*v.target = time.Duration(n) * time.Second
	}
	return l, nil
}

func privateKeyFile(path string) (gossh.Signer, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrConfiguration
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.Join(ErrConfiguration, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 || runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, ErrConfiguration
	}
	data := make([]byte, info.Size())
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, ErrConfiguration
	}
	signer, err := gossh.ParsePrivateKey(data)
	if err != nil {
		return nil, ErrConfiguration
	}
	return signer, nil
}
