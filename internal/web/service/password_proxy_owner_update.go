package service

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Ordinary siblings may already have applied this request's rename. Refuse
// unrelated changes or reuse of the old label before any email-based writes.
func guardClientUpdateIdentity(tx *gorm.DB, expected *model.ClientRecord, updated model.Client) (*model.ClientRecord, error) {
	var current model.ClientRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, expected.Id).Error; err != nil {
		return nil, err
	}
	if current.StableID != expected.StableID || current.Email != expected.Email && current.Email != updated.Email || current.SubID != expected.SubID && current.SubID != updated.SubID {
		return nil, fmt.Errorf("%w: canonical client changed during update", ErrManagedConfigStale)
	}
	if current.Email != expected.Email {
		var reused int64
		if err := tx.Model(&model.ClientRecord{}).Where("email = ? AND id <> ?", expected.Email, expected.Id).Count(&reused).Error; err != nil {
			return nil, err
		}
		if reused != 0 {
			return nil, fmt.Errorf("%w: previous client email was reused", ErrManagedConfigStale)
		}
	}
	for _, field := range []struct{ column, previous, value string }{{"email", expected.Email, updated.Email}, {"sub_id", expected.SubID, updated.SubID}} {
		if field.previous == field.value {
			continue
		}
		var collision int64
		if err := tx.Model(&model.ClientRecord{}).Where(field.column+" = ? AND id <> ?", field.value, current.Id).Count(&collision).Error; err != nil {
			return nil, err
		}
		if collision != 0 {
			return nil, fmt.Errorf("%w: requested canonical %s was acquired by another client", ErrManagedConfigStale, field.column)
		}
	}
	return &current, nil
}

// Claim the unique destination before traffic or membership writes. A writer
// acquiring it after the ownership check makes this transaction fail instead
// of entering the ordinary writer's compatible-email merge path.
func reserveClientUpdateEmail(tx *gorm.DB, current *model.ClientRecord, email string) error {
	if current.Email == email {
		return nil
	}
	result := tx.Model(&model.ClientRecord{}).Where("id = ? AND email = ?", current.Id, current.Email).Update("email", email)
	if result.Error != nil {
		return fmt.Errorf("%w: reserve canonical email: %w", ErrManagedConfigStale, result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrManagedConfigStale
	}
	return nil
}

// An excluded mirror changes shared metadata and its name, while keeping both
// its own wire credentials and the latest canonical credentials independent.
func updateCanonicalMirrorMetadata(tx *gorm.DB, current *model.ClientRecord, updated model.Client, inboundID int) error {
	var links int64
	if err := tx.Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", current.Id, inboundID).Count(&links).Error; err != nil {
		return err
	}
	if links != 1 {
		return ErrManagedConfigStale
	}
	incoming := updated.ToRecord()
	incoming.UUID, incoming.Password, incoming.Auth, incoming.Secret = current.UUID, current.Password, current.Auth, current.Secret
	incoming.Flow, incoming.Security, incoming.Reverse = current.Flow, current.Security, current.Reverse
	incoming.PrivateKey, incoming.PublicKey, incoming.AllowedIPs = current.PrivateKey, current.PublicKey, current.AllowedIPs
	incoming.PreSharedKey, incoming.KeepAlive, incoming.ForwardedPorts = current.PreSharedKey, current.KeepAlive, current.ForwardedPorts
	incoming.AdTag = current.AdTag
	merged := *current
	applyClientRecordMerge(&merged, incoming)
	merged.Email, merged.UpdatedAt = updated.Email, max(current.UpdatedAt, updated.UpdatedAt)
	return model.SaveClientRecord(tx, &merged)
}

func guardPasswordProxyOwnerUpdate(tx *gorm.DB, current *model.ClientRecord, policy *model.ClientPolicyOptions) error {
	failed, err := passwordProxyRemovalPreflight(tx, []*model.ClientRecord{current})
	if err != nil {
		return err
	}
	if err := failed[current.Id]; err != nil {
		return err
	}
	return guardPasswordProxyOwnerUpdateScope(tx, current, policy)
}

