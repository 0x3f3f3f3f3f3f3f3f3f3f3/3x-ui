package clientpolicy

import (
	"context"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/features"
)

type Manager interface {
	features.Feature
	Apply(Policy) error
	Open(context.Context, Metadata, func()) (*Session, error)
	Snapshot(string) (Snapshot, error)
	Remove(string) error
}

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(_ context.Context, raw interface{}) (interface{}, error) {
		e := NewEngine()
		for _, p := range raw.(*Config).Policies {
			if err := e.Apply(Policy{ClientID: p.ClientId, Version: p.Version, Enabled: p.Enabled, Multiplier: p.MultiplierMicros, QuotaBytes: p.QuotaBytes, UploadRate: p.UploadBytesPerSecond, DownloadRate: p.DownloadBytesPerSecond, BurstBytes: p.BurstBytes, ExpiresAt: p.ExpiresAt}); err != nil {
				e.Close()
				return nil, err
			}
		}
		return e, nil
	}))
}

func (*Engine) Type() interface{} { return (*Manager)(nil) }
func (*Engine) Start() error      { return nil }
