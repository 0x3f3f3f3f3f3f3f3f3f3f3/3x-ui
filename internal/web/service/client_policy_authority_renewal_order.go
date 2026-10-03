package service

import (
	"context"
	"errors"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"gorm.io/gorm"
)

type authorityRenewalOperationOrder struct {
	Index     int
	RequestID string
	ResetAt   int64
	CaptureAt int64
}

// Retain only ordering scalars. Payloads are validated and released one at a
// time; hash-key pages cannot determine the order of original renewal effects.
func orderedAuthorityRenewals(ctx context.Context, journal *policyauthority.Journal, source string) ([]authorityRenewalOperationOrder, error) {
	var operations []authorityRenewalOperationOrder
	positions := make(map[string][]authorityRenewalEffectPosition)
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := journal.ResetOperationPage(after, 128)
		if err != nil {
			return nil, err
		}
		for _, header := range page {
			after = header.RequestID
			if !strings.HasPrefix(header.RequestID, authorityRenewalPrefix) {
				continue
			}
			capture, err := journal.LookupResetOperation(header.RequestID)
			if err != nil {
				return nil, err
			}
			original, err := decodeAuthorityRenewalCapture(capture, journal, source)
			if err != nil {
				return nil, err
			}
			operation := authorityRenewalOperationOrder{Index: len(operations), RequestID: header.RequestID, CaptureAt: original.At}
			prepared, err := journal.LookupResetPreparation(header.RequestID)
			if err == nil {
				snapshot, err := decodeAuthorityRenewalPreparation(prepared, capture, journal, source)
				if err != nil {
					return nil, err
				}
				operation.ResetAt = snapshot.ResetAt
				for _, effect := range snapshot.Effects {
					positions[effect.ClientID] = append(positions[effect.ClientID], authorityRenewalEffectPosition{Operation: operation.Index, BeforeCount: effect.BeforeResetCount, AfterCount: effect.AfterResetCount})
				}
			} else if !errors.Is(err, policyauthority.ErrNotFound) {
				return nil, err
			}
			operations = append(operations, operation)
		}
		if len(page) < 128 {
			break
		}
	}
	return sortAuthorityRenewalOperations(ctx, operations, positions)
}

func recoverAuthorityRenewalPreparations(ctx context.Context, expected *gorm.DB, journal *policyauthority.Journal, source string) error {
	operations, err := orderedAuthorityRenewals(ctx, journal, source)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		if operation.ResetAt == 0 {
			continue
		}
		capture, err := journal.LookupResetOperation(operation.RequestID)
		if err != nil {
			return err
		}
		if err := runSerializedTxContextForDatabase(ctx, expected, func(tx *gorm.DB) error {
			return recoverAuthorityPreparedRenewalTx(tx, journal, source, capture)
		}); err != nil {
			return err
		}
	}
	return nil
}
