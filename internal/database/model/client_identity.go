package model

import (
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (c *ClientRecord) BeforeCreate(_ *gorm.DB) error {
	if c.StableID == "" {
		id, err := uuid.NewRandom()
		if err != nil {
			return fmt.Errorf("generate client identity: %w", err)
		}
		c.StableID = id.String()
	}
	id, err := uuid.Parse(c.StableID)
	if err != nil || id == uuid.Nil || id.String() != c.StableID {
		return fmt.Errorf("clientId must be a canonical nonzero UUID")
	}
	return nil
}
