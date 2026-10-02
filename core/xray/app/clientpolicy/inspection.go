package clientpolicy

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"math"
	"os"
	"path/filepath"
	"runtime"
)

type OfflineClientSnapshot struct {
	Policy                        Policy
	Usage                         Usage
	UncertainBytes, ReservedBytes uint64
	Sequence, Epoch               uint64
	FirstUsedAt                   int64
	Revoked, HasAuthority         bool
}

type OfflinePolicySnapshot struct {
	InstanceID      string
	Epoch, Sequence uint64
	Clients         []OfflineClientSnapshot
}

func InspectPolicyStore(path, instanceID string) (OfflinePolicySnapshot, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !validInstanceID(instanceID) {
		return OfflinePolicySnapshot{}, ErrStorage
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 || info.Size() == 0 || info.Size() > 256<<20 {
		return OfflinePolicySnapshot{}, ErrStorage
	}
	options := storeOptions(false)
	options.ReadOnly = true
	db, err := bolt.Open(path, 0600, options)
	if err != nil {
		return OfflinePolicySnapshot{}, fmt.Errorf("%w: inspect: %w", ErrStorage, err)
	}
	defer db.Close()
	snapshot := OfflinePolicySnapshot{InstanceID: instanceID}
	err = db.View(func(tx *bolt.Tx) error {
		meta, clients := tx.Bucket(metaBucket), tx.Bucket(clientsBucket)
		if meta == nil || clients == nil || string(meta.Get([]byte("instance"))) != instanceID || clients.Stats().KeyN > maxStoredClients {
			return ErrStorage
		}
		rawEpoch := meta.Get([]byte("epoch"))
		if len(rawEpoch) != 8 {
			return ErrStorage
		}
		snapshot.Epoch = binary.BigEndian.Uint64(rawEpoch)
		if snapshot.Epoch == math.MaxUint64 {
			return ErrStorage
		}
		snapshot.Sequence = clients.Sequence()
		sequences := make(map[uint64]bool)
		var maximum uint64
		if err := clients.ForEach(func(key, value []byte) error {
			if len(value) == 0 || len(value) > 16<<10 || len(snapshot.Clients) >= maxStoredClients {
				return ErrStorage
			}
			var r storedClient
			if json.Unmarshal(value, &r) != nil || string(key) != r.Policy.ClientID || sequences[r.Sequence] {
				return ErrStorage
			}
			if err := validateStoredClient(r, snapshot.Epoch, snapshot.Sequence, instanceID); err != nil {
				return err
			}
			sequences[r.Sequence] = true
			if r.Sequence > maximum {
				maximum = r.Sequence
			}
			snapshot.Clients = append(snapshot.Clients, OfflineClientSnapshot{Policy: r.Policy, Usage: r.Usage, UncertainBytes: r.UncertainBytes, ReservedBytes: r.ReservedBytes, Sequence: r.Sequence, Epoch: r.Epoch, FirstUsedAt: r.FirstUsedAt, Revoked: r.Revoked, HasAuthority: r.AuthorityGrant != nil})
			return nil
		}); err != nil {
			return err
		}
		if maximum != snapshot.Sequence {
			return ErrStorage
		}
		return nil
	})
	if err != nil {
		return OfflinePolicySnapshot{}, fmt.Errorf("%w: inspect state: %w", ErrStorage, err)
	}
	return snapshot, nil
}

func validateStoredClient(r storedClient, epoch, sequence uint64, instanceID string) error {
	if r.Policy.Validate() != nil || r.Usage.Remainder >= MultiplierScale || r.Epoch > epoch || r.Sequence == 0 || r.Sequence > sequence || r.UncertainBytes > math.MaxUint64-r.ReservedBytes || r.Usage.BilledBytes > math.MaxUint64-r.UncertainBytes-r.ReservedBytes {
		return ErrStorage
	}
	if _, err := r.Policy.effectiveExpiry(r.FirstUsedAt); err != nil {
		return err
	}
	if _, err := r.Policy.quotaUsage(r.Usage, r.UncertainBytes); err != nil {
		return err
	}
	return validateStoredAuthorityGrant(r, instanceID)
}
