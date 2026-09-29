package inbound

import (
	"context"
	gonet "net"
	"sync"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
)

// Manager manages all inbound handlers.
type Manager struct {
	access           sync.RWMutex
	untaggedHandlers []*ownedHandler
	taggedHandlers   map[string]*ownedHandler
	running          bool
	closed           bool
	closeOnce        sync.Once
	closeErr         error
	retired          map[*ownedHandler]struct{}
}

type ownedHandler struct {
	handler inbound.Handler
	once    sync.Once
	err     error
}

func (h *ownedHandler) close() error {
	h.once.Do(func() { h.err = h.handler.Close() })
	return h.err
}

// New returns a new Manager for inbound handlers.
func New(ctx context.Context, config *proxyman.InboundConfig) (*Manager, error) {
	m := &Manager{
		taggedHandlers: make(map[string]*ownedHandler),
		retired:        make(map[*ownedHandler]struct{}),
	}
	return m, nil
}

// Type implements common.HasType.
func (*Manager) Type() interface{} {
	return inbound.ManagerType()
}

// AddHandler implements inbound.Manager.
func (m *Manager) AddHandler(ctx context.Context, handler inbound.Handler) error {
	m.access.Lock()
	defer m.access.Unlock()
	if m.closed {
		return gonet.ErrClosed
	}
	tag := handler.Tag()
	if tag != "" {
		if _, found := m.taggedHandlers[tag]; found {
			return errors.New("existing tag found: " + tag)
		}
	}
	if m.running {
		if err := handler.Start(); err != nil {
			return err
		}
	}
	owned := &ownedHandler{handler: handler}
	if tag != "" {
		m.taggedHandlers[tag] = owned
	} else {
		m.untaggedHandlers = append(m.untaggedHandlers, owned)
	}
	return nil
}

// GetHandler implements inbound.Manager.
func (m *Manager) GetHandler(ctx context.Context, tag string) (inbound.Handler, error) {
	m.access.RLock()
	defer m.access.RUnlock()

	if m.closed {
		return nil, gonet.ErrClosed
	}
	handler, found := m.taggedHandlers[tag]
	if !found {
		return nil, errors.New("handler not found: ", tag)
	}
	return handler.handler, nil
}

// RemoveHandler implements inbound.Manager.
func (m *Manager) RemoveHandler(ctx context.Context, tag string) error {
	if tag == "" {
		return common.ErrNoClue
	}
	m.access.Lock()
	if m.closed {
		m.access.Unlock()
		return gonet.ErrClosed
	}
	owned := m.taggedHandlers[tag]
	if owned == nil {
		m.access.Unlock()
		return common.ErrNoClue
	}
	delete(m.taggedHandlers, tag)
	m.retired[owned] = struct{}{}
	m.access.Unlock()
	err := owned.close()
	if err == nil {
		m.access.Lock()
		delete(m.retired, owned)
		m.access.Unlock()
	}
	return err
}

// ListHandlers implements inbound.Manager.
func (m *Manager) ListHandlers(ctx context.Context) []inbound.Handler {
	m.access.RLock()
	defer m.access.RUnlock()

	response := make([]inbound.Handler, 0, len(m.untaggedHandlers)+len(m.taggedHandlers))
	for _, h := range m.untaggedHandlers {
		response = append(response, h.handler)
	}

	for _, v := range m.taggedHandlers {
		response = append(response, v.handler)
	}

	return response
}

// Start implements common.Runnable.
func (m *Manager) Start() error {
	m.access.Lock()
	defer m.access.Unlock()

	if m.closed {
		return gonet.ErrClosed
	}
	m.running = true

	for _, handler := range m.taggedHandlers {
		if err := handler.handler.Start(); err != nil {
			return err
		}
	}

	for _, handler := range m.untaggedHandlers {
		if err := handler.handler.Start(); err != nil {
			return err
		}
	}
	return nil
}

// Close implements common.Closable.
func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.access.Lock()
		m.closed = true
		m.running = false
		handlers := append([]*ownedHandler(nil), m.untaggedHandlers...)
		for _, h := range m.taggedHandlers {
			handlers = append(handlers, h)
		}
		for h := range m.retired {
			handlers = append(handlers, h)
		}
		m.taggedHandlers = nil
		m.untaggedHandlers = nil
		m.retired = nil
		m.access.Unlock()
		errs := make([]error, len(handlers))
		var closed sync.WaitGroup
		for i, h := range handlers {
			closed.Add(1)
			go func() { defer closed.Done(); errs[i] = h.close() }()
		}
		closed.Wait()
		m.closeErr = errors.Combine(errs...)
	})
	return m.closeErr
}

// NewHandler creates a new inbound.Handler based on the given config.
func NewHandler(ctx context.Context, config *core.InboundHandlerConfig) (inbound.Handler, error) {
	rawReceiverSettings, err := config.ReceiverSettings.GetInstance()
	if err != nil {
		return nil, err
	}
	proxySettings, err := config.ProxySettings.GetInstance()
	if err != nil {
		return nil, err
	}
	tag := config.Tag

	receiverSettings, ok := rawReceiverSettings.(*proxyman.ReceiverConfig)
	if !ok {
		return nil, errors.New("not a ReceiverConfig").AtError()
	}

	streamSettings := receiverSettings.StreamSettings
	if streamSettings != nil && streamSettings.SocketSettings != nil {
		ctx = session.ContextWithSockopt(ctx, &session.Sockopt{
			Mark: streamSettings.SocketSettings.Mark,
		})
	}
	if streamSettings != nil && streamSettings.ProtocolName == "splithttp" {
		ctx = session.ContextWithAllowedNetwork(ctx, net.Network_UDP)
	}

	return NewAlwaysOnInboundHandler(ctx, tag, receiverSettings, proxySettings)
}

func init() {
	common.Must(common.RegisterConfig((*proxyman.InboundConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return New(ctx, config.(*proxyman.InboundConfig))
	}))
	common.Must(common.RegisterConfig((*core.InboundHandlerConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return NewHandler(ctx, config.(*core.InboundHandlerConfig))
	}))
}

// CloseForTrafficDrain permanently rejects handler admission and closes all owned handlers.
func (m *Manager) CloseForTrafficDrain() error { return m.Close() }
