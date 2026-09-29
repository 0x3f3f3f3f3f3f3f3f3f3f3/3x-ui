package mieru

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	apicommon "github.com/enfein/mieru/v3/apis/common"
	apimodel "github.com/enfein/mieru/v3/apis/model"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
)

const (
	maxUDPTargets = 16
	udpIdleTime   = 30 * time.Second
)

type udpAssociation struct {
	server  *Server
	flow    *policyflow.Flow
	wire    *apicommon.PacketOverStreamTunnel
	writeMu sync.Mutex
	mu      sync.Mutex
	targets map[string]net.Conn
	workers sync.WaitGroup
}

func (s *Server) serveUDP(conn net.Conn, destination Destination) {
	flow, err := s.controller.Open(s.ctx, destination.PolicyID, conn)
	if err != nil {
		return
	}
	a := &udpAssociation{server: s, flow: flow, wire: apicommon.NewPacketOverStreamTunnel(conn), targets: make(map[string]net.Conn)}
	stop := context.AfterFunc(flow.Context(), a.closeTargets)
	defer func() {
		flow.Close()
		stop()
		a.closeTargets()
		a.workers.Wait()
	}()
	if err := writeReply(conn); err != nil {
		return
	}
	p := make([]byte, 65535)
	for {
		n, err := a.wire.Read(p)
		if err != nil {
			return
		}
		if n < 4 || p[0] != 0 || p[1] != 0 || p[2] != 0 {
			return
		}
		r := bytes.NewReader(p[3:n])
		var address apimodel.AddrSpec
		if err := address.ReadFromSocks5(r); err != nil || !setTarget(&destination, address) {
			return
		}
		writer := a.flow.DatagramWriter(policyflow.Upload, udpTargetWriter{association: a, destination: destination})
		if _, err := writer.Write(p[n-r.Len() : n]); err != nil && !packetQuotaRemainder(err) {
			return
		}
	}
}

func packetQuotaRemainder(err error) bool {
	var quota *database.UsageQuotaError
	return errors.As(err, &quota) && quota.RawAllowance > 0
}

func (a *udpAssociation) closeTargets() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, conn := range a.targets {
		_ = conn.Close()
	}
}

type udpTargetWriter struct {
	association *udpAssociation
	destination Destination
}

func (w udpTargetWriter) Write(p []byte) (int, error) {
	a := w.association
	key := net.JoinHostPort(w.destination.Host, strconv.Itoa(int(w.destination.Port)))
	a.mu.Lock()
	conn := a.targets[key]
	full := len(a.targets) >= maxUDPTargets
	a.mu.Unlock()
	if conn == nil {
		if full {
			return 0, errors.New("mieru UDP destination limit reached")
		}
		ctx, cancel := context.WithTimeout(a.flow.Context(), dialTimeout)
		defer cancel()
		var err error
		conn, err = a.server.dial(ctx, w.destination)
		if err != nil {
			return 0, err
		}
		a.mu.Lock()
		if err := a.flow.Context().Err(); err != nil {
			a.mu.Unlock()
			_ = conn.Close()
			return 0, err
		}
		a.targets[key] = conn
		a.mu.Unlock()
		a.workers.Go(func() { a.receive(key, conn) })
	}
	_ = conn.SetReadDeadline(time.Now().Add(udpIdleTime))
	_ = conn.SetWriteDeadline(time.Now().Add(dialTimeout))
	return conn.Write(p)
}

func (a *udpAssociation) receive(key string, conn net.Conn) {
	defer func() {
		_ = conn.Close()
		a.mu.Lock()
		if a.targets[key] == conn {
			delete(a.targets, key)
		}
		a.mu.Unlock()
	}()
	packetReader, perPacketPeer := conn.(interface {
		ReadFrom([]byte) (int, net.Addr, error)
	})
	p := make([]byte, policyflow.MaxDatagramSize+1)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(udpIdleTime))
		var n int
		var err error
		var peer net.Addr
		if perPacketPeer {
			n, peer, err = packetReader.ReadFrom(p)
		} else {
			n, err = conn.Read(p)
			peer = conn.RemoteAddr()
		}
		if err != nil {
			return
		}
		var address apimodel.AddrSpec
		if peer == nil || address.From(peer.String()) != nil || len(address.IP) == 0 {
			return
		}
		writer := a.flow.DatagramWriter(policyflow.Download, udpResponseWriter{association: a, address: address})
		if _, err := writer.Write(p[:n]); err != nil && !packetQuotaRemainder(err) {
			a.flow.Close()
			return
		}
	}
}

type udpResponseWriter struct {
	association *udpAssociation
	address     apimodel.AddrSpec
}

func (w udpResponseWriter) Write(p []byte) (int, error) {
	b := bytes.NewBuffer([]byte{0, 0, 0})
	if err := w.address.WriteToSocks5(b); err != nil {
		return 0, err
	}
	b.Write(p)
	a := w.association
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_ = a.wire.SetWriteDeadline(time.Now().Add(dialTimeout))
	n, err := a.wire.Write(b.Bytes())
	if err != nil {
		return 0, err
	}
	if n != b.Len() {
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}
