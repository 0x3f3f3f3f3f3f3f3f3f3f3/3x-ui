package service

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service/outbound"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// XrayTrafficSettlement describes one committed local poll and its maintenance.
type XrayTrafficSettlement struct {
	Traffics        []*xray.Traffic
	ClientTraffics  []*xray.ClientTraffic
	NeedRestart     bool
	ClientsDisabled bool
}

// CollectAndSettleTraffic commits every legacy counter before advancing its cursor.
func (s *XrayService) CollectAndSettleTraffic() (*XrayTrafficSettlement, error) {
	p := currentXrayProcess()
	if p == nil || !p.IsRunning() {
		return nil, errors.New("xray is not running")
	}
	if err := maintainManagedTraffic(p); err != nil {
		return nil, err
	}
	traffics, clients, err := p.SettleTraffic(s.settleLegacyTrafficBatch)
	if err != nil {
		return nil, err
	}
	// Runtime maintenance can restart the child or enqueue SQL after settlement.
	needRestart, disabled, err := s.inboundService.AddTraffic(nil, nil)
	if err != nil {
		return nil, err
	}
	return &XrayTrafficSettlement{Traffics: traffics, ClientTraffics: clients, NeedRestart: needRestart, ClientsDisabled: disabled}, nil
}

func (s *XrayService) settleLegacyTrafficBatch(batch *xray.TrafficBatch) error {
	return s.settleLegacyTrafficBatchChecked(batch, nil)
}

func (s *XrayService) settleLegacyTrafficBatchChecked(batch *xray.TrafficBatch, check func(*gorm.DB, *xray.TrafficBatch) error) error {
	return runSerializedTx(func(tx *gorm.DB) error {
		receipt := model.LegacyTrafficReceipt{ProcessID: batch.ProcessID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&receipt, "process_id = ?", batch.ProcessID).Error; err != nil {
			return err
		}
		if receipt.Sequence == batch.Sequence && receipt.BatchID == batch.ID {
			return nil
		}
		if receipt.Sequence != batch.Sequence-1 {
			return errors.New("traffic settlement receipt is inconsistent")
		}
		if check != nil {
			if err := check(tx, batch); err != nil {
				return err
			}
		}
		traffics, clients := batch.Traffics, batch.ClientTraffics
		if err := s.inboundService.addInboundTraffic(tx, traffics); err != nil {
			return err
		}
		if err := s.inboundService.addClientTraffic(tx, clients); err != nil {
			return err
		}
		if err := (&outbound.OutboundService{}).AddTrafficTx(tx, traffics); err != nil {
			return err
		}
		receipt.Sequence, receipt.BatchID = batch.Sequence, batch.ID
		return tx.Save(&receipt).Error
	})
}
