package clientpolicy

import (
	"errors"
	"math"
	"strings"
	"time"
)

var (
	ErrEngineNotStarted = errors.New("client policy engine not started")
	ErrUnknownClient    = errors.New("unknown managed client")
	ErrPolicyVersion    = errors.New("stale or conflicting client policy version")
	ErrRestricted       = errors.New("client traffic restricted")
	ErrRevoked          = errors.New("client identity revoked")
	ErrSessionClosed    = errors.New("client session closed")
	ErrEngineClosed     = errors.New("client policy engine closed")
	ErrQueueFull        = errors.New("client admission queue full")
	ErrPacketTooLarge   = errors.New("payload exceeds client burst")
)

type Policy struct {
	ClientID               string `json:"clientId"`
	Version                uint64 `json:"version"`
	Enabled                bool   `json:"enabled"`
	Multiplier             uint64 `json:"multiplierMicros"`
	QuotaBytes             uint64 `json:"quotaBytes"`
	UploadRate             uint64 `json:"uploadBytesPerSecond"`
	DownloadRate           uint64 `json:"downloadBytesPerSecond"`
	BurstBytes             uint64 `json:"burstBytes"`
	ExpiresAt              int64  `json:"expiresAt"`
	QuotaBaselineBytes     uint64 `json:"quotaBaselineBytes,omitempty"`
	QuotaBaselineRemainder uint64 `json:"quotaBaselineRemainder,omitempty"`
}

func (p Policy) Validate() error {
	if p.ClientID == "" || len(p.ClientID) > 128 || strings.TrimSpace(p.ClientID) != p.ClientID || strings.ContainsAny(p.ClientID, "\x00\r\n") || p.Version == 0 {
		return ErrInvalidPolicy
	}
	if p.Multiplier == 0 || p.Multiplier > MaxMultiplier || p.UploadRate > 1<<40 || p.DownloadRate > 1<<40 || p.BurstBytes == 0 || p.BurstBytes > 1<<20 || p.ExpiresAt == math.MinInt64 || p.QuotaBaselineRemainder >= MultiplierScale {
		return ErrInvalidPolicy
	}
	return nil
}

type Reason uint8

const (
	ReasonDisabled Reason = 1 << iota
	ReasonExpired
	ReasonQuota
	ReasonRevoked
	ReasonStorage
	ReasonAuthority
)

func (p Policy) reasons(u Usage, revoked bool, now time.Time) Reason {
	var r Reason
	if !p.Enabled {
		r |= ReasonDisabled
	}
	if p.ExpiresAt > 0 && now.UnixMilli() >= p.ExpiresAt {
		r |= ReasonExpired
	}
	if p.QuotaBytes != 0 {
		next, err := charge(Usage{BilledBytes: u.BilledBytes, Remainder: u.Remainder}, Upload, 1, p.Multiplier)
		if err != nil || p.exceedsQuota(next, 0) {
			r |= ReasonQuota
		}
	}
	if revoked {
		r |= ReasonRevoked
	}
	return r
}

type Metadata struct {
	ClientID             string `json:"clientId"`
	InboundTag           string `json:"inboundTag"`
	AuthenticatedAccount string `json:"authenticatedAccount"`
	OriginalTarget       string `json:"originalTarget"`
	ActualTarget         string `json:"actualTarget"`
	SessionID            uint64 `json:"sessionId"`
}

type Snapshot struct {
	AuthorityGrantHistory bool   `json:"authorityGrantHistory,omitempty"`
	FirstUsedAt           int64  `json:"firstUsedAt,omitempty"`
	InstanceID            string `json:"instanceId"`
	Epoch                 uint64 `json:"epoch"`
	Sequence              uint64 `json:"sequence"`
	UncertainBytes        uint64 `json:"uncertainBytes"`
	Usage                 Usage  `json:"usage"`
	PolicyVersion         uint64 `json:"policyVersion"`
	Reasons               Reason `json:"reasons"`
	ActiveSessions        int    `json:"activeSessions"`
}
