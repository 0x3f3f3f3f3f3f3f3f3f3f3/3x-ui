package model

import "github.com/xtls/xray-core/app/clientpolicy"

type ClientPolicyScope string

const (
	ClientPolicyScopeNode   ClientPolicyScope = "node"
	ClientPolicyScopeGlobal ClientPolicyScope = "global"
)

type ClientPolicyOptions struct {
	UploadBytesPerSecond   int64              `json:"uploadBytesPerSecond"`
	DownloadBytesPerSecond int64              `json:"downloadBytesPerSecond"`
	Multiplier             string             `json:"multiplier"`
	Scope                  *ClientPolicyScope `json:"scope,omitempty" gorm:"-" validate:"omitempty,oneof=node global"`
}

func (p *ClientPolicyOptions) EffectiveScope() ClientPolicyScope {
	if p == nil || p.Scope == nil {
		return ClientPolicyScopeNode
	}
	return *p.Scope
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
	if scope := p.EffectiveScope(); scope != ClientPolicyScopeNode && scope != ClientPolicyScopeGlobal {
		return clientpolicy.ErrInvalidPolicy
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
	if p.Scope != nil {
		scope := *p.Scope
		copy.Scope = &scope
	}
	return &copy
}

// Older clients omit scope even when they send the other policy fields.
func MergeClientPolicyOptions(current, incoming *ClientPolicyOptions) *ClientPolicyOptions {
	if incoming == nil {
		return current.Clone()
	}
	merged := incoming.Clone()
	if merged.Scope == nil && current != nil && current.Scope != nil {
		scope := *current.Scope
		merged.Scope = &scope
	}
	return merged
}
