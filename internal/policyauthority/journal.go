package policyauthority

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

const maxJournalBytes = 256 << 20
const maxRecordBytes = 16 << 10
const maxRecords = 100000

var bucketNames = []string{"metadata", "accounts", "nodes", "boots", "grants", "requests", "changes"}
var openJournal = bolt.Open

func journalOptions() *bolt.Options {
	return &bolt.Options{Timeout: 250 * time.Millisecond, MaxSize: maxJournalBytes, OpenFile: func(path string, flags int, mode os.FileMode) (*os.File, error) {
		// Create already reserved a new file exclusively. Opening either path
		// must fail if that file disappears; bbolt normally includes O_CREATE.
		return os.OpenFile(path, flags&^os.O_CREATE, mode)
	}}
}

type metadata struct {
	Schema   uint64   `json:"schema"`
	Identity Identity `json:"identity"`
	Sequence uint64   `json:"sequence"`
}

type Journal struct {
	db     *bolt.DB
	id     Identity
	closed atomic.Bool
}

func (j *Journal) Identity() Identity {
	if j == nil {
		return Identity{}
	}
	return j.id
}

func Create(path string, seeds []Seed) (*Journal, Identity, error) {
	if len(seeds) > maxRecords {
		return nil, Identity{}, ErrRequest
	}
	seen := make(map[string]bool, len(seeds))
	for _, seed := range seeds {
		if !validSeed(seed) || seen[seed.ClientID] {
			return nil, Identity{}, ErrRequest
		}
		seen[seed.ClientID] = true
	}
	if err := privatePath(path, false); err != nil {
		return nil, Identity{}, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, Identity{}, fmt.Errorf("%w: create: %v", ErrJournal, err)
	}
	if err := f.Close(); err != nil {
		return nil, Identity{}, err
	}
	db, err := openJournal(path, 0o600, journalOptions())
	if err != nil {
		return nil, Identity{}, fmt.Errorf("%w: open: %v", ErrJournal, err)
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		_ = db.Close()
		return nil, Identity{}, err
	}
	id := Identity{AuthorityID: hex.EncodeToString(random), Generation: 1}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range bucketNames {
			if _, err := tx.CreateBucket([]byte(name)); err != nil {
				return err
			}
		}
		if err := put(tx, "metadata", "state", metadata{Schema: 2, Identity: id}); err != nil {
			return err
		}
		for _, seed := range seeds {
			if err := put(tx, "accounts", seed.ClientID, initialAccount(seed)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, Identity{}, fmt.Errorf("%w: initialize: %v", ErrJournal, err)
	}
	// A failed initialization is retained for explicit recovery, never recreated.
	directory, err := os.Open(filepath.Dir(path))
	if err == nil {
		err = directory.Sync()
		_ = directory.Close()
	}
	if err != nil {
		_ = db.Close()
		return nil, Identity{}, fmt.Errorf("%w: persist directory: %v", ErrJournal, err)
	}
	return &Journal{db: db, id: id}, id, nil
}

func Open(path string, expected Identity) (*Journal, error) {
	if !key(expected.AuthorityID) || expected.Generation == 0 || !bounded(expected.Generation) {
		return nil, ErrIdentity
	}
	if err := privatePath(path, true); err != nil {
		return nil, err
	}
	db, err := openJournal(path, 0o600, journalOptions())
	if err != nil {
		return nil, fmt.Errorf("%w: open: %v", ErrJournal, err)
	}
	j := &Journal{db: db, id: expected}
	if err := db.View(j.validate); err != nil {
		_ = db.Close()
		return nil, err
	}
	return j, nil
}

func privatePath(path string, existing bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrJournal
	}
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dir.IsDir() || dir.Mode().Perm()&0o077 != 0 {
		return ErrJournal
	}
	if existing {
		file, err := os.Lstat(path)
		if err != nil || !file.Mode().IsRegular() || file.Mode().Perm()&0o077 != 0 || file.Size() == 0 || file.Size() > maxJournalBytes {
			return ErrJournal
		}
	}
	return nil
}