// Call only after the complete password graph was validated for the selected
// records. Bulk field changes validate that shared graph once per batch.
func guardPasswordProxyOwnerUpdateScope(tx *gorm.DB, current *model.ClientRecord, policy *model.ClientPolicyOptions) error {
	if err := guardClientPolicyTargets(tx, current, policy, nil, false); err != nil {
		return err
	}
	var tunnels []model.Inbound
	if err := tx.Joins("JOIN client_inbounds ci ON ci.inbound_id = inbounds.id").
		Where("ci.client_id = ? AND inbounds.protocol = ?", current.Id, model.Tunnel).Find(&tunnels).Error; err != nil {
		return err
	}
	for _, tunnel := range tunnels {
		var links []model.ClientInbound
		if err := tx.Where("inbound_id = ?", tunnel.Id).Find(&links).Error; err != nil {
			return err
		}
		if len(links) != 1 || links[0].ClientId != current.Id || current.StableID == "" {
			return fmt.Errorf("%w: Tunnel %d requires the selected canonical owner alone", ErrPasswordProxyOwner, tunnel.Id)
		}
		if err := validateStoredTunnelOwnerSettings(tx, tunnel.Id); err != nil {
			return err
		}
	}
	return nil
}

// Shared fields never enter the resource's accounts array. Persist them once
// against the current canonical row, including password-only/filter cases.
func (s *ClientService) updatePasswordProxyOwner(inbounds *InboundService, expected *model.ClientRecord, updated model.Client, limitHwid int) (bool, error) {
	err := runSerializedTx(func(tx *gorm.DB) error {
		current, err := guardClientUpdateIdentity(tx, expected, updated)
		if err != nil {
			return err
		}
		if err := guardPasswordProxyOwnerUpdate(tx, current, updated.Policy); err != nil {
			return err
		}
		if err := reserveClientUpdateEmail(tx, current, updated.Email); err != nil {
			return err
		}
		merged := *current
		applyClientRecordMerge(&merged, updated.ToRecord())
		if updated.PreSharedKey == "" {
			merged.PreSharedKey = current.PreSharedKey
		}
		if updated.KeepAlive == nil {
			merged.KeepAlive = current.KeepAlive
		}
		fields := map[string]any{
			"email": updated.Email, "sub_id": merged.SubID,
			"uuid": merged.UUID, "password": merged.Password, "auth": merged.Auth, "secret": merged.Secret,
			"flow": merged.Flow, "security": merged.Security, "reverse": updated.ToRecord().Reverse,
			"wg_private_key": merged.PrivateKey, "wg_public_key": merged.PublicKey, "wg_allowed_ips": merged.AllowedIPs,
			"wg_pre_shared_key": merged.PreSharedKey, "wg_keep_alive": merged.KeepAlive,
			"limit_ip": merged.LimitIP, "total_gb": merged.TotalGB, "expiry_time": merged.ExpiryTime,
			"tg_id": merged.TgID, "comment": merged.Comment, "enable": updated.Enable,
			"group_name": updated.Group, "ad_tag": updated.AdTag,
			"reset": merged.Reset, "reset_day": merged.ResetDay, "reset_weekday": merged.ResetWeekday, "reset_max": merged.ResetMax,
			"traffic_reset": merged.TrafficReset, "traffic_reset_day": merged.TrafficResetDay, "updated_at": updated.UpdatedAt,
		}
		if updated.Policy != nil {
			fields["policy_upload_bytes_per_second"] = updated.Policy.UploadBytesPerSecond
			fields["policy_download_bytes_per_second"] = updated.Policy.DownloadBytesPerSecond
			fields["policy_multiplier"] = updated.Policy.Multiplier
		}
		if err := inbounds.UpdateClientStat(tx, current.Email, &updated); err != nil {
			return err
		}
		if current.Email != updated.Email {
			if err := inbounds.UpdateClientIPs(tx, current.Email, updated.Email); err != nil {
				return err
			}
		}
		if current.Email != updated.Email {
			for _, table := range []any{&model.ClientGlobalTraffic{}, &model.NodeClientTraffic{}} {
				if err := tx.Model(table).Where("email = ?", current.Email).Update("email", updated.Email).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Model(&model.ClientRecord{}).Where("id = ?", current.Id).Updates(fields).Error; err != nil {
			return err
		}
		return s.setClientLimitHwidByEmailTx(tx, updated.Email, limitHwid)
	})
	if err != nil {
		return false, err
	}
	if handled, err := inbounds.reconcileManagedChange(&model.Inbound{Protocol: model.HTTP}); handled {
		return err != nil, err
	}
	return true, nil
}
