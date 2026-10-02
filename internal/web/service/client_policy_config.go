package service

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var ErrManagedConfigStale = errors.New("managed configuration changed during compilation")

type compiledManagedConfig struct {
	database *gorm.DB
	config   *xray.Config
	state    conf.ClientPolicyConfig
	ids      []string
	records  map[string]model.ClientRecord
	bindings map[string][]model.ClientRecord
}

// A candidate binds database identities before negotiated activation; it does not migrate legacy usage.
func (s *XrayService) GetManagedXrayConfig(state *conf.ClientPolicyConfig) (*xray.Config, error) {
	compiled, err := s.compileManagedXrayConfig(state)
	if err != nil {
		return nil, err
	}
	return compiled.prepare()
}

func (s *XrayService) compileManagedXrayConfig(state *conf.ClientPolicyConfig) (*compiledManagedConfig, error) {
	if state == nil || !filepath.IsAbs(state.StateFile) {
		return nil, clientpolicy.ErrInvalidPolicy
	}
	policyState := *state
	policyState.Policies = nil
	if _, err := policyState.Build(); err != nil {
		return nil, err
	}
	var cfg *xray.Config
	var bindings map[string][]model.ClientRecord
	var records map[string]model.ClientRecord
	sourceDB := database.GetDB()
	err := readManagedConfigSnapshot(sourceDB, func(tx *gorm.DB) error {
		var err error
		cfg, err = s.getXrayConfigFromDB(true, tx)
		if err != nil {
			return err
		}
		bindings, records, err = localManagedInboundBindings(tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	api := conf.APIConfig{}
	if len(cfg.API) != 0 && string(cfg.API) != "null" {
		if err := json.Unmarshal(cfg.API, &api); err != nil {
			return nil, err
		}
	}
	legacyControlTag := api.Tag
	if api.Tag == "" {
		api.Tag = "api"
	}
	api.Listen = filepath.Join(filepath.Dir(state.StateFile), "control.sock")
	present := make(map[string]bool)
	for _, service := range api.Services {
		present[strings.ToLower(service)] = true
	}
	for _, service := range []string{"ClientPolicyServiceV1", "HandlerService", "StatsService", "RoutingService"} {
		if !present[strings.ToLower(service)] {
			api.Services = append(api.Services, service)
		}
	}
	if _, err := api.Build(); err != nil {
		return nil, err
	}
	cfg.API, err = json.Marshal(api)
	if err != nil {
		return nil, err
	}
	var inbounds []xray.InboundConfig
	seen := make(map[string]bool)
	for _, inbound := range cfg.InboundConfigs {
		owners, known := bindings[inbound.Tag]
		if legacyControlTag != "" && inbound.Tag == legacyControlTag && !known {
			if !isLegacyControlInbound(cfg, inbound, legacyControlTag) {
				return nil, fmt.Errorf("%w: legacy control tag has no verified control-only binding", xray.ErrClientPolicyCapability)
			}
			continue
		}
		if !known || seen[inbound.Tag] {
			return nil, fmt.Errorf("%w: inbound %q has no unique database resource binding", xray.ErrClientPolicyCapability, inbound.Tag)
		}
		seen[inbound.Tag] = true
		if err := bindManagedInboundIdentity(&inbound, owners); err != nil {
			return nil, err
		}
		inbounds = append(inbounds, inbound)
	}
	if len(seen) != len(bindings) {
		return nil, fmt.Errorf("%w: an enabled database listener is missing from the candidate", xray.ErrClientPolicyCapability)
	}
	cfg.InboundConfigs = inbounds
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		policy, err := desiredClientPolicy(records[id])
		if err != nil {
			return nil, err
		}
		policyState.Policies = append(policyState.Policies, policy)
	}
	cfg.ClientPolicy, err = json.Marshal(policyState)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var coreConfig conf.Config
	if err := json.Unmarshal(raw, &coreConfig); err != nil {
		return nil, err
	}
	if _, err := coreConfig.Build(); err != nil {
		return nil, fmt.Errorf("managed candidate validation: %w", err)
	}
	return &compiledManagedConfig{database: sourceDB, config: cfg, state: policyState, ids: ids, records: records, bindings: bindings}, nil
}

func (c *compiledManagedConfig) prepare() (*xray.Config, error) {
	policyState := c.state
	policyState.Policies = nil
	for _, batch := range chunkStrings(c.ids, 1000) {
		policies, err := prepareClientPoliciesForDatabase(c.database, batch, c.records)
		if err != nil {
			return nil, err
		}
		policyState.Policies = append(policyState.Policies, policies...)
	}
	raw, err := json.Marshal(policyState)
	if err != nil {
		return nil, err
	}
	cfg := *c.config
	cfg.ClientPolicy = raw
	return &cfg, nil
}

func readManagedConfigSnapshot(db *gorm.DB, read func(*gorm.DB) error) error {
	if db.Name() != "sqlite" {
		return db.Transaction(read, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	}
	// SQLite's normal BeginTx uses the DSN's immediate write lock, even for read-only TxOptions.
	return database.WithConnection(db, func(connection *gorm.DB) (err error) {
		connection = connection.Session(&gorm.Session{NewDB: true})
		if err := connection.Exec("BEGIN DEFERRED").Error; err != nil {
			return err
		}
		defer func() { err = errors.Join(err, connection.Exec("ROLLBACK").Error) }()
		return read(connection)
	})
}

func localManagedInboundBindings(tx *gorm.DB) (map[string][]model.ClientRecord, map[string]model.ClientRecord, error) {
	bindings := make(map[string][]model.ClientRecord)
	records := make(map[string]model.ClientRecord)
	err := func() error {
		var inbounds []model.Inbound
		if err := tx.Select("id", "tag", "protocol").Where("node_id IS NULL AND enable = ?", true).Find(&inbounds).Error; err != nil {
			return err
		}
		tags := make(map[int]string, len(inbounds))
		for _, inbound := range inbounds {
			switch inbound.Protocol {
			case model.Tunnel, model.VLESS, model.VMESS, model.Trojan, model.Shadowsocks, model.Mixed, model.HTTP, model.Mieru, model.SSH, model.Snell:
			default:
				return fmt.Errorf("%w: managed adapter for %s is not implemented", xray.ErrClientPolicyCapability, inbound.Protocol)
			}
			tags[inbound.Id] = inbound.Tag
			bindings[inbound.Tag] = nil
		}
		type boundRecord struct {
			model.ClientRecord
			InboundID int
		}
		var rows []boundRecord
		if err := tx.Table("client_inbounds ci").Select("c.*, ci.inbound_id").
			Joins("JOIN inbounds i ON i.id = ci.inbound_id").Joins("LEFT JOIN clients c ON c.id = ci.client_id").
			Where("i.node_id IS NULL AND i.enable = ?", true).Order("ci.inbound_id, ci.client_id").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if row.Id == 0 || row.StableID == "" {
				return fmt.Errorf("%w: listener link has no stable client record", xray.ErrClientPolicyCapability)
			}
			tag := tags[row.InboundID]
			bindings[tag] = append(bindings[tag], row.ClientRecord)
			records[row.StableID] = row.ClientRecord
		}
		if len(records) > 100000 {
			return clientpolicy.ErrInvalidPolicy
		}
		ids := make([]int, 0, len(records))
		for _, record := range records {
			ids = append(ids, record.Id)
		}
		for _, batch := range chunkInts(ids, 1000) {
			var remote int64
			if err := tx.Table("client_inbounds ci").Joins("JOIN inbounds i ON i.id = ci.inbound_id").
				Where("ci.client_id IN ? AND i.node_id IS NOT NULL", batch).Count(&remote).Error; err != nil {
				return err
			}
			if remote != 0 {
				return fmt.Errorf("%w: shared remote clients require coordinated budget and rate allocation", xray.ErrClientPolicyCapability)
			}
		}
		return nil
	}()
	return bindings, records, err
}

func bindManagedInboundIdentity(inbound *xray.InboundConfig, records []model.ClientRecord) error {
	if inbound.Protocol == string(model.Snell) {
		return bindManagedSnellIdentity(inbound, records)
	}
	if inbound.Protocol == string(model.SSH) {
		return bindManagedSSHIdentity(inbound, records)
	}
	if inbound.Protocol == string(model.Mieru) {
		return bindManagedMieruIdentity(inbound, records)
	}
	if isPasswordProxy(model.Protocol(inbound.Protocol)) {
		return bindManagedPasswordProxyIdentity(inbound, records)
	}
	var settings map[string]any
	if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
		return err
	}
	if settings == nil {
		return fmt.Errorf("%w: managed listener settings are required", xray.ErrClientPolicyCapability)
	}
	delete(settings, "clientId")
	delete(settings, "client_id")
	if inbound.Protocol == string(model.Tunnel) {
		if len(records) != 1 {
			return fmt.Errorf("%w: Tunnel %q requires exactly one stored owner", xray.ErrClientPolicyCapability, inbound.Tag)
		}
		delete(settings, "clients")
		settings["clientId"], settings["email"] = records[0].StableID, records[0].Email
	} else {
		method, _ := settings["method"].(string)
		if inbound.Protocol == string(model.Shadowsocks) && strings.HasPrefix(method, "2022-") {
			return fmt.Errorf("%w: managed Shadowsocks 2022 is not implemented", xray.ErrClientPolicyCapability)
		}
		byEmail := make(map[string]model.ClientRecord, len(records))
		for _, record := range records {
			byEmail[record.Email] = record
		}
		clients, _ := settings["clients"].([]any)
		for _, raw := range clients {
			client, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%w: invalid managed account", xray.ErrClientPolicyCapability)
			}
			email, _ := client["email"].(string)
			record, exists := byEmail[email]
			if !exists {
				return fmt.Errorf("%w: account has no stored identity", xray.ErrClientPolicyCapability)
			}
			key, credential := "id", record.UUID
			if inbound.Protocol == string(model.Trojan) || inbound.Protocol == string(model.Shadowsocks) {
				key, credential = "password", record.Password
			}
			if supplied, _ := client[key].(string); supplied != credential {
				return fmt.Errorf("%w: authenticated account changed", ErrManagedConfigStale)
			}
			if flow, _ := client["flow"].(string); strings.HasPrefix(flow, "xtls-rprx-vision") {
				return fmt.Errorf("%w: managed Vision activation is not verified", xray.ErrClientPolicyCapability)
			}
			delete(client, "client_id")
			client["clientId"] = record.StableID
		}
	}
	var err error
	inbound.Settings, err = json.Marshal(settings)
	return err
}
