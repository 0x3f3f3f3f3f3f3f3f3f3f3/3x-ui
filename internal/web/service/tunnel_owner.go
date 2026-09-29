package service

import (
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

var ErrTunnelOwnerConflict = errors.New("Tunnel listener can belong to only one client")

func validateTunnelOwnerLinks(tx *gorm.DB, inboundID int, clients []model.Client, detachEmails []string, prune bool) error {
	var inbound model.Inbound
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
		Where("id = ? AND protocol = ?", inboundID, model.Tunnel).Find(&inbound).Error; err != nil {
		return err
	}
	if inbound.Id == 0 {
		return nil
	}
	owners := make(map[string]bool)
	if !prune {
		var emails []string
		if err := tx.Table("clients c").Joins("JOIN client_inbounds ci ON ci.client_id = c.id").
			Where("ci.inbound_id = ?", inboundID).Pluck("c.email", &emails).Error; err != nil {
			return err
		}
		for _, email := range emails {
			owners[strings.ToLower(strings.TrimSpace(email))] = true
		}
	}
	for _, email := range detachEmails {
		delete(owners, strings.ToLower(strings.TrimSpace(email)))
	}
	for _, client := range clients {
		email := strings.ToLower(strings.TrimSpace(client.Email))
		if email != "" {
			owners[email] = true
		}
	}
	if len(owners) > 1 {
		return ErrTunnelOwnerConflict
	}
	return nil
}
