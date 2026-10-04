package service

import (
	"context"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"gorm.io/gorm"
)

func limitedAuthorityRatesHeld(account policyauthority.Account) bool {
	return !account.Policy.Upload.Unlimited && (account.UploadHeld.Rate != 0 || account.UploadUnlimitedHeld != 0) ||
		!account.Policy.Download.Unlimited && (account.DownloadHeld.Rate != 0 || account.DownloadUnlimitedHeld != 0)
}

// Controller operation mutex is held; no SQL writer is held across an RPC.
// Advance the disposable scan cursor only after the SQL projection acknowledges
// the journal's committed result. A failed projection safely replays this page.
func (c *authorityController) reconcileRetiredClientRatesLocked(ctx context.Context, client string) error {
	var next string
	err := runSerializedTxContextForDatabase(ctx, c.execution.db, func(tx *gorm.DB) error {
		account, err := lockedAuthorityProjectionAccount(tx, c.execution.journal, client)
		if err != nil {
			return err
		}
		if _, err := checkedAuthorityProjection(tx, c.execution.journal, account); err != nil {
			return err
		}
		next, err = c.execution.journal.ReleaseRetiredClientRatesPage(c.execution.boot.NodeID, client, c.retirementCursor[client], 128)
		if err != nil {
			return err
		}
		return projectClientPolicyAuthorityTx(tx, c.execution.journal, client)
	})
	if err != nil {
		return err
	}
	if next == "" {
		delete(c.retirementCursor, client)
	} else {
		c.retirementCursor[client] = next
	}
	return nil
}
