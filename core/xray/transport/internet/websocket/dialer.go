package websocket

import (
	"context"
	_ "embed"
	"encoding/base64"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/browser_dialer"
	"github.com/xtls/xray-core/transport/internet/stat"
	"github.com/xtls/xray-core/transport/internet/tls"
)

// Dial dials a WebSocket connection to the given destination.
func Dial(ctx context.Context, dest net.Destination, streamSettings *internet.MemoryStreamConfig) (stat.Connection, error) {
	errors.LogInfo(ctx, "creating connection to ", dest)
	var conn net.Conn
	if streamSettings.ProtocolSettings.(*Config).Ed > 0 {
		ctx, cancel := context.WithCancel(ctx)
		conn = &delayDialConn{
			dialed:         make(chan struct{}),
			cancel:         cancel,
			ctx:            ctx,
			dest:           dest,
			streamSettings: streamSettings,
		}
	} else {
		var err error
		if conn, err = dialWebSocket(ctx, dest, streamSettings, nil); err != nil {
			return nil, errors.New("failed to dial WebSocket").Base(err)
		}
	}
	return stat.Connection(conn), nil
}

func init() {
	common.Must(internet.RegisterTransportDialer(protocolName, Dial))
}

func dialWebSocket(ctx context.Context, dest net.Destination, streamSettings *internet.MemoryStreamConfig, ed []byte) (net.Conn, error) {
	wsSettings := streamSettings.ProtocolSettings.(*Config)

	dialer := &websocket.Dialer{
		NetDial: func(network, addr string) (net.Conn, error) {
			conn, err := internet.DialSystem(ctx, dest, streamSettings.SocketSettings)
			if err != nil {
				return nil, err
			}

			if streamSettings.TcpmaskManager != nil {
				newConn, err := streamSettings.TcpmaskManager.WrapConnClient(conn)
				if err != nil {
					conn.Close()
					return nil, errors.New("mask err").Base(err)
				}
				conn = newConn
			}

			return conn, err
		},
		ReadBufferSize:   4 * 1024,
		WriteBufferSize:  4 * 1024,
		HandshakeTimeout: time.Second * 8,
	}

	protocol := "ws"

	tConfig := tls.ConfigFromStreamSettings(streamSettings)
	if tConfig != nil {
		protocol = "wss"
		tlsConfig := tConfig.GetTLSConfig(tls.WithDestination(dest), tls.WithNextProto("http/1.1"))
		dialer.TLSClientConfig = tlsConfig
		if fingerprint := tls.GetFingerprint(tConfig.Fingerprint); fingerprint != nil {
			dialer.NetDialTLSContext = func(_ context.Context, _, addr string) (net.Conn, error) {
				// Like the NetDial in the dialer
				pconn, err := internet.DialSystem(ctx, dest, streamSettings.SocketSettings)
				if err != nil {
					errors.LogErrorInner(ctx, err, "failed to dial to "+addr)
					return nil, err
				}

				if streamSettings.TcpmaskManager != nil {
					newConn, err := streamSettings.TcpmaskManager.WrapConnClient(pconn)
					if err != nil {
						pconn.Close()
						return nil, errors.New("mask err").Base(err)
					}
					pconn = newConn
				}

				// TLS and apply the handshake
				cn := tls.UClient(pconn, tlsConfig, fingerprint).(*tls.UConn)
				if err := cn.WebsocketHandshakeContext(ctx); err != nil {
					errors.LogErrorInner(ctx, err, "failed to dial to "+addr)
					return nil, err
				}
				if !tlsConfig.InsecureSkipVerify {
					if err := cn.VerifyHostname(tlsConfig.ServerName); err != nil {
						errors.LogErrorInner(ctx, err, "failed to dial to "+addr)
						return nil, err
					}
				}
				return cn, nil
			}
		}
	}

	if browser_dialer.HasBrowserDialer() {
		// For Browser Dialer's optimized IP and non-standard port
		host := wsSettings.Host
		if host == "" && tConfig.ServerName != "" {
			host = tConfig.ServerName
		}
		if host == "" {
			host = dest.Address.String()
		}
		if !(protocol == "ws" && dest.Port == 80) && !(protocol == "wss" && dest.Port == 443) {
			host += ":" + dest.Port.String()
		}
		uri := protocol + "://" + host + wsSettings.GetNormalizedPath()

		conn, err := browser_dialer.DialWS(uri, ed)
		if err != nil {
			return nil, err
		}

		return NewConnection(conn, conn.RemoteAddr(), nil, wsSettings.HeartbeatPeriod), nil
	}

	host := dest.Address.String()
	if !(protocol == "ws" && dest.Port == 80) && !(protocol == "wss" && dest.Port == 443) {
		host += ":" + dest.Port.String()
	}
	uri := protocol + "://" + host + wsSettings.GetNormalizedPath()

	header := wsSettings.GetRequestHeader()
	// See dialer.DialContext()
	header.Set("Host", wsSettings.Host)
	if header.Get("Host") == "" && tConfig != nil {
		header.Set("Host", tConfig.ServerName)
	}
	if header.Get("Host") == "" {
		header.Set("Host", dest.Address.String())
	}
	if ed != nil {
		// RawURLEncoding is support by both V2Ray/V2Fly and XRay.
		header.Set("Sec-WebSocket-Protocol", base64.RawURLEncoding.EncodeToString(ed))
	}

	conn, resp, err := dialer.DialContext(ctx, uri, header)
	if err != nil {
		var reason string
		if resp != nil {
			reason = resp.Status
		}
		return nil, errors.New("failed to dial to (", uri, "): ", reason).Base(err)
	}

	return NewConnection(conn, conn.RemoteAddr(), nil, wsSettings.HeartbeatPeriod), nil
}

