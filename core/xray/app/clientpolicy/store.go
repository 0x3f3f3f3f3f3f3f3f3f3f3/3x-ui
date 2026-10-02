package clientpolicy

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	reservationRawBytes = 65536
	maxStoredClients    = 100000
)

var (
	ErrStorage    = errors.New("client policy storage unavailable")
	metaBucket    = []byte("metadata-v1")
	clientsBucket = []byte("clients-v1")
)

type storedClient struct {
	AuthorityGrant     *storedAuthorityGrant `json:"authorityGrant,omitempty"`
	FirstUsedAt        int64                 `json:"firstUsedAt,omitempty"`
	InitializationHash string                `json:"initializationHash,omitempty"`
	Policy             Policy                `json:"policy"`
	Usage              Usage                 `json:"usage"`
	UncertainBytes     uint64                `json:"uncertainBytes"`
	ReservedBytes      uint64                `json:"reservedBytes"`
	Revoked            bool                  `json:"revoked"`
	Sequence           uint64                `json:"sequence"`
	Epoch              uint64                `json:"epoch"`
}

type stateStore interface {
	save(storedClient) (uint64, error)
	saveBatch([]storedClient) ([]uint64, error)
	read(uint64, int) ([]storedClient, error)
	close() error
}

type boltStore struct {
	db    *bolt.DB
	epoch uint64
}

func storeOptions(create bool) *bolt.Options {
	return &bolt.Options{Timeout: 200 * time.Millisecond, MaxSize: 256 << 20, OpenFile: func(path string, flag int, mode os.FileMode) (*os.File, error) {
		if create {
			flag |= os.O_CREATE | os.O_EXCL
		} else {
			flag &^= os.O_CREATE
		}
		return os.OpenFile(path, flag, mode)
	}}
}

func validInstanceID(id string) bool {
	p := Policy{ClientID: id, Version: 1, Multiplier: MultiplierScale, BurstBytes: 1}
	return p.Validate() == nil
}

func CreateStore(path, instanceID string) error {
	if !validInstanceID(instanceID) || path == "" {
		return ErrInvalidPolicy
	}
	db, err := bolt.Open(path, 0o600, storeOptions(true))
	if err != nil {
		return fmt.Errorf("%w: initialize: %w", ErrStorage, err)
	}
	defer db.Close()
	err = db.Update(func(tx *bolt.Tx) error {
		m, err := tx.CreateBucket(metaBucket)
		if err != nil {
			return err
		}
		if _, err := tx.CreateBucket(clientsBucket); err != nil {
			return err
		}
		if err := m.Put([]byte("instance"), []byte(instanceID)); err != nil {
			return err
		}
		return putNumber(m, "epoch", 0)
	})
	if err != nil {
		return fmt.Errorf("%w: initialize: %w", ErrStorage, err)
	}
	return nil
}

func putNumber(b *bolt.Bucket, key string, n uint64) error {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], n)
	return b.Put([]byte(key), data[:])
}

func OpenPersistentEngine(path, instanceID string) (*Engine, error) {
	return openPersistentEngine(path, instanceID, "")
}

