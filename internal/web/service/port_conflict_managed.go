package service

import (
	"encoding/json"
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/gorm"
)

func managedBridgePort(inbound *model.Inbound) (int, error) {
	if inbound.Protocol != model.SSH && inbound.Protocol != model.Mieru {
		return 0, nil
	}
	var settings struct {
		BridgePort int `json:"bridgePort"`
	}
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil || settings.BridgePort < 1 || settings.BridgePort > 65535 {
		return 0, fmt.Errorf("invalid managed bridge reservation on inbound #%d", inbound.Id)
	}
	return settings.BridgePort, nil
}

func inboundRoutingBridgePort(inbound *model.Inbound) (int, error) {
	if !mtprotoRoutesThroughXray(inbound) {
		return managedBridgePort(inbound)
	}
	port := parseRouteXrayPort(inbound.Settings)
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid MTProto bridge reservation on inbound #%d", inbound.Id)
	}
	return port, nil
}

func checkManagedBridgeConflict(db *gorm.DB, inbound *model.Inbound, ignoreID int, bits transportBits) (*portConflictDetail, error) {
	if bits&transportTCP == 0 || !listenOverlaps(loopbackBind, inboundBindAddr(inbound)) {
		return nil, nil
	}
	query := db.Model(&model.Inbound{}).Where("protocol IN ?", []model.Protocol{model.SSH, model.Mieru, model.MTProto})
	if ignoreID > 0 {
		query = query.Where("id != ?", ignoreID)
	}
	if inbound.NodeID == nil {
		query = query.Where("node_id IS NULL")
	} else {
		query = query.Where("node_id = ?", *inbound.NodeID)
	}
	var rows []*model.Inbound
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		port, err := inboundRoutingBridgePort(row)
		if err != nil {
			return nil, err
		}
		if port == inbound.Port {
			return &portConflictDetail{
				InboundID: row.Id, Remark: row.Remark, Tag: row.Tag,
				Listen: "127.0.0.1", Port: port, Relay: true, Transports: transportTCP,
			}, nil
		}
	}
	return nil, nil
}
