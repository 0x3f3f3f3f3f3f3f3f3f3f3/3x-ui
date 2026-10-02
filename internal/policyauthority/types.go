// Package policyauthority stores control-plane issuance independently of the
// panel SQL database and node execution snapshots. Network authentication and
// actual core enforcement are separate integration requirements.
package policyauthority

import (
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrJournal     = errors.New("invalid or unavailable issuance journal")
	ErrIdentity    = errors.New("issuance authority identity does not match")
	ErrRequest     = errors.New("invalid or conflicting issuance request")
	ErrCapacity    = errors.New("unavailable quota, rate or burst capacity")
	ErrIncarnation = errors.New("node incarnation does not match")
	ErrDeleted     = errors.New("canonical account has been deleted")
)

const MaxLeaseDuration = 10 * time.Second

type Identity struct {
	AuthorityID string `json:"authorityId"`
	Generation  uint64 `json:"generation"`
}

// Unlimited is explicit; a limited zero share denies its direction.
type Direction struct {
	Unlimited bool   `json:"unlimited"`
	Rate      uint64 `json:"rate"`
	Burst     uint64 `json:"burst"`
}

type Policy struct {
	WindowID       string    `json:"windowId"`
	Version        uint64    `json:"version"`
	QuotaUnlimited bool      `json:"quotaUnlimited"`
	QuotaBytes     uint64    `json:"quotaBytes"`
	Upload         Direction `json:"upload"`
	Download       Direction `json:"download"`
}

type Usage struct {
	RawUpload   uint64 `json:"rawUpload"`
	RawDownload uint64 `json:"rawDownload"`
	BilledBytes uint64 `json:"billedBytes"`
	Remainder   uint64 `json:"remainder"`
}

// WindowUsed is confirmed charged usage in the current quota window. Frozen
// capacity is additional uncertain allowance; neither field is a grant.
type Seed struct {
	ClientID     string `json:"clientId"`
	Policy       Policy `json:"policy"`
	Usage        Usage  `json:"usage"`
	WindowUsed   uint64 `json:"windowUsed"`
	FrozenBilled uint64 `json:"frozenBilled"`
}

type NodeBoot struct {
	NodeID   string `json:"nodeId"`
	SourceID string `json:"sourceId"`
	BootID   string `json:"bootId"`
}

type Binding struct {
	Identity      Identity `json:"identity"`
	NodeBoot      NodeBoot `json:"nodeBoot"`
	ClientID      string   `json:"clientId"`
	WindowID      string   `json:"windowId"`
	PolicyVersion uint64   `json:"policyVersion"`
}

type Request struct {
	Binding       Binding       `json:"binding"`
	RequestID     string        `json:"requestId"`
	ChallengeID   string        `json:"challengeId"`
	Capacity      uint64        `json:"capacity"`
	Upload        Direction     `json:"upload"`
	Download      Direction     `json:"download"`
	LeaseDuration time.Duration `json:"leaseDuration"`
}

type Grant struct {
	Request        Request `json:"request"`
	GrantID        string  `json:"grantId"`
	Sequence       uint64  `json:"sequence"`
	Usage          Usage   `json:"usage"`
	ReportSequence uint64  `json:"reportSequence"`
	Sealed         bool    `json:"sealed"`
}

type Report struct {
	Binding  Binding `json:"binding"`
	GrantID  string  `json:"grantId"`
	Sequence uint64  `json:"sequence"`
	Usage    Usage   `json:"usage"`
	Seal     bool    `json:"seal"`
}

type Account struct {
	Seed         Seed      `json:"seed"`
	Usage        Usage     `json:"usage"`
	WindowUsed   uint64    `json:"windowUsed"`
	HeldCapacity uint64    `json:"heldCapacity"`
	UploadHeld   Direction `json:"uploadHeld"`
	DownloadHeld Direction `json:"downloadHeld"`
	Deleted      bool      `json:"deleted"`
}

func key(s string) bool {
	return s != "" && len(s) <= 128 && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}

func bounded(values ...uint64) bool {
	for _, n := range values {
		if n > math.MaxInt64 {
			return false
		}
	}
	return true
}

func validUsage(u Usage) bool {
	return bounded(u.RawUpload, u.RawDownload, u.BilledBytes) && u.Remainder < 1000000
}

func validDirection(d Direction) bool {
	if d.Unlimited {
		return d.Rate == 0 && d.Burst == 0
	}
	return bounded(d.Rate, d.Burst) && ((d.Rate == 0 && d.Burst == 0) || (d.Rate > 0 && d.Burst > 0))
}

func validSeed(s Seed) bool {
	return key(s.ClientID) && key(s.Policy.WindowID) && s.Policy.Version > 0 && bounded(s.Policy.QuotaBytes, s.WindowUsed, s.FrozenBilled) && validUsage(s.Usage) && validDirection(s.Policy.Upload) && validDirection(s.Policy.Download) && (!s.Policy.QuotaUnlimited || s.Policy.QuotaBytes == 0)
}

func validBoot(b NodeBoot) bool { return key(b.NodeID) && key(b.SourceID) && key(b.BootID) }
