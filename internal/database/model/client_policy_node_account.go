package model

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// This retained identity is separate from ordinary clients and allocation authority.
// Replacing a node's original source must not create another allowance.
type ClientPolicyNodeAccount struct {
	ClientID             string `json:"clientId" gorm:"primaryKey;size:36;<-:create"`
	ParentClientID       string `json:"parentClientId" gorm:"not null;size:36;<-:create;uniqueIndex:idx_policy_account_node,priority:1;uniqueIndex:idx_policy_account_source,priority:1"`
	NodeID               string `json:"nodeId" gorm:"not null;size:128;<-:create;uniqueIndex:idx_policy_account_node,priority:2"`
	SourceID             string `json:"sourceId" gorm:"not null;size:128;<-:create;uniqueIndex:idx_policy_account_source,priority:2"`
	DesiredPolicyVersion int64  `json:"desiredPolicyVersion,string" gorm:"not null;default:0;<-:create"`
	PolicyFingerprint    string `json:"-" gorm:"not null;default:'';<-:create"`
}

func (a *ClientPolicyNodeAccount) BeforeCreate(_ *gorm.DB) error {
	if a.ClientID == "" {
		id, err := uuid.NewRandom()
		if err != nil {
			return err
		}
		a.ClientID = id.String()
	}
	for _, value := range []string{a.ClientID, a.ParentClientID} {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || id.String() != value {
			return fmt.Errorf("policy account requires canonical nonzero UUIDs")
		}
	}
	for _, value := range []string{a.NodeID, a.SourceID} {
		if len(value) == 0 || len(value) > 128 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("policy account requires bounded original node/source identities")
		}
	}
	if a.ClientID == a.ParentClientID || a.DesiredPolicyVersion < 0 || (a.DesiredPolicyVersion == 0) != (a.PolicyFingerprint == "") {
		return fmt.Errorf("policy account identity/version is inconsistent")
	}
	return nil
}