func (j *Journal) Close() error {
	if j == nil || j.closed.Swap(true) {
		return nil
	}
	return j.db.Close()
}

func (j *Journal) update(fn func(*bolt.Tx) error) error {
	if j == nil || j.closed.Load() {
		return ErrJournal
	}
	return j.db.Update(func(tx *bolt.Tx) error {
		if tx.Size() > maxJournalBytes {
			return ErrJournal
		}
		return fn(tx)
	})
}

func put(tx *bolt.Tx, bucket, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maxRecordBytes {
		return ErrJournal
	}
	b := tx.Bucket([]byte(bucket))
	if b == nil {
		return ErrJournal
	}
	return b.Put([]byte(key), raw)
}

func get(tx *bolt.Tx, bucket, key string, value any) error {
	b := tx.Bucket([]byte(bucket))
	if b == nil {
		return ErrJournal
	}
	raw := b.Get([]byte(key))
	if len(raw) == 0 || len(raw) > maxRecordBytes || json.Unmarshal(raw, value) != nil {
		return ErrJournal
	}
	return nil
}

func compound(parts ...string) string { raw, _ := json.Marshal(parts); return string(raw) }

func (j *Journal) RegisterBoot(boot NodeBoot) error {
	if !validBoot(boot) {
		return ErrRequest
	}
	return j.update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("nodes"))
		history := tx.Bucket([]byte("boots"))
		if b.Get([]byte(boot.NodeID)) != nil {
			var prior NodeBoot
			if err := get(tx, "nodes", boot.NodeID, &prior); err != nil {
				return err
			}
			if prior.SourceID != boot.SourceID {
				return ErrIncarnation
			}
			if prior == boot {
				return nil
			}
		} else if b.Stats().KeyN >= maxRecords {
			return ErrJournal
		}
		historyKey := compound(boot.NodeID, boot.BootID)
		if history.Get([]byte(historyKey)) != nil {
			return ErrIncarnation
		}
		if history.Stats().KeyN >= maxRecords {
			return ErrJournal
		}
		if err := put(tx, "boots", historyKey, boot); err != nil {
			return err
		}
		// A replacement boot never releases the old boot's allocations.
		return put(tx, "nodes", boot.NodeID, boot)
	})
}

func (j *Journal) checkBinding(tx *bolt.Tx, b Binding) error {
	if b.Identity != j.id {
		return ErrIdentity
	}
	if !validBoot(b.NodeBoot) || !key(b.ClientID) || !key(b.WindowID) || b.PolicyVersion == 0 {
		return ErrRequest
	}
	var current NodeBoot
	if err := get(tx, "nodes", b.NodeBoot.NodeID, &current); err != nil || current != b.NodeBoot {
		return ErrIncarnation
	}
	return nil
}

func sum(values ...uint64) (uint64, error) {
	var total uint64
	for _, v := range values {
		if v > math.MaxInt64-total {
			return 0, ErrCapacity
		}
		total += v
	}
	return total, nil
}

func allocateDirection(limit, held, share Direction) (Direction, error) {
	if !validDirection(share) || share.Unlimited && !limit.Unlimited {
		return Direction{}, ErrCapacity
	}
	rate, err := sum(held.Rate, share.Rate)
	if err != nil {
		return Direction{}, err
	}
	burst, err := sum(held.Burst, share.Burst)
	if err != nil || !limit.Unlimited && (rate > limit.Rate || burst > limit.Burst) {
		return Direction{}, ErrCapacity
	}
	return Direction{Rate: rate, Burst: burst}, nil
}

