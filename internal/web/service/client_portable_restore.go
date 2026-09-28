package service

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func (s *ClientService) importPortableClient(inboundSvc *InboundService, item ClientCreatePayload) (bool, bool, error) {
	client := &item.Client
	client.Email = strings.TrimSpace(client.Email)
	if client.Email == "" {
		return false, false, fmt.Errorf("client email is required")
	}
	if traffic := item.Traffic; traffic != nil && (traffic.Up < 0 || traffic.Down < 0 || traffic.Down > math.MaxInt64-traffic.Up || traffic.ResetCount < 0 || traffic.LastOnline < 0 || traffic.LastSubFetch < 0) {
		return false, false, fmt.Errorf("invalid portable traffic")
	}
	for _, validate := range []func() error{
		func() error { return validateClientEmail(client.Email) },
		func() error { return validateClientSubID(client.SubID) },
		func() error { return validateClientRenewal(*client) },
		func() error { return validateClientResetMax(client.ResetMax) },
		func() error { return validateClientTrafficReset(client.TrafficReset, client.TrafficResetDay) },
	} {
		if err := validate(); err != nil {
			return false, false, err
		}
	}
	normalizeClientTrafficReset(client)
	if client.SubID == "" {
		client.SubID = uuid.NewString()
	}
	now := time.Now().UnixMilli()
	if client.CreatedAt == 0 {
		client.CreatedAt = now
	}
	client.UpdatedAt = now

	ids := slices.Clone(item.InboundIds)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	locks := make([]*sync.Mutex, 0, len(ids))
	for _, id := range ids {
		locks = append(locks, lockInbound(id))
	}
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].Unlock()
		}
	}()

	inbounds := make([]*model.Inbound, 0, len(ids))
	for _, id := range ids {
		inbound, err := inboundSvc.GetInbound(id)
		if err != nil {
			return false, false, err
		}
		if client.SSH != nil && (inbound.Protocol != model.SSH || inbound.NodeID != nil) {
			return false, false, ErrClientPolicyUnsupported
		}
		if err := s.fillProtocolDefaults(client, inbound); err != nil {
			return false, false, err
		}
		inbounds = append(inbounds, inbound)
	}
	adds := make([]*preparedInboundClientAdd, 0, len(ids))
	applies := make([]inboundApply, 0, len(ids))
	for _, inbound := range inbounds {
		settings, err := json.Marshal(map[string][]model.Client{"clients": {clientWithInboundFlow(*client, inbound)}})
		if err != nil {
			return false, false, err
		}
		add, err := s.prepareInboundClientAdd(inboundSvc, &model.Inbound{Id: inbound.Id, Settings: string(settings)})
		if err != nil {
			return false, false, err
		}
		if add != nil {
			adds = append(adds, add)
			applies = append(applies, inboundApply{id: inbound.Id, run: add.apply})
		}
	}

	err := runSerializedTx(func(tx *gorm.DB) error {
		var taken int64
		if err := tx.Model(&model.ClientRecord{}).Where("LOWER(email) = ?", strings.ToLower(client.Email)).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("email already in use: %s", client.Email)
		}
		if err := tx.Model(&model.ClientRecord{}).Where("sub_id = ?", client.SubID).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("subId already in use: %s", client.SubID)
		}
		if len(ids) == 0 {
			record := client.ToRecord()
			if err := tx.Create(record).Error; err != nil {
				return err
			}
			if !client.Enable {
				if err := tx.Model(record).UpdateColumn("enable", false).Error; err != nil {
					return err
				}
			}
		}
		for _, add := range adds {
			if err := add.persist(tx); err != nil {
				return err
			}
		}
		if err := s.setClientLimitHwidByEmailTx(tx, client.Email, item.LimitHwid); err != nil {
			return err
		}
		return restorePortableTrafficTx(tx, inboundSvc, item)
	})
	if err != nil {
		return false, false, err
	}
	withdrawClientTombstones(client.Email)
	needRestart, err := fanoutInboundApplies(applies)
	return true, needRestart, err
}

// Only import's freshly created, uncommitted identity may initialize this ledger.
func restorePortableTrafficTx(tx *gorm.DB, inboundSvc *InboundService, item ClientCreatePayload) error {
	if item.Traffic != nil {
		if err := applyPortableTraffic(tx, inboundSvc, item); err != nil {
			return err
		}
	}
	if item.Client.SSH != nil {
		var record model.ClientRecord
		if err := tx.Where("email = ?", item.Client.Email).First(&record).Error; err != nil {
			return err
		}
		if _, err := sshClientBinding(*record.ToClient(), record.PolicyID); err != nil {
			return err
		}
		if len(item.InboundIds) == 0 && item.Traffic == nil {
			if err := inboundSvc.AddClientStat(tx, 0, &item.Client); err != nil {
				return err
			}
		}
		if err := database.NewClientUsageLedger(tx).Ensure(tx.Statement.Context, record.PolicyID); err != nil {
			return err
		}
		if item.Traffic != nil {
			if err := tx.Model(&model.ClientUsageAccount{}).Where("policy_id = ?", record.PolicyID).
				Updates(map[string]any{"up": item.Traffic.Up, "down": item.Traffic.Down, "billed": item.Traffic.Up + item.Traffic.Down}).Error; err != nil {
				return err
			}
		}
	}
	if item.Traffic != nil {
		return adjustGroupBaselinesForRestoredTraffic(tx, []string{item.Client.Email})
	}
	return nil
}
