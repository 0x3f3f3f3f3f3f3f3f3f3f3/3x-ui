package model

import "github.com/xtls/xray-core/app/clientpolicy"

type ClientPolicyOptions struct {
	UploadBytesPerSecond   int64  `json:"uploadBytesPerSecond"`
	DownloadBytesPerSecond int64  `json:"downloadBytesPerSecond"`
	Multiplier             string `json:"multiplier"`
}

func (p *ClientPolicyOptions) MultiplierMicros() (uint64, error) {
	if p == nil || p.Multiplier == "" {
		return clientpolicy.MultiplierScale, nil
	}
	return clientpolicy.ParseMultiplier(p.Multiplier)
}

func (p *ClientPolicyOptions) Validate() error {
	if p == nil {
		return nil
	}
	if p.UploadBytesPerSecond < 0 || p.DownloadBytesPerSecond < 0 || p.UploadBytesPerSecond > 1<<40 || p.DownloadBytesPerSecond > 1<<40 {
		return clientpolicy.ErrInvalidPolicy
	}
	_, err := p.MultiplierMicros()
	return err
}

func (p *ClientPolicyOptions) Clone() *ClientPolicyOptions {
	if p == nil {
		return nil
	}
	copy := *p
	return &copy
}
