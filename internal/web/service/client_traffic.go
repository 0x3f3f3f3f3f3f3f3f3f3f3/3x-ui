package service

import (
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"gorm.io/gorm"
)

func (s *ClientService) ResetTrafficByEmail(inboundSvc *InboundService, email string) (bool, error) {
	if email == "" {
		return false, common.NewError("client email is required")
	}
	rec, err := s.GetRecordByEmail(nil, email)
	if err != nil {
		return false, err
	}
	inboundIds, err := s.GetInboundIdsForRecord(rec.Id)
	if err != nil {
		return false, err
	}

	needRestart := false
	managed, err := clientHasUsageAccount(database.GetDB(), email)
	if err != nil {
		return false, err
	}
	if managed {
		if err := submitTrafficWrite(func() error { return resetManagedClientTraffic(inboundIds, email) }); err != nil {
			return false, err
		}
		return false, s.propagateManagedClientReset(inboundSvc, inboundIds, email)
	}
	if len(inboundIds) == 0 {
		if rErr := inboundSvc.ResetClientTrafficByEmail(email); rErr != nil {
			return false, rErr
		}
	} else {
		applies := make([]inboundApply, 0, len(inboundIds))
		for _, ibId := range inboundIds {
			applies = append(applies, inboundApply{id: ibId, run: func() (bool, error) {
				return inboundSvc.ResetClientTraffic(ibId, email)
			}})
		}
		nr, applyErr := fanoutInboundApplies(applies)
		if applyErr != nil {
			return nr, applyErr
		}
		needRestart = nr
	}

	// Enable only once the counters are zero: a still-depleted client enabled
	// first is switched off again by the next traffic tick.
	if !rec.Enable {
		updated := rec.ToClient()
		updated.Enable = true
		nr, uErr := s.Update(inboundSvc, rec.Id, *updated, rec.LimitHwid)
		if uErr != nil {
			logger.Warning("Failed to auto-enable client during traffic reset:", uErr)
		}
		if nr {
			needRestart = true
		}
	}
	return needRestart, nil
}

func (s *ClientService) BulkResetTraffic(inboundSvc *InboundService, emails []string) (int, error) {
	if len(emails) == 0 {
		return 0, nil
	}
	cleanEmails := trimmedUniqueEmails(emails)
	if len(cleanEmails) == 0 {
		return 0, nil
	}

	recordsByEmail, err := clientRecordsByEmail(nil, cleanEmails)
	if err != nil {
		return 0, err
	}
	affected := 0
	err = submitTrafficWrite(func() error {
		db := database.GetDB()
		return db.Transaction(func(tx *gorm.DB) error {
			count, err := resetClientUsageTx(tx, cleanEmails)
			if err != nil {
				return err
			}
			affected = int(count)
			if err := clearGlobalTraffic(tx, cleanEmails...); err != nil {
				return err
			}
			for _, batch := range chunkStrings(cleanEmails, sqlInChunk) {
				if err := tx.Where("email IN ?", batch).Delete(&model.NodeClientTraffic{}).Error; err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		return 0, err
	}
	// After the zeroing, as in ResetTrafficByEmail: enabling a still-depleted
	// client first lets the next traffic tick switch it off again.
	for _, e := range cleanEmails {
		rec := recordsByEmail[e]
		if rec == nil || rec.Enable {
			continue
		}
		managed, err := clientHasUsageAccount(database.GetDB(), e)
		if err != nil {
			return affected, err
		}
		if managed {
			continue
		}
		updated := rec.ToClient()
		updated.Enable = true
		if _, uErr := s.Update(inboundSvc, rec.Id, *updated, rec.LimitHwid); uErr != nil {
			logger.Warning("Failed to auto-enable client during bulk traffic reset:", uErr)
		}
	}
	return affected, nil
}

func (s *ClientService) ResetAllClientTraffics(inboundSvc *InboundService, id int) error {
	err := submitTrafficWrite(func() error {
		return s.resetAllClientTrafficsLocked(id)
	})
	if err == nil {
		inboundSvc.resetAllMtprotoQuotas()
	}
	return err
}

func (s *ClientService) resetAllClientTrafficsLocked(id int) error {
	db := database.GetDB()
	now := time.Now().Unix() * 1000

	if err := db.Transaction(func(tx *gorm.DB) error {
		// client_traffics.inbound_id is stale: it reflects the inbound the row was
		// first inserted under and is never refreshed. Use the client_inbounds join
		// as the authoritative source for which emails belong to a given inbound.
		var resetEmails []string
		if id == -1 {
			if err := tx.Model(xray.ClientTraffic{}).Pluck("email", &resetEmails).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Table("client_inbounds ci").
				Select("c.email").
				Joins("JOIN clients c ON c.id = ci.client_id").
				Where("ci.inbound_id = ?", id).
				Pluck("c.email", &resetEmails).Error; err != nil {
				return err
			}
		}
		if len(resetEmails) == 0 {
			return nil
		}

		if _, err := resetClientUsageTx(tx, resetEmails); err != nil {
			return err
		}

		if err := clearGlobalTraffic(tx, resetEmails...); err != nil {
			return err
		}

		for _, batch := range chunkStrings(resetEmails, sqlInChunk) {
			if err := tx.Where("email IN ?", batch).Delete(&model.NodeClientTraffic{}).Error; err != nil {
				return err
			}
		}

		inboundWhereText := "id "
		if id == -1 {
			inboundWhereText += " > ?"
		} else {
			inboundWhereText += " = ?"
		}

		result := tx.Model(model.Inbound{}).
			Where(inboundWhereText, id).
			Update("last_traffic_reset_time", now)

		return result.Error
	}); err != nil {
		return err
	}
	return nil
}

func (s *ClientService) ResetAllTraffics() (bool, error) {
	var affected int64
	err := submitTrafficWrite(func() error {
		return database.GetDB().Transaction(func(tx *gorm.DB) error {
			var emails []string
			if err := tx.Model(&xray.ClientTraffic{}).Pluck("email", &emails).Error; err != nil {
				return err
			}
			var err error
			affected, err = resetClientUsageTx(tx, emails)
			if err != nil {
				return err
			}
			if err := tx.Where("1 = 1").Delete(&model.ClientGlobalTraffic{}).Error; err != nil {
				return err
			}
			return tx.Where("1 = 1").Delete(&model.NodeClientTraffic{}).Error
		})
	})
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}
