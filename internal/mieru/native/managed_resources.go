package protocol

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/enfein/mieru/v3/pkg/common"
	"github.com/enfein/mieru/v3/pkg/protocol/serveruser"
)

type ServerLimits struct {
	Sessions           int
	SessionsPerUser    int
	QueueSegments      int
	QueueBytes         int
	DisableUserMetrics bool
}

type ServerResourceStats struct {
	Sessions          int
	PeakSessions      int
	Rejected          uint64
	BufferedBytes     int64
	PeakBufferedBytes int64
}

type serverResources struct {
	limits    ServerLimits
	mu        sync.Mutex
	sessions  int
	peak      int
	rejected  uint64
	closed    bool
	users     map[string]int
	bytes     atomic.Int64
	peakBytes atomic.Int64
	workers   sync.WaitGroup
}

type sessionResource struct {
	owner        *serverResources
	user         string
	deliveryMu   sync.Mutex
	released     bool
	receiveBytes atomic.Int64
	underlay     *baseUnderlay
}

func (m *Mux) SetServerLimits(limits ServerLimits) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.isClient || m.used || limits.Sessions < 1 || limits.Sessions > 1024 || limits.SessionsPerUser < 1 || limits.SessionsPerUser > limits.Sessions || limits.QueueSegments < 64 || limits.QueueSegments > 4096 || limits.QueueBytes < 65535 || limits.QueueBytes > 16<<20 {
		return errors.New("invalid managed mieru server resource limits")
	}
	m.serverResources = &serverResources{limits: limits, users: make(map[string]int)}
	return nil
}

