package clientpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

var ErrLedgerCursor = errors.New("ledger cursor is ahead of durable state")

type LedgerRecord struct {
	FirstUsedAt    int64
	InstanceID     string
	Epoch          uint64
	Sequence       uint64
	ClientID       string
	PolicyVersion  uint64
	Usage          Usage
	UncertainBytes uint64
	ReservedBytes  uint64
	Revoked        bool
}

type Connection struct {
	Metadata
	PolicyVersion uint64
}

type Capabilities struct {
	BootID              string
	InstanceID          string
	Epoch               uint64
	ReservationRawBytes uint64
	Ready               bool
	Persistent          bool
}

func (e *Engine) Capabilities() Capabilities {
	return Capabilities{BootID: e.bootID, InstanceID: e.instanceID, Epoch: e.epoch, ReservationRawBytes: reservationRawBytes, Ready: e.ready.Load() && !e.failed.Load(), Persistent: e.store != nil}
}

func (e *Engine) GetClient(id string) (Policy, Snapshot, error) {
	c, err := e.state(id)
	if err != nil {
		return Policy{}, Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.policy, Snapshot{FirstUsedAt: c.firstUsedAt, InstanceID: e.instanceID, Epoch: e.epoch, Sequence: c.sequence, Usage: c.usage, UncertainBytes: c.uncertain, PolicyVersion: c.policy.Version, Reasons: c.reasonsLocked(time.Now()), ActiveSessions: c.activeSessionsLocked()}, nil
}

func (e *Engine) Connections(id string) ([]Connection, error) {
	c, err := e.state(id)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Connection, 0, len(c.sessions))
	for _, s := range c.sessions {
		if s.closed.Load() {
			continue
		}
		out = append(out, Connection{Metadata: s.metadata, PolicyVersion: c.policy.Version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out, nil
}

func (e *Engine) CloseConnections(id string) (int, error) {
	return e.closeConnections(id, "")
}

func (e *Engine) CloseInboundConnections(id, tag string) (int, error) {
	if tag == "" {
		return 0, ErrInvalidPolicy
	}
	return e.closeConnections(id, tag)
}

func (e *Engine) closeConnections(id, tag string) (int, error) {
	c, err := e.state(id)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	sessions := make([]*Session, 0, len(c.sessions))
	for _, session := range c.sessions {
		if tag == "" || session.metadata.InboundTag == tag {
			sessions = append(sessions, session)
		}
	}
	c.mu.Unlock()
	closeSessions(sessions)
	return len(sessions), nil
}

func (e *Engine) ReadLedger(after uint64, limit int) ([]LedgerRecord, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalidPolicy
	}
	if e.store == nil {
		return nil, ErrStorage
	}
	records, err := e.store.read(after, limit)
	if err != nil {
		if errors.Is(err, ErrLedgerCursor) {
			return nil, err
		}
		return nil, e.storageFailed(fmt.Errorf("%w: read committed ledger: %w", ErrStorage, err))
	}
	out := make([]LedgerRecord, 0, len(records))
	for _, r := range records {
		out = append(out, LedgerRecord{FirstUsedAt: r.FirstUsedAt, InstanceID: e.instanceID, Epoch: r.Epoch, Sequence: r.Sequence, ClientID: r.Policy.ClientID, PolicyVersion: r.Policy.Version, Usage: r.Usage, UncertainBytes: r.UncertainBytes, ReservedBytes: r.ReservedBytes, Revoked: r.Revoked})
	}
	return out, nil
}

func (s *boltStore) read(after uint64, limit int) ([]storedClient, error) {
	var records []storedClient
	err := s.db.View(func(tx *bolt.Tx) error {
		if after > tx.Bucket(clientsBucket).Sequence() {
			return ErrLedgerCursor
		}
		return tx.Bucket(clientsBucket).ForEach(func(_, v []byte) error {
			var r storedClient
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			if r.Sequence > after {
				records = append(records, r)
			}
			return nil
		})
	})
	sort.Slice(records, func(i, j int) bool { return records[i].Sequence < records[j].Sequence })
	if len(records) > limit {
		records = records[:limit]
	}
	return records, err
}
