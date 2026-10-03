package model

import (
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (inbound *Inbound) BeforeCreate(_ *gorm.DB) error {
	if inbound.StableID == "" {
		id, err := uuid.NewRandom()
		if err != nil {
			return fmt.Errorf("generate inbound identity: %w", err)
		}
		inbound.StableID = id.String()
	}
	id, err := uuid.Parse(inbound.StableID)
	if err != nil || id == uuid.Nil || id.String() != inbound.StableID {
		return fmt.Errorf("inbound identity must be a canonical nonzero UUID")
	}
	return nil
}