func (m *Mux) ServerResourceStats() ServerResourceStats {
	m.mu.Lock()
	r := m.serverResources
	m.mu.Unlock()
	if r == nil {
		return ServerResourceStats{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return ServerResourceStats{Sessions: r.sessions, PeakSessions: r.peak, Rejected: r.rejected, BufferedBytes: r.bytes.Load(), PeakBufferedBytes: r.peakBytes.Load()}
}

func (r *serverResources) acquire(policy serveruser.Policy) *sessionResource {
	r.mu.Lock()
	defer r.mu.Unlock()
	user := policy.Name()
	if r.closed || user == "" || r.sessions >= r.limits.Sessions || r.users[user] >= r.limits.SessionsPerUser || (r.limits.DisableUserMetrics && len(policy.Quotas()) != 0) {
		r.rejected++
		return nil
	}
	r.sessions++
	r.peak = max(r.peak, r.sessions)
	r.users[user]++
	r.workers.Add(1)
	return &sessionResource{owner: r, user: user}
}

func (r *serverResources) adjustBytes(delta int) {
	if r == nil {
		return
	}
	current := r.bytes.Add(int64(delta))
	for peak := r.peakBytes.Load(); current > peak; peak = r.peakBytes.Load() {
		if r.peakBytes.CompareAndSwap(peak, current) {
			break
		}
	}
}

func (m *Mux) newServerUnderlay(mtu int, pattern *appctlpb.TrafficPattern) *baseUnderlay {
	b := newBaseUnderlay(false, mtu, pattern)
	b.serverResources = m.serverResources
	if b.serverResources != nil {
		b.pendingSessions = make(chan struct{}, sessionChanCapacity)
	}
	return b
}

func (b *baseUnderlay) newServerSession(id uint32, policy serveruser.Policy) *Session {
	if b.serverResources == nil {
		return newSessionWithServerUserPolicy(id, false, b.MTU(), policy, nil, b.trafficPattern)
	}
	select {
	case b.pendingSessions <- struct{}{}:
	default:
		b.serverResources.mu.Lock()
		b.serverResources.rejected++
		b.serverResources.mu.Unlock()
		return nil
	}
	lease := b.serverResources.acquire(policy)
	if lease == nil {
		<-b.pendingSessions
		return nil
	}
	return newSessionWithResource(id, false, b.MTU(), policy, nil, b.trafficPattern, lease)
}

func (b *baseUnderlay) discardServerSession(s *Session) {
	if s.resource != nil {
		<-b.pendingSessions
		s.releaseManagedResources()
	}
}

func (s *Session) releaseManagedResources() {
	r := s.resource
	if r == nil {
		return
	}
	r.deliveryMu.Lock()
	defer r.deliveryMu.Unlock()
	if r.released {
		return
	}
	r.released = true
	s.oLock.Lock()
	s.sendQueue.DeleteAll()
	s.sendBuf.DeleteAll()
	s.oLock.Unlock()
	s.recvQueue.DeleteAll()
	s.recvBuf.DeleteAll()
	s.rLock.Lock()
	s.unreadBuf = nil
	s.rLock.Unlock()
	for {
		select {
		case seg := <-s.recvChan:
			r.releaseReceived(len(seg.payload))
		default:
			if r.underlay != nil {
				r.underlay.sessionMap.CompareAndDelete(s.id, s)
			}
			r.owner.mu.Lock()
			r.owner.sessions--
			r.owner.users[r.user]--
			if r.owner.users[r.user] == 0 {
				delete(r.owner.users, r.user)
			}
			r.owner.mu.Unlock()
			r.owner.workers.Done()
			return
		}
	}
}

func (s *Session) queueManagedStreamSegment(seg *segment) {
	for {
		select {
		case <-s.closedChan:
			return
		default:
		}
		if s.recvQueue.Insert(seg) {
			return
		}
		select {
		case <-s.closedChan:
			return
		case <-time.After(backPressureDelay):
		}
	}
}

func (b *baseUnderlay) deliverManagedSegment(s *Session, seg *segment) bool {
	r := s.resource
	r.deliveryMu.Lock()
	defer r.deliveryMu.Unlock()
	if r.released || s.closeRequested.Load() {
		return false
	}
	for !r.reserveReceived(len(seg.payload)) {
		if s.transportProtocol == common.PacketTransport {
			return true
		}
		select {
		case <-s.closedChan:
			return false
		case <-time.After(backPressureDelay):
		}
	}
	if s.transportProtocol == common.PacketTransport {
		select {
		case s.recvChan <- seg:
		default:
			r.releaseReceived(len(seg.payload))
		}
		return true
	}
	select {
	case s.recvChan <- seg:
		return true
	case <-s.closedChan:
		r.releaseReceived(len(seg.payload))
		return false
	case <-b.done:
		r.releaseReceived(len(seg.payload))
		return false
	}
}

func (r *sessionResource) reserveReceived(n int) bool {
	for used := r.receiveBytes.Load(); ; used = r.receiveBytes.Load() {
		if used+int64(n) > int64(r.owner.limits.QueueBytes) {
			return false
		}
		if r.receiveBytes.CompareAndSwap(used, used+int64(n)) {
			r.owner.adjustBytes(n)
			return true
		}
	}
}

func (r *sessionResource) releaseReceived(n int) {
	r.receiveBytes.Add(-int64(n))
	r.owner.adjustBytes(-n)
}

func (t *segmentTree) canFit(segments, payloadBytes int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tr.Len()+segments <= t.cap && (t.byteLimit == 0 || t.bufferedBytes+payloadBytes <= t.byteLimit)
}

func (s *Session) queueManagedPacketSegment(seg *segment) error {
	s.requestPacketAck()
	seq, _ := seg.Seq()
	next := s.nextRecv.Load()
	if seq < next {
		return nil
	}
	if seq == next && s.recvQueue.Insert(seg) {
		s.nextRecv.Add(1)
	} else if !s.recvBuf.Insert(seg) {
		return nil
	}
	if err := s.moveRecvBufToRecvQueue(); err != nil {
		return err
	}
	s.requestPacketAck()
	return nil
}

func drainManagedQueue[T any](queue <-chan T) {
	for {
		select {
		case <-queue:
		default:
			return
		}
	}
}