func (j *Journal) Issue(request Request) (Grant, error) {
	var issued Grant
	if !key(request.RequestID) || !key(request.ChallengeID) || request.Capacity == 0 || !bounded(request.Capacity) || request.LeaseDuration <= 0 || request.LeaseDuration > MaxLeaseDuration {
		return issued, ErrRequest
	}
	err := j.update(func(tx *bolt.Tx) error {
		if err := j.checkBinding(tx, request.Binding); err != nil {
			return err
		}
		var account Account
		if err := get(tx, "accounts", request.Binding.ClientID, &account); err != nil {
			return err
		}
		if account.Deleted {
			return ErrDeleted
		}
		requestKey := compound(request.Binding.NodeBoot.NodeID, request.Binding.ClientID, request.RequestID)
		if raw := tx.Bucket([]byte("requests")).Get([]byte(requestKey)); raw != nil {
			if err := get(tx, "grants", string(raw), &issued); err != nil {
				return err
			}
			if issued.Request != request {
				return ErrRequest
			}
			return nil
		}
		if account.Policy.Version != request.Binding.PolicyVersion || account.Policy.WindowID != request.Binding.WindowID {
			return ErrRequest
		}
		total, err := addAmounts(amount{account.WindowUsed, account.WindowRemainder}, amount{account.FrozenBilled, 0}, amount{account.HeldCapacity, account.HeldRemainder}, amount{request.Capacity, 0})
		if err != nil || !account.Policy.QuotaUnlimited && (amount{account.Policy.QuotaBytes, 0}).less(total) {
			return ErrCapacity
		}
		if !account.Policy.Upload.Unlimited && account.UploadUnlimitedHeld > 0 || !account.Policy.Download.Unlimited && account.DownloadUnlimitedHeld > 0 {
			return ErrCapacity
		}
		if account.UploadHeld, err = allocateDirection(account.Policy.Upload, account.UploadHeld, request.Upload); err != nil {
			return err
		}
		if account.DownloadHeld, err = allocateDirection(account.Policy.Download, account.DownloadHeld, request.Download); err != nil {
			return err
		}
		if request.Upload.Unlimited {
			account.UploadUnlimitedHeld++
		}
		if request.Download.Unlimited {
			account.DownloadUnlimitedHeld++
		}
		account.HeldCapacity, err = sum(account.HeldCapacity, request.Capacity)
		if err != nil {
			return err
		}
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		if meta.Sequence >= math.MaxInt64 || tx.Bucket([]byte("grants")).Stats().KeyN >= maxRecords {
			return ErrJournal
		}
		meta.Sequence++
		if err := advanceRevision(&account); err != nil {
			return err
		}
		issued = Grant{Request: request, GrantID: fmt.Sprintf("%s:%d", j.id.AuthorityID, meta.Sequence), Sequence: meta.Sequence}
		if err := put(tx, "accounts", account.Seed.ClientID, account); err != nil {
			return err
		}
		if err := put(tx, "metadata", "state", meta); err != nil {
			return err
		}
		if err := put(tx, "grants", issued.GrantID, issued); err != nil {
			return err
		}
		return tx.Bucket([]byte("requests")).Put([]byte(requestKey), []byte(issued.GrantID))
	})
	if err != nil {
		return Grant{}, err
	}
	return issued, nil
}

