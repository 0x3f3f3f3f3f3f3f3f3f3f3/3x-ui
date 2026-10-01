package mieru

import (
	"encoding/binary"
	"net"
	"strconv"
	"time"

	"github.com/enfein/mieru/v3/pkg/cipher"
	miProtocol "github.com/enfein/mieru/v3/pkg/protocol"
	"github.com/enfein/mieru/v3/pkg/protocol/serveruser"
	"github.com/xtls/xray-core/common/protocol"
)

type authenticatedPacketConn struct {
	net.PacketConn
	server *Server
}

type packetBinding struct {
	user  *protocol.MemoryUser
	timer *time.Timer
}

// The library first tries retained session ciphers even for new UDP sessions.
// Validate against the current registry before delivery to prevent a retired
// password from creating a new canonical binding through that fast path.
func (c *authenticatedPacketConn) ReadFrom(payload []byte) (int, net.Addr, error) {
	const headerSize = cipher.DefaultNonceSize + miProtocol.MetadataLength + cipher.DefaultOverhead
	for {
		n, peer, err := c.PacketConn.ReadFrom(payload)
		if err != nil {
			return n, peer, err
		}
		if n < headerSize {
			continue
		}
		c.server.authMu.Lock()
		_, metadata, authentication, err := c.server.packetUsers.Discover(payload[:headerSize], serveruser.SourceFromAddr(peer), true)
		if err != nil || len(metadata) != miProtocol.MetadataLength {
			c.server.authMu.Unlock()
			continue
		}
		// Layout is the pinned library's sessionStruct/dataAckStruct shared header:
		// byte 0 openSessionRequest=2; bytes 6..9 are the authenticated session ID.
		if metadata[0] == 2 {
			key := packetBindingKey(peer.String(), strconv.FormatUint(uint64(binary.BigEndian.Uint32(metadata[6:10])), 10))
			var user *protocol.MemoryUser
			for _, candidate := range c.server.users.GetUsers() {
				if candidate.Account.(*Account).Username == authentication.Policy().Name() {
					user = candidate
					break
				}
			}
			previous := c.server.packetBindings[key]
			limit := int(c.server.config.MaxConnections)
			if limit == 0 {
				limit = 1024
			}
			if user == nil || previous != nil && previous.user != user || previous == nil && len(c.server.packetBindings) >= limit {
				c.server.authMu.Unlock()
				continue
			}
			if previous == nil {
				binding := &packetBinding{user: user}
				timeout := c.server.config.HandshakeTimeoutSeconds
				if timeout == 0 {
					timeout = 10
				}
				binding.timer = time.AfterFunc(time.Duration(timeout)*time.Second, func() {
					c.server.authMu.Lock()
					defer c.server.authMu.Unlock()
					if c.server.packetBindings[key] == binding {
						delete(c.server.packetBindings, key)
					}
				})
				c.server.packetBindings[key] = binding
			}
		}
		c.server.authMu.Unlock()
		// Cache publication remains the library's full authenticated-payload task.
		return n, peer, nil
	}
}

func packetBindingKey(peer, id string) string { return peer + "/" + id }
func packetConnectionKey(conn net.Conn) string {
	value, ok := conn.(*miProtocol.Session)
	if !ok {
		return ""
	}
	return packetBindingKey(conn.RemoteAddr().String(), value.ToSessionInfo().GetId())
}

func (s *Server) packetUser(conn net.Conn) *protocol.MemoryUser {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	binding := s.packetBindings[packetConnectionKey(conn)]
	if binding == nil {
		return nil
	}
	binding.timer.Stop()
	return binding.user
}

func (s *Server) releasePacketUser(conn net.Conn, user *protocol.MemoryUser) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	key := packetConnectionKey(conn)
	binding := s.packetBindings[key]
	if binding != nil && (user == nil || binding.user == user) {
		binding.timer.Stop()
		delete(s.packetBindings, key)
	}
}
