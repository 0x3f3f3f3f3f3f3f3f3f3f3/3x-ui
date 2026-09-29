package service

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"gorm.io/gorm"
)

func checkSSHUpstreamPortConflict(inbound *model.Inbound, bits transportBits) (*portConflictDetail, error) {
	port, err := sshOutboundBridgePort()
	if err != nil {
		return nil, err
	}
	if inbound.Port != port || bits&transportTCP == 0 || !listenOverlaps(loopbackBind, inboundBindAddr(inbound)) {
		return nil, nil
	}
	return &portConflictDetail{Tag: "ssh-upstream", Listen: "127.0.0.1", Port: port, Relay: true, Transports: transportTCP}, nil
}

func templateListenerReservations(raw string) ([]*model.Inbound, error) {
	var config xray.Config
	if err := json.Unmarshal([]byte(UnwrapXrayTemplateConfig(raw)), &config); err != nil {
		return nil, err
	}
	var listeners []*model.Inbound
	hasAPI := false
	for _, in := range config.InboundConfigs {
		if in.Port < 1 || in.Port > 65535 {
			continue
		}
		var listen string
		if len(in.Listen) != 0 {
			if err := json.Unmarshal(in.Listen, &listen); err != nil {
				return nil, fmt.Errorf("invalid template listener %q: %w", in.Tag, err)
			}
		}
		protocol := model.Protocol(in.Protocol)
		if in.Protocol == "socks" {
			protocol = model.Mixed
		}
		listeners = append(listeners, &model.Inbound{
			Tag:            in.Tag,
			Listen:         listen,
			Port:           in.Port,
			Protocol:       protocol,
			Settings:       string(in.Settings),
			StreamSettings: string(in.StreamSettings),
		})
		hasAPI = hasAPI || in.Tag == "api"
	}
	if !hasAPI {
		listeners = append(listeners, &model.Inbound{Tag: "api", Listen: "127.0.0.1", Port: defaultXrayAPIPort})
	}
	var metrics struct {
		Tag    string `json:"tag"`
		Listen string `json:"listen"`
	}
	if len(config.Metrics) != 0 {
		if err := json.Unmarshal(config.Metrics, &metrics); err != nil {
			return nil, fmt.Errorf("invalid metrics listener: %w", err)
		}
	}
	if metrics.Listen != "" {
		host, portString, err := net.SplitHostPort(metrics.Listen)
		if err != nil {
			return nil, fmt.Errorf("invalid metrics listener: %w", err)
		}
		port, err := strconv.Atoi(portString)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid metrics listener port %q", portString)
		}
		if metrics.Tag == "" {
			metrics.Tag = "metrics"
		}
		listeners = append(listeners, &model.Inbound{Tag: metrics.Tag, Listen: host, Port: port})
	}
	return listeners, nil
}

func templateListenerReservationsTx(db *gorm.DB) ([]*model.Inbound, error) {
	var setting model.Setting
	err := db.Where("key = ?", "xrayTemplateConfig").First(&setting).Error
	if database.IsNotFound(err) {
		setting.Value = defaultValueMap["xrayTemplateConfig"]
	} else if err != nil {
		return nil, err
	}
	listeners, err := templateListenerReservations(setting.Value)
	if err != nil {
		// Preserve the API reservation when recovering a malformed stored template.
		listeners = []*model.Inbound{{Tag: "api", Listen: "127.0.0.1", Port: defaultXrayAPIPort}}
	}
	return listeners, nil
}

func checkTemplatePortConflictTx(db *gorm.DB, inbound *model.Inbound) (*portConflictDetail, error) {
	listeners, err := templateListenerReservationsTx(db)
	if err != nil {
		return nil, err
	}
	for _, listener := range listeners {
		if listener.Port != inbound.Port || !listenOverlaps(inboundBindAddr(listener), inboundBindAddr(inbound)) {
			continue
		}
		shared := inboundTransports(listener.Protocol, listener.StreamSettings, listener.Settings) &
			inboundTransports(inbound.Protocol, inbound.StreamSettings, inbound.Settings)
		if shared != 0 {
			return &portConflictDetail{Tag: listener.Tag, Listen: listener.Listen, Port: listener.Port, Transports: shared}, nil
		}
	}
	return nil, nil
}

func checkTemplateReservationsTx(db *gorm.DB, raw string) error {
	if err := lockListenerReservationsTx(db); err != nil {
		return err
	}
	listeners, err := templateListenerReservations(raw)
	if err != nil {
		return err
	}
	for _, listener := range listeners {
		conflict, err := checkListenerReservationsTx(db, listener, 0, false)
		if err != nil {
			return err
		}
		if conflict != nil {
			return fmt.Errorf("template listener %q conflicts: %s", listener.Tag, conflict)
		}
	}
	return nil
}