func (j *Journal) Report(report Report) error {
	if !key(report.GrantID) || report.Sequence == 0 || !bounded(report.Sequence) || !validUsage(report.Usage) {
		return ErrRequest
	}
	return j.update(func(tx *bolt.Tx) error {
		if err := j.checkBinding(tx, report.Binding); err != nil {
			return err
		}
		var grant Grant
		if err := get(tx, "grants", report.GrantID, &grant); err != nil {
			return err
		}
		if report.Binding != grant.Request.Binding {
			return ErrRequest
		}
		if report.Sequence == grant.ReportSequence {
			if report.Usage == grant.Usage && report.Seal == grant.Sealed {
				return nil
			}
			return ErrRequest
		}
		if grant.Sealed || report.Sequence < grant.ReportSequence || (amount{grant.Request.Capacity, 0}).less(usageAmount(report.Usage)) {
			return ErrRequest
		}
		delta, err := usageDelta(report.Usage, grant.Usage)
		if err != nil {
			return err
		}
		var account Account
		if err := get(tx, "accounts", report.Binding.ClientID, &account); err != nil {
			return err
		}
		if account.Usage, err = addUsage(account.Usage, delta); err != nil {
			return err
		}
		window, err := addAmounts(amount{account.WindowUsed, account.WindowRemainder}, usageAmount(delta))
		if err != nil {
			return err
		}
		account.WindowUsed, account.WindowRemainder = window.whole, window.fraction
		held, err := subtractAmounts(amount{account.HeldCapacity, account.HeldRemainder}, usageAmount(delta))
		if err != nil {
			return ErrJournal
		}
		if report.Seal {
			remaining, err := subtractAmounts(amount{grant.Request.Capacity, 0}, usageAmount(report.Usage))
			if err != nil {
				return ErrJournal
			}
			if held, err = subtractAmounts(held, remaining); err != nil {
				return ErrJournal
			}
			if account.UploadHeld.Rate < grant.Request.Upload.Rate || account.UploadHeld.Burst < grant.Request.Upload.Burst || account.DownloadHeld.Rate < grant.Request.Download.Rate || account.DownloadHeld.Burst < grant.Request.Download.Burst {
				return ErrJournal
			}
			account.UploadHeld.Rate -= grant.Request.Upload.Rate
			account.UploadHeld.Burst -= grant.Request.Upload.Burst
			account.DownloadHeld.Rate -= grant.Request.Download.Rate
			account.DownloadHeld.Burst -= grant.Request.Download.Burst
			if grant.Request.Upload.Unlimited {
				if account.UploadUnlimitedHeld == 0 {
					return ErrJournal
				}
				account.UploadUnlimitedHeld--
			}
			if grant.Request.Download.Unlimited {
				if account.DownloadUnlimitedHeld == 0 {
					return ErrJournal
				}
				account.DownloadUnlimitedHeld--
			}
		}
		account.HeldCapacity, account.HeldRemainder = held.whole, held.fraction
		if err := advanceRevision(&account); err != nil {
			return err
		}
		if grant.ReportCount >= math.MaxInt64 {
			return ErrJournal
		}
		grant.ReportCount++
		// Source/grant fractions remain recorded as well as their exact sum.
		// Aggregation never applies a multiplier or truncates fractional usage.
		grant.Usage, grant.ReportSequence, grant.Sealed = report.Usage, report.Sequence, report.Seal
		if err := put(tx, "grants", grant.GrantID, grant); err != nil {
			return err
		}
		return put(tx, "accounts", account.Seed.ClientID, account)
	})
}

func (j *Journal) Account(id string) (Account, error) {
	var account Account
	if j == nil || j.closed.Load() || !key(id) {
		return account, ErrJournal
	}
	err := j.db.View(func(tx *bolt.Tx) error { return get(tx, "accounts", id, &account) })
	return account, err
}

// AddAccount is explicit provisioning, distinct from reopening a missing
// authority. A prior canonical identity, including a tombstone, is never reused.
func (j *Journal) AddAccount(seed Seed) error {
	if !validSeed(seed) {
		return ErrRequest
	}
	return j.update(func(tx *bolt.Tx) error {
		accounts := tx.Bucket([]byte("accounts"))
		if accounts.Get([]byte(seed.ClientID)) != nil {
			var prior Account
			if err := get(tx, "accounts", seed.ClientID, &prior); err != nil {
				return err
			}
			if prior.Deleted {
				return ErrDeleted
			}
			if prior.Seed == seed {
				return nil
			}
			return ErrRequest
		}
		if accounts.Stats().KeyN >= maxRecords {
			return ErrJournal
		}
		return put(tx, "accounts", seed.ClientID, initialAccount(seed))
	})
}

// Tombstone blocks future issuance without crediting outstanding grants. Late
// authenticated settlement retains old raw/billed history under its original ID.
func (j *Journal) Tombstone(clientID string) error {
	if !key(clientID) {
		return ErrRequest
	}
	return j.update(func(tx *bolt.Tx) error {
		var account Account
		if err := get(tx, "accounts", clientID, &account); err != nil {
			return err
		}
		if account.Deleted {
			return nil
		}
		if err := advanceRevision(&account); err != nil {
			return err
		}
		account.Deleted = true
		return put(tx, "accounts", clientID, account)
	})
}