func openPersistentEngine(path, instanceID, bootID string) (*Engine, error) {
	if !validInstanceID(instanceID) {
		return nil, ErrInvalidPolicy
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open existing state: %w", ErrStorage, err)
	}
	if !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: state must be a private regular file", ErrStorage)
	}
	db, err := bolt.Open(path, 0o600, storeOptions(false))
	if err != nil {
		return nil, fmt.Errorf("%w: open: %w", ErrStorage, err)
	}
	var epoch uint64
	var records []storedClient
	err = db.Update(func(tx *bolt.Tx) error {
		meta, clients := tx.Bucket(metaBucket), tx.Bucket(clientsBucket)
		if meta == nil || clients == nil || string(meta.Get([]byte("instance"))) != instanceID {
			return errors.New("state identity or schema mismatch")
		}
		rawEpoch := meta.Get([]byte("epoch"))
		if len(rawEpoch) != 8 || binary.BigEndian.Uint64(rawEpoch) == math.MaxUint64 {
			return errors.New("invalid state epoch")
		}
		epoch = binary.BigEndian.Uint64(rawEpoch) + 1
		if err := putNumber(meta, "epoch", epoch); err != nil {
			return err
		}
		if err := clients.ForEach(func(key, value []byte) error {
			if len(records) >= maxStoredClients {
				return errors.New("state client limit exceeded")
			}
			var r storedClient
			if err := json.Unmarshal(value, &r); err != nil {
				return err
			}
			if string(key) != r.Policy.ClientID {
				return errors.New("invalid client state")
			}
			if err := validateStoredClient(r, epoch-1, clients.Sequence(), instanceID); err != nil {
				return err
			}
			records = append(records, r)
			return nil
		}); err != nil {
			return err
		}
		for i := range records {
			r := &records[i]
			if r.ReservedBytes != 0 {
				r.UncertainBytes += r.ReservedBytes
				r.ReservedBytes = 0
				r.Epoch = epoch
				seq, err := saveRecord(clients, *r)
				if err != nil {
					return err
				}
				r.Sequence = seq
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("%w: recover: %w", ErrStorage, err)
	}
	e := NewEngine()
	// Incarnation is immutable before any recovered expiry callback can run.
	e.bootID = bootID
	e.store, e.instanceID, e.epoch = &boltStore{db: db, epoch: epoch}, instanceID, epoch
	now := time.Now()
	for _, r := range records {
		c := newClientState(e)
		c.policy, c.usage, c.uncertain, c.revoked, c.sequence = r.Policy, r.Usage, r.UncertainBytes, r.Revoked, r.Sequence
		c.initializationHash, c.firstUsedAt = r.InitializationHash, r.FirstUsedAt
		c.previousGrant = r.AuthorityGrant
		c.buckets[Upload].update(c.policy.UploadRate, c.policy.BurstBytes, now)
		c.buckets[Download].update(c.policy.DownloadRate, c.policy.BurstBytes, now)
		e.clients[c.policy.ClientID] = c
		c.armExpiryLocked()
	}
	return e, nil
}

func saveRecord(b *bolt.Bucket, r storedClient) (uint64, error) {
	if b.Sequence() == math.MaxUint64 {
		return 0, ErrOverflow
	}
	seq, err := b.NextSequence()
	if err != nil {
		return 0, err
	}
	r.Sequence = seq
	data, err := json.Marshal(r)
	if err != nil {
		return 0, err
	}
	return seq, b.Put([]byte(r.Policy.ClientID), data)
}

func (s *boltStore) save(r storedClient) (uint64, error) {
	seqs, err := s.saveBatch([]storedClient{r})
	if err != nil {
		return 0, err
	}
	return seqs[0], nil
}

func (s *boltStore) saveBatch(records []storedClient) ([]uint64, error) {
	seqs := make([]uint64, len(records))
	err := s.db.Update(func(tx *bolt.Tx) error {
		for i, r := range records {
			r.Epoch = s.epoch
			seq, err := saveRecord(tx.Bucket(clientsBucket), r)
			if err != nil {
				return err
			}
			seqs[i] = seq
		}
		return nil
	})
	return seqs, err
}

func (s *boltStore) close() error { return s.db.Close() }

func (c *clientState) persistLocked(p Policy, revoked bool, reserved uint64) error {
	if c.engine.store == nil {
		return nil
	}
	seq, err := c.engine.store.save(storedClient{AuthorityGrant: c.authorityRecordLocked(reserved), FirstUsedAt: c.firstUsedAt, Policy: p, Usage: c.usage, UncertainBytes: c.uncertain, ReservedBytes: reserved, Revoked: revoked, InitializationHash: c.initializationHash})
	if err != nil {
		return fmt.Errorf("%w: commit: %w", ErrStorage, err)
	}
	c.sequence, c.reservationLeft = seq, 0
	c.checkpointDirty = reserved != 0
	return nil
}

func (c *clientState) reserveLocked(n uint64) error {
	if c.engine.store == nil || n <= c.reservationLeft {
		return nil
	}
	if n > reservationRawBytes {
		return ErrPacketTooLarge
	}
	raw := uint64(reservationRawBytes)
	affordable := func(raw uint64) bool {
		u, err := charge(c.usage, Upload, raw, c.policy.Multiplier)
		return err == nil && !c.policy.exceedsQuota(u, c.uncertain) && c.withinAuthorityGrantLocked(u) && !(u.BilledBytes == math.MaxUint64-c.uncertain && u.Remainder != 0)
	}
	if !affordable(raw) {
		lo, hi := n, raw
		for lo < hi {
			mid := lo + (hi-lo+1)/2
			if !affordable(mid) {
				hi = mid - 1
			} else {
				lo = mid
			}
		}
		raw = lo
	}
	upper, err := charge(c.usage, Upload, raw, c.policy.Multiplier)
	if err != nil {
		return err
	}
	reserved := upper.BilledBytes - c.usage.BilledBytes
	if upper.Remainder != 0 {
		if reserved == math.MaxUint64 {
			return ErrOverflow
		}
		reserved++
	}
	if c.uncertain > math.MaxUint64-reserved || c.usage.BilledBytes > math.MaxUint64-c.uncertain-reserved {
		return ErrOverflow
	}
	if c.grant != nil {
		spent, err := grantUsage(upper, c.grant.StartUsage)
		if err != nil {
			return err
		}
		c.grant.ReservedBilledBytes, c.grant.ReservedRemainder = spent.BilledBytes, spent.Remainder
	}
	if err := c.persistLocked(c.policy, c.revoked, reserved); err != nil {
		return err
	}
	c.reservationLeft = raw
	return nil
}

func (c *clientState) reasonsLocked(now time.Time) Reason {
	u := c.usage
	var r Reason
	if u.BilledBytes > math.MaxUint64-c.uncertain {
		r |= ReasonQuota
	} else {
		u.BilledBytes += c.uncertain
	}
	policy := c.policy
	expiry, err := policy.effectiveExpiry(c.firstUsedAt)
	if err != nil {
		r |= ReasonStorage
	}
	policy.ExpiresAt = expiry
	r |= policy.reasons(u, c.revoked, now)
	if c.engine.failed.Load() {
		r |= ReasonStorage
	}
	if c.engine.bootID != "" {
		if c.grant == nil || c.grant.Sealed || c.grant.Grant.BootID != c.engine.bootID || c.grant.Grant.PolicyVersion != c.policy.Version || !now.Before(c.grant.deadline) {
			r |= ReasonAuthority
		} else {
			next, err := charge(c.usage, Upload, 1, c.policy.Multiplier)
			if err != nil || !c.withinAuthorityGrantLocked(next) {
				r |= ReasonAuthority
			}
		}
	}
	return r
}

func (e *Engine) storageFailed(err error) error {
	if e.failed.CompareAndSwap(false, true) {
		e.demandMu.Lock()
		e.notifyDemandLocked()
		e.demandMu.Unlock()
		e.mu.Lock()
		clients := make([]*clientState, 0, len(e.clients))
		for _, c := range e.clients {
			clients = append(clients, c)
		}
		e.mu.Unlock()
		for _, c := range clients {
			c.mu.Lock()
			c.notifyLocked()
			sessions := c.sessionsLocked()
			c.mu.Unlock()
			closeSessions(sessions)
		}
	}
	return err
}

func (e *Engine) Checkpoint() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ErrEngineClosed
	}
	clients := make([]*clientState, 0, len(e.clients))
	for _, c := range e.clients {
		clients = append(clients, c)
	}
	e.mu.Unlock()
	for _, c := range clients {
		c.mu.Lock()
		if e.failed.Load() {
			c.mu.Unlock()
			return ErrStorage
		}
		if c.closed {
			c.mu.Unlock()
			return ErrEngineClosed
		}
		if !c.checkpointDirty {
			c.mu.Unlock()
			continue
		}
		err := c.persistLocked(c.policy, c.revoked, 0)
		c.mu.Unlock()
		if err != nil {
			return e.storageFailed(err)
		}
	}
	return nil
}
