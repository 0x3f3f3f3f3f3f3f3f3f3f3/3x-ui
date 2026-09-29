package outbound

import (
	"context"
	"net"
	"sync"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/transport/internet/stat"
)

type handlerTask struct{ cancel context.CancelFunc }

func (h *Handler) isClosed() bool {
	h.lifecycleAccess.Lock()
	defer h.lifecycleAccess.Unlock()
	return h.closed
}

func (h *Handler) beginTask(ctx context.Context) (context.Context, func(), error) {
	h.lifecycleAccess.Lock()
	defer h.lifecycleAccess.Unlock()
	if h.closed {
		return nil, nil, net.ErrClosed
	}
	ctx, cancel := context.WithCancel(ctx)
	task := &handlerTask{cancel: cancel}
	if h.tasks == nil {
		h.tasks = make(map[*handlerTask]struct{})
	}
	h.tasks[task] = struct{}{}
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel()
			h.lifecycleAccess.Lock()
			delete(h.tasks, task)
			h.lifecycleAccess.Unlock()
		})
	}, nil
}

func (h *Handler) trackConnection(conn stat.Connection, finish func()) (stat.Connection, error) {
	h.lifecycleAccess.Lock()
	if h.closed {
		h.lifecycleAccess.Unlock()
		_ = conn.Close()
		finish()
		return nil, net.ErrClosed
	}
	var tracked *stat.CounterConnection
	tracked = stat.NewCounterConnection(conn, h.downlinkCounter, h.uplinkCounter, func() {
		h.lifecycleAccess.Lock()
		delete(h.connections, tracked)
		h.lifecycleAccess.Unlock()
		finish()
	})
	if h.connections == nil {
		h.connections = make(map[*stat.CounterConnection]struct{})
	}
	h.connections[tracked] = struct{}{}
	h.lifecycleAccess.Unlock()
	return tracked, nil
}

func (h *Handler) closeLifetime() {
	h.lifecycleAccess.Lock()
	h.closed = true
	tasks := make([]*handlerTask, 0, len(h.tasks))
	for task := range h.tasks {
		tasks = append(tasks, task)
	}
	connections := make([]*stat.CounterConnection, 0, len(h.connections))
	for conn := range h.connections {
		connections = append(connections, conn)
	}
	h.lifecycleAccess.Unlock()
	for _, task := range tasks {
		task.cancel()
	}
	var errs []error
	for _, conn := range connections {
		errs = append(errs, conn.Close())
	}
	errs = append(errs, common.Close(h.mux), common.Close(h.xudp), common.Close(h.proxy))
	h.closeErr = errors.Combine(errs...)
}
