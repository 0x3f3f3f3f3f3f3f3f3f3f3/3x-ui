package dispatcher

import (
	"context"
	"errors"

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
	metadata := clientpolicy.Metadata{ClientID: in.User.ClientID, InboundTag: in.Tag, AuthenticatedAccount: in.User.Email, OriginalTarget: destination.String(), ActualTarget: destination.String()}
	if out := session.OutboundsFromContext(ctx); len(out) > 0 {
		metadata.OriginalTarget = out[len(out)-1].OriginalTarget.String()
	}
	s, err := d.clients.Open(child, metadata, func() {
		cancel()
		common.Interrupt(reader)
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
	link.Reader = &managedReader{Reader: reader, session: s}
	link.Writer = &managedWriter{Writer: writer, session: s}
	child = context.WithValue(child, managedLinkKey{}, &managedLinkState{link: link, user: in.User, clientID: in.User.ClientID})
	return child, func() { untrack(); s.Release(); cancel() }, nil
}

type managedReader struct {
	Reader  buf.Reader
	session *clientpolicy.Session
}

func (r *managedReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := r.Reader.ReadMultiBuffer()
	for _, b := range mb {
		if admitErr := r.session.Admit(clientpolicy.Upload, uint64(b.Len())); admitErr != nil {
			buf.ReleaseMulti(mb)
			return nil, admitErr
		}
	}
	return mb, err
}

func (r *managedReader) Interrupt() { common.Interrupt(r.Reader) }

type managedWriter struct {
	Writer  buf.Writer
	session *clientpolicy.Session
}

func (w *managedWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	for len(mb) > 0 {
		b := mb[0]
		mb = mb[1:]
		if err := w.session.Admit(clientpolicy.Download, uint64(b.Len())); err != nil {
			b.Release()
			buf.ReleaseMulti(mb)
			return err
		}
		if err := w.Writer.WriteMultiBuffer(buf.MultiBuffer{b}); err != nil {
			buf.ReleaseMulti(mb)
			return err
		}
	}
	return nil
}

func (w *managedWriter) Close() error { return common.Close(w.Writer) }
func (w *managedWriter) Interrupt()   { common.Interrupt(w.Writer) }
