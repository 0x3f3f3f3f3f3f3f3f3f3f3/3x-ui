package routedbridge

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type ManagedBridge struct {
	*Bridge
}

func NewManaged(tag string, address netip.AddrPort, bindings []ClientBinding) (*ManagedBridge, error) {
	b, err := New(tag, address, bindings)
	if err != nil {
		return nil, err
	}
	return &ManagedBridge{Bridge: b}, nil
}

func (b *ManagedBridge) Apply(cfg *xray.Config) error {
	return b.apply(cfg, true)
}

// Check authenticates every private binding without providing an outbound target.
func (b *ManagedBridge) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, client := range b.clients {
		conn, err := b.connect(ctx, client, netip.MustParseAddrPort("127.0.0.1:1"), nil)
		if err != nil {
			return err
		}
		_ = conn.Close()
	}
	return nil
}

func (b *ManagedBridge) DialTCP(ctx context.Context, policyID string, source netip.AddrPort, host string, port uint16) (net.Conn, error) {
	conn, _, err := b.dial(ctx, policyID, source, host, port, 1)
	return conn, err
}

func (b *ManagedBridge) DialUDP(ctx context.Context, policyID string, source netip.AddrPort, host string, port uint16) (*DatagramConn, error) {
	conn, address, err := b.dial(ctx, policyID, source, host, port, 3)
	if err != nil {
		return nil, err
	}
	return &DatagramConn{Conn: conn, address: address, remote: datagramAddress(net.JoinHostPort(host, fmt.Sprint(port)))}, nil
}

func (b *ManagedBridge) dial(ctx context.Context, policyID string, source netip.AddrPort, host string, port uint16, command byte) (net.Conn, []byte, error) {
	client, ok := b.clients[policyID]
	if !ok {
		return nil, nil, ErrIdentity
	}
	if !source.IsValid() || source.Port() == 0 || source.Addr().Zone() != "" || port == 0 {
		return nil, nil, ErrDestination
	}
	address, err := socksAddress(host, port)
	if err != nil {
		return nil, nil, err
	}
	if dest, err := netip.ParseAddr(host); err == nil && dest.Unmap() == b.address.Addr().Unmap() && port == b.address.Port() {
		return nil, nil, ErrDestination
	}
	request := append([]byte{command}, address...)
	request = append(request, '\r', '\n')
	conn, err := b.connect(ctx, client, source, request)
	return conn, address, err
}

func (b *ManagedBridge) connect(ctx context.Context, client credential, source netip.AddrPort, request []byte) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", b.address.String())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	err = conn.SetDeadline(deadline)
	if err == nil {
		err = b.authenticate(conn, client, source)
	}
	if err == nil && request != nil {
		err = writeManagedFrame(conn, request)
	}
	if !stop() || ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		err = conn.SetDeadline(time.Time{})
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return conn, nil
}

func (b *ManagedBridge) authenticate(conn net.Conn, client credential, source netip.AddrPort) error {
	family, destination := "TCP4", "127.0.0.1"
	ip := source.Addr().Unmap()
	if ip.Is6() {
		family, destination = "TCP6", "::1"
	}
	header := fmt.Sprintf("PROXY %s %s %s %d %d\r\n", family, ip, destination, source.Port(), b.address.Port())
	if err := writeManagedFrame(conn, []byte(header)); err != nil {
		return err
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	hash := sha256.Sum224([]byte(client.password))
	probe := []byte(hex.EncodeToString(hash[:]))
	probe = append(probe, '\r', '\n', 0x7f, 0, 0, 0, 0, 0, 0, 0, '\r', '\n')
	probe = append(probe, nonce[:]...)
	if err := writeManagedFrame(conn, probe); err != nil {
		return err
	}
	var reply [36]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(client.password))
	_, _ = mac.Write([]byte("3x-ui-managed/1"))
	_, _ = mac.Write(nonce[:])
	if string(reply[:4]) != "XUI\x01" || !hmac.Equal(reply[4:], mac.Sum(nil)) {
		return ErrUnavailable
	}
	return nil
}

func writeManagedFrame(conn net.Conn, frame []byte) error {
	n, err := conn.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err != nil {
		_ = conn.Close()
	}
	return err
}

// DatagramConn carries whole UDP packets to one original destination over an authenticated core stream.
type DatagramConn struct {
	net.Conn
	address []byte
	remote  datagramAddress
	readMu  sync.Mutex
	writeMu sync.Mutex
}

func (c *DatagramConn) RemoteAddr() net.Addr { return c.remote }

func (c *DatagramConn) Write(payload []byte) (int, error) {
	if len(payload) > 65507 {
		return 0, ErrDestination
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	frame := make([]byte, 0, len(c.address)+4+len(payload))
	frame = append(frame, c.address...)
	frame = binary.BigEndian.AppendUint16(frame, uint16(len(payload)))
	frame = append(frame, '\r', '\n')
	frame = append(frame, payload...)
	if err := writeManagedFrame(c.Conn, frame); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func (c *DatagramConn) Read(payload []byte) (int, error) {
	n, _, err := c.ReadFrom(payload)
	return n, err
}

func (c *DatagramConn) ReadFrom(payload []byte) (int, net.Addr, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	n, peer, err := c.readPacket(payload)
	if err != nil {
		_ = c.Close()
	}
	return n, peer, err
}

func (c *DatagramConn) readPacket(payload []byte) (int, net.Addr, error) {
	var family [1]byte
	if _, err := io.ReadFull(c.Conn, family[:]); err != nil {
		return 0, nil, err
	}
	length := 4
	switch family[0] {
	case 1:
	case 4:
		length = 16
	default:
		return 0, nil, ErrUnavailable
	}
	var address [22]byte
	if _, err := io.ReadFull(c.Conn, address[:length+6]); err != nil {
		return 0, nil, err
	}
	port := binary.BigEndian.Uint16(address[length:])
	size := int(binary.BigEndian.Uint16(address[length+2:]))
	if port == 0 || size > 65507 || address[length+4] != '\r' || address[length+5] != '\n' {
		return 0, nil, ErrUnavailable
	}
	if size > len(payload) {
		return 0, nil, io.ErrShortBuffer
	}
	if _, err := io.ReadFull(c.Conn, payload[:size]); err != nil {
		return 0, nil, err
	}
	peer := &net.UDPAddr{IP: append(net.IP(nil), address[:length]...), Port: int(port)}
	return size, peer, nil
}

type datagramAddress string

func (a datagramAddress) Network() string { return "udp" }
func (a datagramAddress) String() string  { return string(a) }
