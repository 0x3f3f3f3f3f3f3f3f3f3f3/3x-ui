package dispatcher

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
)

type managedLinkKey struct{}

type managedLinkState struct {
	link     *transport.Link
	user     *protocol.MemoryUser
	clientID string
}

func managedLinkFromContext(ctx context.Context, link *transport.Link) *managedLinkState {
	state, _ := ctx.Value(managedLinkKey{}).(*managedLinkState)
	if state != nil && state.link == link {
		return state
	}
	return nil
}

func (d *DefaultDispatcher) manageLink(ctx context.Context, destination net.Destination, link *transport.Link) (context.Context, func(), error) {
	in := session.InboundFromContext(ctx)
	if state := managedLinkFromContext(ctx, link); state != nil {
		if in == nil || in.User != state.user || in.User.ClientID != state.clientID {
			return ctx, nil, errors.New("managed link identity changed during redispatch")
		}
		return ctx, func() {}, nil
	}
	if in == nil || in.User == nil || in.User.ClientID == "" {
		return ctx, func() {}, nil
	}
	if d.clients == nil {
		return ctx, nil, clientpolicy.ErrUnknownClient
	}
	child, cancel := context.WithCancel(ctx)
	reader, writer := link.Reader, link.Writer
	managedRead := &managedReader{Reader: reader, datagram: destination.Network == net.Network_UDP}
	metadata := clientpolicy.Metadata{ClientID: in.User.ClientID, InboundTag: in.Tag, AuthenticatedAccount: in.User.Email, OriginalTarget: destination.String(), ActualTarget: destination.String()}
	if out := session.OutboundsFromContext(ctx); len(out) > 0 {
		metadata.OriginalTarget = out[len(out)-1].OriginalTarget.String()
	}
	s, err := d.clients.Open(child, metadata, func() {
		cancel()
		managedRead.Interrupt()
		common.Interrupt(writer)
		if in.Conn != nil {
			in.Conn.Close()
		}
	})
	if err != nil {
		cancel()
		return ctx, nil, err
	}
	untrack, err := in.User.TrackSession(s.Close)
	if err != nil {
		s.Close()
		return ctx, nil, err
	}
	managedRead.session = s
	link.Reader = managedRead
	link.Writer = &managedWriter{Writer: writer, session: s, datagram: destination.Network == net.Network_UDP}
	child = context.WithValue(child, managedLinkKey{}, &managedLinkState{link: link, user: in.User, clientID: in.User.ClientID})
	return child, func() { untrack(); s.Release(); cancel(); managedRead.Interrupt() }, nil
}

type managedReader struct {
	Reader       buf.Reader
	session      *clientpolicy.Session
	datagram     bool
	mu           sync.Mutex
	pending      buf.MultiBuffer
	pendingErr   error
	deliveryDone func()
	closed       atomic.Bool
}

func (r *managedReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	r.mu.Lock()
	defer func() {
		if r.closed.Load() {
			buf.ReleaseMulti(r.pending)
			r.pending = nil
		}
		r.mu.Unlock()
		if r.closed.Load() {
			r.releasePending()
		}
	}()
	if finish := r.deliveryDone; finish != nil {
		r.deliveryDone = nil
		finish()
	}
	if r.closed.Load() {
		return nil, io.EOF
	}
	if r.pending.IsEmpty() {
		r.pending, r.pendingErr = r.Reader.ReadMultiBuffer()
		if r.pending.IsEmpty() {
			buf.ReleaseMulti(r.pending)
			r.pending = nil
			return nil, r.pendingErr
		}
	}
	for {
		var part buf.MultiBuffer
		atomicPacket := r.datagram || r.pending[0].UDP != nil
		if atomicPacket {
			var b *buf.Buffer
			r.pending, b = buf.SplitFirst(r.pending)
			part = buf.MultiBuffer{b}
		} else {
			limit, err := r.session.StreamChunkSize(clientpolicy.Upload)
			if err != nil {
				buf.ReleaseMulti(r.pending)
				r.pending = nil
				return nil, err
			}
			r.pending, part = buf.SplitSize(r.pending, int32(limit))
		}
		finish, err := r.session.AdmitPayload(clientpolicy.Upload, uint64(part.Len()))
		if err != nil {
			if !atomicPacket && errors.Is(err, clientpolicy.ErrPacketTooLarge) {
				r.pending = append(part, r.pending...)
				continue
			}
			buf.ReleaseMulti(part)
			buf.ReleaseMulti(r.pending)
			r.pending = nil
			return nil, err
		}
		// A stream forwarder consumes the returned buffer before asking for the
		// next one. Keep quota closure behind that boundary; Interrupt also joins it.
		r.deliveryDone = finish
		if r.pending.IsEmpty() {
			return part, r.pendingErr
		}
		return part, nil
	}
}

func (r *managedReader) releasePending() {
	var finish func()
	if r.mu.TryLock() {
		buf.ReleaseMulti(r.pending)
		r.pending = nil
		finish, r.deliveryDone = r.deliveryDone, nil
		r.mu.Unlock()
	}
	if finish != nil {
		finish()
	}
}

func (r *managedReader) Interrupt() {
	r.closed.Store(true)
	common.Interrupt(r.Reader)
	// Admission may synchronously close its own session while Read holds mu.
	// That read's deferred cleanup joins this release without a recursive lock.
	r.releasePending()
}

type managedWriter struct {
	Writer   buf.Writer
	session  *clientpolicy.Session
	datagram bool
}

func (w *managedWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	for len(mb) > 0 {
		var part buf.MultiBuffer
		atomicPacket := w.datagram || mb[0].UDP != nil
		if atomicPacket {
			var b *buf.Buffer
			mb, b = buf.SplitFirst(mb)
			part = buf.MultiBuffer{b}
		} else {
			limit, err := w.session.StreamChunkSize(clientpolicy.Download)
			if err != nil {
				buf.ReleaseMulti(mb)
				return err
			}
			mb, part = buf.SplitSize(mb, int32(limit))
		}
		finish, err := w.session.AdmitPayload(clientpolicy.Download, uint64(part.Len()))
		if err != nil {
			if !atomicPacket && errors.Is(err, clientpolicy.ErrPacketTooLarge) {
				mb = append(part, mb...)
				continue
			}
			buf.ReleaseMulti(part)
			buf.ReleaseMulti(mb)
			return err
		}
		err = w.Writer.WriteMultiBuffer(part)
		finish()
		if err != nil {
			buf.ReleaseMulti(mb)
			return err
		}
	}
	return nil
}

func (w *managedWriter) Close() error { return common.Close(w.Writer) }
func (w *managedWriter) Interrupt()   { common.Interrupt(w.Writer) }
