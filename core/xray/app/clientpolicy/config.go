package clientpolicy

import (
	"context"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/features"
)

type Manager interface {
	features.Feature
	Apply(Policy) error
	Initialize(Policy, Usage) error
	Open(context.Context, Metadata, func()) (*Session, error)
	Snapshot(string) (Snapshot, error)
	Remove(string) error
	RemoveVersion(string, uint64) error
	ApplyBatch([]Policy) error
	Capabilities() Capabilities
	BeginAuthorityChallenge(string) (AuthorityChallenge, error)
	BindAuthority(string, AuthorityBinding) error
	InstallAuthorityGrant(ExecutionGrant) (ExecutionGrantState, error)
	GetAuthorityGrant(string) (ExecutionGrantState, error)
	SealAuthorityGrant(string, string) (ExecutionGrantState, error)
	PauseAuthorityGrant(string, string) (ExecutionGrantState, error)
	RenewAuthorityGrant(AuthorityGrantRenewal) (time.Time, error)
	GetClient(string) (Policy, Snapshot, error)
	Connections(string) ([]Connection, error)
	CloseConnections(string) (int, error)
	CloseInboundConnections(string, string) (int, error)
	Checkpoint() error
	ReadLedger(uint64, int) ([]LedgerRecord, error)
}

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(_ context.Context, raw interface{}) (interface{}, error) {
		config := raw.(*Config)
		bootID, err := freshAuthorityNonce()
		if err != nil {
			return nil, err
		}
		e, err := openPersistentEngine(config.StateFile, config.InstanceId, bootID)
		if err != nil {
			return nil, err
		}
		policies := make([]Policy, 0, len(config.Policies))
		for _, p := range config.Policies {
			if p == nil {
				e.Close()
				return nil, ErrInvalidPolicy
			}
			policies = append(policies, Policy{ClientID: p.ClientId, Version: p.Version, Enabled: p.Enabled, Multiplier: p.MultiplierMicros, QuotaBytes: p.QuotaBytes, UploadRate: p.UploadBytesPerSecond, DownloadRate: p.DownloadBytesPerSecond, BurstBytes: p.BurstBytes, ExpiresAt: p.ExpiresAt, QuotaBaselineBytes: p.QuotaBaselineBytes, QuotaBaselineRemainder: p.QuotaBaselineRemainder})
		}
		if err := e.applyBatch(policies, false); err != nil {
			e.Close()
			return nil, err
		}
		e.initial = policies
		e.ready.Store(false)
		return e, nil
	}))
}

func (*Engine) Type() interface{}        { return (*Manager)(nil) }
func (*Engine) StartAfterFeatures() bool { return true }
func (e *Engine) Start() error {
	e.startOnce.Do(func() {
		e.startErr = e.ApplyBatch(e.initial)
		if e.startErr == nil {
			e.ready.Store(true)
		}
	})
	return e.startErr
}