type delayDialConn struct {
	access         sync.Mutex
	writeAccess    sync.Mutex
	conn           net.Conn
	closed         bool
	closeOnce      sync.Once
	closeErr       error
	dialed         chan struct{}
	cancel         context.CancelFunc
	ctx            context.Context
	dest           net.Destination
	streamSettings *internet.MemoryStreamConfig
}

func (d *delayDialConn) LocalAddr() net.Addr {
	d.access.Lock()
	defer d.access.Unlock()
	if d.conn == nil {
		return nil
	}
	return d.conn.LocalAddr()
}

func (d *delayDialConn) RemoteAddr() net.Addr {
	d.access.Lock()
	defer d.access.Unlock()
	if d.conn == nil {
		return nil
	}
	return d.conn.RemoteAddr()
}

func (d *delayDialConn) Write(b []byte) (int, error) {
	d.writeAccess.Lock()
	defer d.writeAccess.Unlock()
	d.access.Lock()
	if d.closed {
		d.access.Unlock()
		return 0, io.ErrClosedPipe
	}
	conn := d.conn
	d.access.Unlock()
	if conn == nil {
		ed := b
		if len(ed) > int(d.streamSettings.ProtocolSettings.(*Config).Ed) {
			ed = nil
		}
		var err error
		conn, err = dialWebSocket(d.ctx, d.dest, d.streamSettings, ed)
		if err != nil {
			_ = d.Close()
			return 0, errors.New("failed to dial WebSocket").Base(err)
		}
		d.access.Lock()
		if d.closed {
			d.access.Unlock()
			_ = conn.Close()
			return 0, io.ErrClosedPipe
		}
		d.conn = conn
		close(d.dialed)
		d.access.Unlock()
		if ed != nil {
			return len(ed), nil
		}
	}
	return conn.Write(b)
}

func (d *delayDialConn) Read(b []byte) (int, error) {
	d.access.Lock()
	if d.closed {
		d.access.Unlock()
		return 0, io.ErrClosedPipe
	}
	conn := d.conn
	d.access.Unlock()
	if conn == nil {
		select {
		case <-d.ctx.Done():
			return 0, io.ErrClosedPipe
		case <-d.dialed:
		}
		d.access.Lock()
		if d.closed {
			d.access.Unlock()
			return 0, io.ErrClosedPipe
		}
		conn = d.conn
		d.access.Unlock()
	}
	return conn.Read(b)
}

func (d *delayDialConn) Close() error {
	d.closeOnce.Do(func() {
		d.access.Lock()
		d.closed = true
		conn := d.conn
		d.access.Unlock()
		d.cancel()
		if conn != nil {
			d.closeErr = conn.Close()
		}
	})
	return d.closeErr
}

func (d *delayDialConn) SetDeadline(t time.Time) error {
	d.access.Lock()
	defer d.access.Unlock()
	if d.closed {
		return io.ErrClosedPipe
	}
	if d.conn == nil {
		return errors.New("WebSocket handshake has not completed")
	}
	return d.conn.SetDeadline(t)
}

func (d *delayDialConn) SetReadDeadline(t time.Time) error {
	d.access.Lock()
	defer d.access.Unlock()
	if d.closed {
		return io.ErrClosedPipe
	}
	if d.conn == nil {
		return errors.New("WebSocket handshake has not completed")
	}
	return d.conn.SetReadDeadline(t)
}

func (d *delayDialConn) SetWriteDeadline(t time.Time) error {
	d.access.Lock()
	defer d.access.Unlock()
	if d.closed {
		return io.ErrClosedPipe
	}
	if d.conn == nil {
		return errors.New("WebSocket handshake has not completed")
	}
	return d.conn.SetWriteDeadline(t)
}
