package sshoutbound

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"time"
)

const BridgeHandshakeTimeout = 5 * time.Second

func (m *Manager) serve(client net.Conn) {
	_ = client.SetDeadline(time.Now().Add(BridgeHandshakeTimeout))
	var greeting [2]byte
	if _, err := io.ReadFull(client, greeting[:]); err != nil || greeting[0] != 5 || greeting[1] == 0 {
		return
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(client, methods); err != nil {
		return
	}
	passwordMethod := false
	for _, method := range methods {
		if method == 2 {
			passwordMethod = true
		}
	}
	if !passwordMethod {
		_, _ = client.Write([]byte{5, 255})
		return
	}
	if _, err := client.Write([]byte{5, 2}); err != nil {
		return
	}
	var auth [2]byte
	if _, err := io.ReadFull(client, auth[:]); err != nil || auth[0] != 1 || auth[1] == 0 {
		return
	}
	user := make([]byte, int(auth[1]))
	if _, err := io.ReadFull(client, user); err != nil {
		return
	}
	var size [1]byte
	if _, err := io.ReadFull(client, size[:]); err != nil || size[0] == 0 {
		return
	}
	password := make([]byte, int(size[0]))
	if _, err := io.ReadFull(client, password); err != nil {
		return
	}
	connector := m.authenticate(client, string(user), string(password))
	if connector == nil {
		_, _ = client.Write([]byte{1, 1})
		return
	}
	if _, err := client.Write([]byte{1, 0}); err != nil {
		return
	}
	target, code := readSOCKSTarget(client)
	if code != 0 {
		replySOCKS(client, code)
		return
	}
	_ = client.SetDeadline(time.Now().Add(DialTimeout))
	upstream, err := connector.DialContext(context.Background(), "tcp", target)
	if err != nil {
		replySOCKS(client, 5)
		return
	}
	defer upstream.Close()
	if !replySOCKS(client, 0) {
		return
	}
	_ = client.SetDeadline(time.Time{})
	relayTCP(client, upstream)
}

func readSOCKSTarget(client io.Reader) (string, byte) {
	var header [4]byte
	if _, err := io.ReadFull(client, header[:]); err != nil || header[0] != 5 || header[2] != 0 {
		return "", 1
	}
	if header[1] != 1 {
		return "", 7
	}
	var host string
	switch header[3] {
	case 1, 4:
		length := 4
		if header[3] == 4 {
			length = 16
		}
		address := make([]byte, length)
		if _, err := io.ReadFull(client, address); err != nil {
			return "", 8
		}
		host = net.IP(address).String()
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(client, size[:]); err != nil || size[0] == 0 {
			return "", 8
		}
		address := make([]byte, int(size[0]))
		if _, err := io.ReadFull(client, address); err != nil {
			return "", 8
		}
		host = string(address)
	default:
		return "", 8
	}
	var port [2]byte
	if _, err := io.ReadFull(client, port[:]); err != nil {
		return "", 8
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:]))))
	if !validTarget(target) {
		return "", 8
	}
	return target, 0
}

func replySOCKS(client io.Writer, code byte) bool {
	_, err := client.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
	return err == nil
}

func relayTCP(client, upstream net.Conn) {
	done := make(chan struct{}, 2)
	copyOne := func(destination, source net.Conn) {
		_, err := io.CopyBuffer(destination, source, make([]byte, 32<<10))
		if err != nil {
			_ = client.Close()
			_ = upstream.Close()
		} else if half, ok := destination.(interface{ CloseWrite() error }); ok {
			if err := half.CloseWrite(); err != nil {
				_ = client.Close()
				_ = upstream.Close()
			}
		}
		done <- struct{}{}
	}
	go copyOne(client, upstream)
	go copyOne(upstream, client)
	<-done
	<-done
}
