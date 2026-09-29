package outbound

import (
	"context"
	"net"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/outbound"
)

// Manager is to manage all outbound handlers.
type Manager struct {
	access           sync.RWMutex
	defaultHandler   *ownedHandler
	taggedHandler    map[string]*ownedHandler
	untaggedHandlers []*ownedHandler
	running          bool
	tagsCache        *sync.Map
	closed           bool
	closeOnce        sync.Once
	closeErr         error
	retired          map[*ownedHandler]struct{}
}

type ownedHandler struct {
	handler outbound.Handler
	once    sync.Once
	err     error
}

func (h *ownedHandler) close() error {
	h.once.Do(func() { h.err = h.handler.Close() })
	return h.err
}

// New creates a new Manager.
func New(ctx context.Context, config *proxyman.OutboundConfig) (*Manager, error) {
	m := &Manager{
		taggedHandler: make(map[string]*ownedHandler),
		tagsCache:     &sync.Map{},
		retired:       make(map[*ownedHandler]struct{}),
	}
	return m, nil
}

// Type implements common.HasType.
func (m *Manager) Type() interface{} {
	return outbound.ManagerType()
}

// Start implements core.Feature
func (m *Manager) Start() error {
	m.access.Lock()
	defer m.access.Unlock()

	if m.closed {
		return net.ErrClosed
	}
	m.running = true

	for _, h := range m.taggedHandler {
		if err := h.handler.Start(); err != nil {
			return err
		}
	}

	for _, h := range m.untaggedHandlers {
		if err := h.handler.Start(); err != nil {
			return err
		}
	}

	return nil
}

// Close implements core.Feature
func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.access.Lock()
		m.closed = true
		m.running = false
		handlers := append([]*ownedHandler(nil), m.untaggedHandlers...)
		for _, h := range m.taggedHandler {
			handlers = append(handlers, h)
		}
		for h := range m.retired {
			handlers = append(handlers, h)
		}
		m.defaultHandler = nil
		m.taggedHandler = nil
		m.untaggedHandlers = nil
		m.retired = nil
		m.tagsCache = &sync.Map{}
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

// GetDefaultHandler implements outbound.Manager.
func (m *Manager) GetDefaultHandler() outbound.Handler {
	m.access.RLock()
	defer m.access.RUnlock()

	if m.defaultHandler == nil {
		return nil
	}
	return m.defaultHandler.handler
}

// GetHandler implements outbound.Manager.
func (m *Manager) GetHandler(tag string) outbound.Handler {
	m.access.RLock()
	defer m.access.RUnlock()
	if handler, found := m.taggedHandler[tag]; found {
		return handler.handler
	}
	return nil
}

// AddHandler implements outbound.Manager.
func (m *Manager) AddHandler(ctx context.Context, handler outbound.Handler) error {
	return m.addHandler(ctx, handler, true)
}

func (m *Manager) AddHandlerWithoutDefault(ctx context.Context, handler outbound.Handler) error {
	return m.addHandler(ctx, handler, false)
}

func (m *Manager) addHandler(ctx context.Context, handler outbound.Handler, allowDefault bool) error {
	m.access.Lock()
	defer m.access.Unlock()
	if m.closed {
		return net.ErrClosed
	}
	tag := handler.Tag()
	if tag != "" {
		if _, found := m.taggedHandler[tag]; found {
			return errors.New("existing tag found: " + tag)
		}
	}
	if m.running {
		if err := handler.Start(); err != nil {
			return err
		}
	}
	owned := &ownedHandler{handler: handler}
	m.tagsCache = &sync.Map{}
	if allowDefault && m.defaultHandler == nil {
		m.defaultHandler = owned
	}
	if tag != "" {
		m.taggedHandler[tag] = owned
	} else {
		m.untaggedHandlers = append(m.untaggedHandlers, owned)
	}
	return nil
}

// RemoveHandler implements outbound.Manager.
func (m *Manager) RemoveHandler(ctx context.Context, tag string) error {
	return m.removeHandler(ctx, tag, nil)
}

func (m *Manager) RemoveHandlerIf(ctx context.Context, tag string, expected outbound.Handler) error {
	if expected == nil || !reflect.TypeOf(expected).Comparable() {
		return errors.New("expected handler has no comparable identity")
	}
	return m.removeHandler(ctx, tag, expected)
}

func (m *Manager) removeHandler(ctx context.Context, tag string, expected outbound.Handler) error {
	if tag == "" {
		return common.ErrNoClue
	}
	m.access.Lock()
	if m.closed {
		m.access.Unlock()
		return nil
	}
	owned := m.taggedHandler[tag]
	if owned == nil || expected != nil && owned.handler != expected {
		m.access.Unlock()
		return nil
	}
	m.tagsCache = &sync.Map{}
	delete(m.taggedHandler, tag)
	if m.defaultHandler == owned {
		m.defaultHandler = nil
	}
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

// ListHandlers implements outbound.Manager.
func (m *Manager) ListHandlers(ctx context.Context) []outbound.Handler {
	m.access.RLock()
	defer m.access.RUnlock()

	response := make([]outbound.Handler, 0, len(m.untaggedHandlers)+len(m.taggedHandler))
	for _, h := range m.untaggedHandlers {
		response = append(response, h.handler)
	}

	for _, v := range m.taggedHandler {
		response = append(response, v.handler)
	}

	return response
}

// Select implements outbound.HandlerSelector.
func (m *Manager) Select(selectors []string) []string {
	m.access.RLock()
	defer m.access.RUnlock()
	key := strings.Join(selectors, ",")
	if cache, ok := m.tagsCache.Load(key); ok {
		return cache.([]string)
	}

	tags := make([]string, 0, len(selectors))

	for tag := range m.taggedHandler {
		for _, selector := range selectors {
			if strings.HasPrefix(tag, selector) {
				tags = append(tags, tag)
				break
			}
		}
	}

	sort.Strings(tags)
	m.tagsCache.Store(key, tags)

	return tags
}

func init() {
	common.Must(common.RegisterConfig((*proxyman.OutboundConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return New(ctx, config.(*proxyman.OutboundConfig))
	}))
	common.Must(common.RegisterConfig((*core.OutboundHandlerConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return NewHandler(ctx, config.(*core.OutboundHandlerConfig))
	}))
}
