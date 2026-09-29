package splithttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"

	"github.com/apernet/quic-go"
)

type requestDialContextKey struct{}

var errNoDialWaiters = errors.New("XHTTP dial has no remaining waiters")

type pendingDialGroup struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	waiters int
}

func (c *DefaultDialerClient) beginRequestDial(ctx context.Context) (context.Context, func(), error) {
	c.dialAccess.Lock()
	defer c.dialAccess.Unlock()
	if c.closing {
		return nil, nil, net.ErrClosed
	}
	shared := c.httpVersion == "2" || c.httpVersion == "3"
	group := c.pendingDial
	if !shared || group == nil {
		parent := context.Background()
		if !shared {
			parent = ctx
		}
		owned, cancel := context.WithCancelCause(parent)
		group = &pendingDialGroup{ctx: owned, cancel: cancel}
		if c.dialGroups == nil {
			c.dialGroups = make(map[*pendingDialGroup]struct{})
		}
		c.dialGroups[group] = struct{}{}
		if shared {
			c.pendingDial = group
		}
	}
	group.waiters++
	var once sync.Once
	return context.WithValue(ctx, requestDialContextKey{}, group.ctx), func() {
		once.Do(func() {
			c.dialAccess.Lock()
			defer c.dialAccess.Unlock()
			group.waiters--
			if group.waiters == 0 {
				group.cancel(errNoDialWaiters)
				delete(c.dialGroups, group)
				if c.pendingDial == group {
					c.pendingDial = nil
				}
			}
		})
	}, nil
}

func ownedDialContext(ctx context.Context) (context.Context, func()) {
	owner, ok := ctx.Value(requestDialContextKey{}).(context.Context)
	if !ok {
		return ctx, func() {}
	}
	dial, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	stop := context.AfterFunc(owner, func() { cancel(context.Cause(owner)) })
	if owner.Err() != nil {
		cancel(context.Cause(owner))
	}
	return dial, func() { stop(); cancel(context.Canceled) }
}

type retiredDialError struct{ error }

func (e *retiredDialError) Unwrap() error { return e.error }

func dialError(ctx context.Context, err error) error {
	if err != nil && errors.Is(context.Cause(ctx), errNoDialWaiters) {
		return &retiredDialError{err}
	}
	return err
}

// A transport may close the body before acquiring a connection; keep it for a safe retry.
type requestAttemptBody struct {
	io.ReadCloser
	acquired atomic.Bool
}

func (b *requestAttemptBody) Close() error {
	if b.acquired.Load() {
		return b.ReadCloser.Close()
	}
	return nil
}

func (c *DefaultDialerClient) doRequest(req *http.Request) (*http.Response, error) {
	for {
		ctx, release, err := c.beginRequestDial(req.Context())
		if err != nil {
			if req.Body != nil {
				_ = req.Body.Close()
			}
			return nil, err
		}
		var acquired atomic.Bool
		var body *requestAttemptBody
		if req.Body != nil {
			body = &requestAttemptBody{ReadCloser: req.Body}
		}
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) {
			acquired.Store(true)
			if body != nil {
				body.acquired.Store(true)
			}
		}})
		attempt := req.WithContext(ctx)
		if body != nil {
			attempt.Body = body
		}
		stop := context.AfterFunc(req.Context(), release)
		resp, err := c.client.Do(attempt)
		stop()
		release()
		var retired *retiredDialError
		if err != nil && !acquired.Load() && req.Context().Err() == nil && errors.As(err, &retired) {
			continue
		}
		if err != nil && req.Body != nil {
			_ = req.Body.Close()
		}
		return resp, err
	}
}

func (c *DefaultDialerClient) ownQUICConnection(conn *quic.Conn) error {
	c.dialAccess.Lock()
	defer c.dialAccess.Unlock()
	if c.closing {
		return net.ErrClosed
	}
	if c.quicConnections == nil {
		c.quicConnections = make(map[*quic.Conn]struct{})
	}
	c.quicConnections[conn] = struct{}{}
	context.AfterFunc(conn.Context(), func() {
		c.dialAccess.Lock()
		delete(c.quicConnections, conn)
		c.dialAccess.Unlock()
	})
	return nil
}
