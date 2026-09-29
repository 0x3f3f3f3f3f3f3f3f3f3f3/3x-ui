package splithttp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	gonet "net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"

	"github.com/apernet/quic-go"
	"github.com/apernet/quic-go/http3"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/signal/done"
)

// interface to abstract between use of browser dialer, vs net/http
type DialerClient interface {
	IsClosed() bool

	// ctx, url, sessionId, body, uploadOnly
	OpenStream(context.Context, string, string, io.Reader, bool) (io.ReadCloser, net.Addr, net.Addr, error)

	// ctx, url, sessionId, seqStr, body, contentLength
	PostPacket(context.Context, string, string, string, buf.MultiBuffer) error
}

// implements splithttp.DialerClient in terms of direct network connections
type DefaultDialerClient struct {
	transportConfig *Config
	client          *http.Client
	closed          atomic.Bool
	dialAccess      sync.Mutex
	pendingDial     *pendingDialGroup
	closing         bool
	dialGroups      map[*pendingDialGroup]struct{}
	quicConnections map[*quic.Conn]struct{}
	httpVersion     string
	// pool of net.Conn, created using dialUploadConn
	uploadRawPool  *sync.Pool
	dialUploadConn func(ctxInner context.Context) (net.Conn, error)
}

func (c *DefaultDialerClient) IsClosed() bool {
	return c.closed.Load()
}

func (c *DefaultDialerClient) OpenStream(ctx context.Context, url string, sessionId string, body io.Reader, uploadOnly bool) (io.ReadCloser, net.Addr, net.Addr, error) {
	type addresses struct{ remote, local net.Addr }
	connected := make(chan addresses, 1)
	failed := make(chan error, 1)
	ctx, cancel := context.WithCancel(ctx)
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		select {
		case connected <- addresses{info.Conn.RemoteAddr(), info.Conn.LocalAddr()}:
		default:
		}
	}})
	method := "GET"
	if body != nil {
		method = c.transportConfig.GetNormalizedUplinkHTTPMethod()
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	c.transportConfig.FillStreamRequest(req, sessionId, "")
	stream := &WaitReadCloser{wait: done.New(), cancel: cancel}
	go func() {
		resp, err := c.doRequest(req)
		if err != nil {
			if !uploadOnly && ctx.Err() == nil {
				c.closed.Store(true)
				errors.LogInfoInner(ctx, err, "failed to "+method+" "+url)
			}
			failed <- err
			common.Close(body)
			stream.Close()
			return
		}
		if resp.StatusCode != 200 && !uploadOnly {
			errors.LogInfo(ctx, "unexpected status ", resp.StatusCode)
		}
		if resp.StatusCode != 200 || uploadOnly {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			common.Close(body)
			stream.Close()
			return
		}
		stream.Set(resp.Body)
	}()
	select {
	case address := <-connected:
		if err := ctx.Err(); err != nil {
			stream.Close()
			return nil, nil, nil, err
		}
		return stream, address.remote, address.local, nil
	case err := <-failed:
		stream.Close()
		return nil, nil, nil, err
	case <-ctx.Done():
		stream.Close()
		return nil, nil, nil, ctx.Err()
	}
}

func (c *DefaultDialerClient) PostPacket(ctx context.Context, url string, sessionId string, seqStr string, payload buf.MultiBuffer) error {
	method := c.transportConfig.GetNormalizedUplinkHTTPMethod()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	c.transportConfig.FillPacketRequest(req, sessionId, seqStr, payload)

	if c.httpVersion != "1.1" {
		resp, err := c.doRequest(req)
		if err != nil {
			if ctx.Err() == nil {
				c.closed.Store(true)
			}
			return err
		}

		defer resp.Body.Close()
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			return err
		}

		if resp.StatusCode != 200 {
			return errors.New("bad status code:", resp.Status)
		}
	} else {
		owned, release, err := c.beginRequestDial(ctx)
		if err != nil {
			return err
		}
		defer release()
		ctx, finish := ownedDialContext(owned)
		defer finish()
		req = req.WithContext(ctx)
		// stringify the entire HTTP/1.1 request so it can be
		// safely retried. if instead req.Write is called multiple
		// times, the body is already drained after the first
		// request
		requestBuff := new(bytes.Buffer)
		requestBuff.Grow(512 + int(req.ContentLength))
		common.Must(req.Write(requestBuff))

		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			pooled := c.uploadRawPool.Get()
			newConnection := pooled == nil
			var conn *H1Conn
			if newConnection {
				raw, err := c.dialUploadConn(ctx)
				if err != nil {
					return err
				}
				conn = NewH1Conn(raw)
			} else {
				conn = pooled.(*H1Conn)
			}
			retry, err := c.writeHTTP1Packet(ctx, conn, req, requestBuff.Bytes())
			if err == nil {
				c.uploadRawPool.Put(conn)
				break
			}
			_ = conn.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !retry || newConnection {
				return err
			}
		}
	}

	return nil
}

func (c *DefaultDialerClient) writeHTTP1Packet(ctx context.Context, conn *H1Conn, req *http.Request, data []byte) (retry bool, err error) {
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = conn.Close()
		close(closed)
	})
	defer func() {
		if !stop() {
			<-closed
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if conn.UnreadedResponsesCount > 0 {
		resp, err := http.ReadResponse(conn.RespBufReader, req)
		if err != nil {
			if ctx.Err() == nil {
				c.closed.Store(true)
			}
			return false, fmt.Errorf("error while reading response: %w", err)
		}
		_, err = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return false, err
		}
		if resp.StatusCode != 200 {
			return false, fmt.Errorf("got non-200 error response code: %d", resp.StatusCode)
		}
	}
	_, err = conn.Write(data)
	return true, err
}

// Close cancels pending dials and owns QUIC connections independently of the HTTP cache.
func (c *DefaultDialerClient) Close() error {
	c.dialAccess.Lock()
	c.closed.Store(true)
	c.closing = true
	for group := range c.dialGroups {
		group.cancel(gonet.ErrClosed)
	}
	connections := make([]*quic.Conn, 0, len(c.quicConnections))
	for conn := range c.quicConnections {
		connections = append(connections, conn)
	}
	c.dialAccess.Unlock()
	for _, conn := range connections {
		_ = conn.CloseWithError(0, "XHTTP client closed")
	}
	transport := c.client.Transport
	if h3Transport, ok := transport.(*http3.Transport); ok {
		h3Transport.Close()
	}
	return nil
}

type WaitReadCloser struct {
	cancel context.CancelFunc
	wait   *done.Instance
	reader atomic.Pointer[io.ReadCloser]
}

func (w *WaitReadCloser) Set(rc io.ReadCloser) {
	w.reader.Store(&rc)
	if w.wait.Done() {
		if p := w.reader.Swap(nil); p != nil {
			(*p).Close()
		}
	}
	w.wait.Close()
}

func (w *WaitReadCloser) Read(b []byte) (int, error) {
	rc := w.reader.Load()
	if rc == nil {
		<-w.wait.Wait()
		if rc = w.reader.Load(); rc == nil {
			return 0, io.ErrClosedPipe
		}
	}
	return (*rc).Read(b)
}

func (w *WaitReadCloser) Close() error {
	if w.cancel != nil {
		w.cancel()
	}
	w.wait.Close()
	if p := w.reader.Swap(nil); p != nil {
		return (*p).Close()
	}
	return nil
}
