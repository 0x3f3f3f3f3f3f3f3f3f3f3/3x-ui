// Package routedbridge connects authenticated managed flows to the existing Xray router.
package routedbridge

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrConfig      = errors.New("invalid managed routing bridge configuration")
	ErrIdentity    = errors.New("client is not bound to this routing bridge")
	ErrDestination = errors.New("invalid managed routing destination")
	ErrUnavailable = errors.New("managed routing bridge unavailable")
)

type ClientBinding struct {
	PolicyID string
	Email    string
}

type credential struct {
	ClientBinding
	password string
}

type Bridge struct {
	tag     string
	address netip.AddrPort
	clients map[string]credential
}

func New(tag string, address netip.AddrPort, bindings []ClientBinding) (*Bridge, error) {
	if tag == "" || !address.IsValid() || !address.Addr().IsLoopback() || address.Addr().Zone() != "" || address.Port() == 0 || len(bindings) == 0 {
		return nil, ErrConfig
	}
	b := &Bridge{tag: tag, address: address, clients: make(map[string]credential, len(bindings))}
	emails := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		id, err := uuid.Parse(binding.PolicyID)
		if err != nil || id == uuid.Nil || id.String() != binding.PolicyID || len(binding.Email) == 0 || len(binding.Email) > 255 || strings.ContainsAny(binding.Email, "\x00\r\n>") || emails[binding.Email] {
			return nil, ErrConfig
		}
		if _, exists := b.clients[binding.PolicyID]; exists {
			return nil, ErrConfig
		}
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return nil, fmt.Errorf("%w: credential generation failed", ErrConfig)
		}
		b.clients[binding.PolicyID] = credential{ClientBinding: binding, password: base64.RawURLEncoding.EncodeToString(secret[:])}
		emails[binding.Email] = true
	}
	return b, nil
}

// DialTCP must be called after policy admission; the bridge never meters payload a second time.
func (b *Bridge) DialTCP(ctx context.Context, policyID string, source netip.AddrPort, host string, port uint16) (net.Conn, error) {
	client, ok := b.clients[policyID]
	if !ok {
		return nil, ErrIdentity
	}
	if !source.IsValid() || source.Port() == 0 || source.Addr().Zone() != "" || port == 0 {
		return nil, ErrDestination
	}
	address, err := socksAddress(host, port)
	if err != nil {
		return nil, err
	}
	if dest, err := netip.ParseAddr(host); err == nil && dest.Unmap() == b.address.Addr().Unmap() && port == b.address.Port() {
		return nil, ErrDestination
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(handshakeCtx, "tcp", b.address.String())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	stop := context.AfterFunc(handshakeCtx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := handshakeCtx.Deadline()
	_ = conn.SetDeadline(deadline)
	if err := b.handshake(conn, client, source, address); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !stop() || handshakeCtx.Err() != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, handshakeCtx.Err())
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func socksAddress(host string, port uint16) ([]byte, error) {
	var address []byte
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return nil, ErrDestination
		}
		ip = ip.Unmap()
		if ip.Is4() {
			address = append([]byte{1}, ip.AsSlice()...)
		} else {
			address = append([]byte{4}, ip.AsSlice()...)
		}
	} else {
		if len(host) == 0 || len(host) > 253 || strings.ContainsAny(host, "\x00\r\n\t /:\\") {
			return nil, ErrDestination
		}
		address = append([]byte{3, byte(len(host))}, host...)
	}
	return binary.BigEndian.AppendUint16(address, port), nil
}

func (b *Bridge) handshake(conn net.Conn, client credential, source netip.AddrPort, address []byte) error {
	family, destination := "TCP4", "127.0.0.1"
	ip := source.Addr().Unmap()
	if ip.Is6() {
		family, destination = "TCP6", "::1"
	}
	header := fmt.Sprintf("PROXY %s %s %s %d %d\r\n", family, ip, destination, source.Port(), b.address.Port())
	if _, err := io.WriteString(conn, header); err != nil {
		return err
	}
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	if reply != [2]byte{5, 2} {
		return errors.New("bridge did not require credential authentication")
	}
	request := append([]byte{1, byte(len(client.Email))}, client.Email...)
	request = append(request, byte(len(client.password)))
	request = append(request, client.password...)
	if _, err := conn.Write(request); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	if reply != [2]byte{1, 0} {
		return errors.New("bridge credential rejected")
	}
	if _, err := conn.Write(append([]byte{5, 1, 0}, address...)); err != nil {
		return err
	}
	var response [4]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		return err
	}
	if response[0] != 5 || response[1] != 0 || response[2] != 0 {
		return errors.New("bridge refused connection")
	}
	length := 0
	switch response[3] {
	case 1:
		length = 4
	case 4:
		length = 16
	case 3:
		if _, err := io.ReadFull(conn, reply[:1]); err != nil {
			return err
		}
		length = int(reply[0])
	default:
		return errors.New("invalid bridge reply address")
	}
	_, err := io.CopyN(io.Discard, conn, int64(length+2))
	return err
}
