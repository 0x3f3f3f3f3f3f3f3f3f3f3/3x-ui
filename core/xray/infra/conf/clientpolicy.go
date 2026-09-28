package conf

import "github.com/xtls/xray-core/app/clientpolicy"

type ClientPolicyConfig struct {
	Policies []clientpolicy.Policy `json:"policies"`
}

func (c *ClientPolicyConfig) Build() (*clientpolicy.Config, error) {
	out := new(clientpolicy.Config)
	seen := make(map[string]bool)
	for _, p := range c.Policies {
		if err := p.Validate(); err != nil {
			return nil, err
		}
		if seen[p.ClientID] {
			return nil, clientpolicy.ErrInvalidPolicy
		}
		seen[p.ClientID] = true
		out.Policies = append(out.Policies, &clientpolicy.PolicyConfig{ClientId: p.ClientID, Version: p.Version, Enabled: p.Enabled, MultiplierMicros: p.Multiplier, QuotaBytes: p.QuotaBytes, UploadBytesPerSecond: p.UploadRate, DownloadBytesPerSecond: p.DownloadRate, BurstBytes: p.BurstBytes, ExpiresAt: p.ExpiresAt})
	}
	return out, nil
}
