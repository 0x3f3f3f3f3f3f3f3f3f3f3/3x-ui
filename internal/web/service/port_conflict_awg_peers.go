package service

import (
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgnet"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/gorm"
)

func (ctx *portConflictContext) replaceInbound(inbound *model.Inbound) {
	if inbound.Id <= 0 || inbound.NodeID != nil {
		return
	}
	for i, existing := range ctx.inbounds {
		if existing.Id == inbound.Id {
			ctx.inbounds[i] = inbound
			return
		}
	}
	ctx.inbounds = append(ctx.inbounds, inbound)
}

// Client commits may rebase over another writer; validate the actual saved row
// while its listener reservation transaction is still held.
func (s *InboundService) checkSavedAmneziaWGForwardedPorts(tx *gorm.DB, inbound *model.Inbound) error {
	if inbound.Protocol != model.AmneziaWG {
		return nil
	}
	var saved model.Inbound
	if err := tx.First(&saved, inbound.Id).Error; err != nil {
		return err
	}
	return s.checkAmneziaWGForwardedPorts(tx, &saved)
}

func checkAWGForwardOwnershipTx(tx *gorm.DB, inbound *model.Inbound) error {
	instance, ok := amneziawg.InstanceFromInbound(inbound)
	if !ok || inbound.NodeID != nil {
		return nil
	}
	claims := amneziawgnet.ForwardedPortClaims(instance)
	if len(claims) == 0 {
		return nil
	}
	owners := make(map[int]string, len(claims))
	for _, claim := range claims {
		if owner, exists := owners[claim.Port]; exists && owner != claim.Email {
			return fmt.Errorf("amneziawg: client %q forwarded port %d is already owned by client %q on inbound #%d", claim.Email, claim.Port, owner, inbound.Id)
		}
		owners[claim.Port] = claim.Email
	}
	var rows []*model.Inbound
	if err := tx.Where("protocol = ? AND node_id IS NULL AND id <> ?", model.AmneziaWG, inbound.Id).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		other, ok := amneziawg.InstanceFromInbound(row)
		if !ok {
			continue
		}
		for _, claim := range amneziawgnet.ForwardedPortClaims(other) {
			if owner, exists := owners[claim.Port]; exists {
				return fmt.Errorf("amneziawg: client %q forwarded port %d is already owned by client %q on inbound %q (#%d)", owner, claim.Port, claim.Email, row.Tag, row.Id)
			}
		}
	}
	return nil
}
