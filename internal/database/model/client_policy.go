package model

import (
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Credentials and labels can change; the accounting incarnation belongs to this row's lifetime.
func (r *ClientRecord) BeforeCreate(_ *gorm.DB) error {
	if r.PolicyID != "" {
		if _, err := uuid.Parse(r.PolicyID); err != nil {
			return fmt.Errorf("invalid client policy identity: %w", err)
		}
		return nil
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return fmt.Errorf("create client policy identity: %w", err)
	}
	r.PolicyID = id.String()
	return nil
}
