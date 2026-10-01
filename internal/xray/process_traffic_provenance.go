package xray

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// TrafficConfigProof records configuration identity without retaining credentials.
// ConfigStable can only become false during the lifetime of a child.
type TrafficConfigProof struct {
	ConfigDigest          string
	EffectiveConfigDigest string
	ConfigStable          bool
}

func canonicalTrafficConfigDigest(data []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func prepareTrafficConfigProof(logical *Config, written []byte) (*TrafficConfigProof, error) {
	data, err := json.Marshal(logical)
	if err != nil {
		return nil, err
	}
	digest, err := canonicalTrafficConfigDigest(data)
	if err != nil {
		return nil, err
	}
	effective, err := canonicalTrafficConfigDigest(written)
	if err != nil {
		return nil, err
	}
	// Managed startup writes a control-only bootstrap. Listener activation is
	// intentionally outside that written image and cannot establish legacy proof.
	legacy := len(logical.ClientPolicy) == 0 || strings.TrimSpace(string(logical.ClientPolicy)) == "null"
	return &TrafficConfigProof{ConfigDigest: digest, EffectiveConfigDigest: effective, ConfigStable: legacy}, nil
}

// NativeTrafficConfigProof returns a detached snapshot of this child's startup
// evidence. A direct command with no written configuration returns nil.
func (p *Process) NativeTrafficConfigProof() *TrafficConfigProof {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.nativeTrafficConfigProof == nil {
		return nil
	}
	copy := *p.nativeTrafficConfigProof
	return &copy
}

// Call only while holding process.mu. Never acquire trafficMu here: traffic
// collection and child start already acquire trafficMu before process.mu.
func (p *process) invalidateTrafficConfigProofLocked() {
	proof := p.nativeTrafficConfigProof
	if proof == nil || !proof.ConfigStable {
		return
	}
	data, err := json.Marshal(p.config)
	if err != nil {
		proof.ConfigStable = false
		return
	}
	digest, err := canonicalTrafficConfigDigest(data)
	if err != nil || digest != proof.ConfigDigest {
		proof.ConfigStable = false
	}
}
