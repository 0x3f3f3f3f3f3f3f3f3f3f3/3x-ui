package service

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func fixedOutboundSettings(t *testing.T, tag any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"clients": []any{}, "allowedNetwork": "tcp,udp", "rewriteAddress": "127.0.0.1", "rewritePort": 9001, "outboundTag": tag})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTunnelFixedOutboundRejectsInvalidSaveAtomically(t *testing.T) {
	for _, mode := range []string{"create", "update"} {
		for _, tc := range []struct {
			name, raw string
			tag       any
		}{
			{name: "missing", tag: "removed"},
			{name: "balancer-is-not-concrete", tag: "balanced"},
			{name: "wrong-type", tag: 7},
			{name: "case-variant", raw: `{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":9001,"OutboundTag":"direct"}`},
			{name: "mixed-case", raw: `{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":9001,"outboundTag":"direct","OutboundTag":"removed"}`},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				setupPolicyLedgerDB(t)
				policyConfigTemplate(t)
				db := database.GetDB()
				template, err := (&SettingService{}).GetXrayConfigTemplate()
				if err != nil {
					t.Fatal(err)
				}
				var config map[string]any
				if err := json.Unmarshal([]byte(template), &config); err != nil {
					t.Fatal(err)
				}
				config["routing"].(map[string]any)["balancers"] = []any{map[string]any{"tag": "balanced", "selector": []string{"direct"}, "strategy": map[string]any{"type": "random"}}}
				data, err := json.Marshal(config)
				if err != nil {
					t.Fatal(err)
				}
				if err := (&SettingService{}).saveSetting("xrayTemplateConfig", string(data)); err != nil {
					t.Fatal(err)
				}
				owner := &model.ClientRecord{Email: "fixed-owner", Enable: true}
				if err := db.Create(owner).Error; err != nil {
					t.Fatal(err)
				}
				svc := &InboundService{}
				request := tunnelOwnerRequest(t, owner.StableID, map[string]any{"enable": true})
				var before model.Inbound
				if mode == "update" {
					added, _, err := svc.AddInbound(request)
					if err != nil {
						t.Fatal(err)
					}
					if err := db.First(&before, added.Id).Error; err != nil {
						t.Fatal(err)
					}
					*request = before
				}
				request.Settings = fixedOutboundSettings(t, tc.tag)
				if tc.raw != "" {
					request.Settings = tc.raw
				}
				if mode == "create" {
					_, _, err = svc.AddInbound(request)
				} else {
					_, _, err = svc.UpdateInbound(request)
				}
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "outbound") {
					t.Fatalf("invalid fixed outbound saved or rejected for unrelated reason: %v", err)
				}
				if mode == "create" {
					for _, row := range []any{&model.Inbound{}, &model.ClientInbound{}} {
						var count int64
						if err := db.Model(row).Count(&count).Error; err != nil || count != 0 {
							t.Fatalf("invalid selection leaked %T rows: %d %v", row, count, err)
						}
					}
				} else {
					var after model.Inbound
					if err := db.First(&after, before.Id).Error; err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(before, after) {
						t.Fatal("invalid selection changed saved settings")
					}
				}
				var saved model.ClientRecord
				if err := db.First(&saved, owner.Id).Error; err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(*owner, saved) {
					t.Fatal("invalid selection changed canonical client")
				}
			})
		}
	}
}

func TestTunnelFixedOutboundRequiresOwnerBeforeEnable(t *testing.T) {
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	svc := &InboundService{}
	request := &model.Inbound{Protocol: model.Tunnel, Listen: "127.0.0.1", Port: 24101, Enable: true, Settings: fixedOutboundSettings(t, "direct"), StreamSettings: `{}`}
	if _, _, err := svc.AddInbound(request); err == nil || !strings.Contains(err.Error(), "canonical owner") {
		t.Fatalf("enabled fixed outbound without canonical owner: %v", err)
	}
	request.Id, request.Enable = 0, false
	saved, _, err := svc.AddInbound(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetInboundEnable(saved.Id, true); err == nil || !strings.Contains(err.Error(), "canonical owner") {
		t.Fatalf("re-enabled fixed outbound without canonical owner: %v", err)
	}
	var after model.Inbound
	if err := database.GetDB().First(&after, saved.Id).Error; err != nil || after.Enable || after.Settings != saved.Settings {
		t.Fatalf("rejected enable changed dormant selection: %+v %v", after, err)
	}
}

func TestTunnelFixedOutboundPreservesRemovedSubscriptionSelection(t *testing.T) {
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	db := database.GetDB()
	owner := &model.ClientRecord{Email: "subscription-owner", Enable: true}
	if err := db.Create(owner).Error; err != nil {
		t.Fatal(err)
	}
	sub := &model.OutboundSubscription{Enabled: true, LastFetchedOutbounds: `[{"tag":"chosen-sub","protocol":"freedom","settings":{"finalRules":[{"action":"allow"}]}}]`}
	if err := db.Create(sub).Error; err != nil {
		t.Fatal(err)
	}
	svc := &InboundService{}
	request := tunnelOwnerRequest(t, owner.StableID, map[string]any{"enable": true})
	request.Settings = fixedOutboundSettings(t, "chosen-sub")
	saved, _, err := svc.AddInbound(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&OutboundSubscriptionService{}).Delete(sub.Id); err != nil {
		t.Fatal(err)
	}
	config, err := (&XrayService{}).getXrayConfigFromDB(false, db)
	if err != nil {
		t.Fatal(err)
	}
	var selected string
	for _, inbound := range config.InboundConfigs {
		if inbound.Tag == saved.Tag {
			var settings struct {
				OutboundTag string `json:"outboundTag"`
			}
			if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
				t.Fatal(err)
			}
			selected = settings.OutboundTag
		}
	}
	if selected != "chosen-sub" {
		t.Fatalf("removed fixed selection changed into default routing: %q", selected)
	}
	if _, err := svc.SetInboundEnable(saved.Id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetInboundEnable(saved.Id, true); err == nil || !strings.Contains(err.Error(), "outbound") {
		t.Fatalf("re-enabled missing selected outbound: %v", err)
	}
	var after model.Inbound
	if err := db.First(&after, saved.Id).Error; err != nil || after.Enable || after.Settings != saved.Settings {
		t.Fatalf("failed enable lost the saved selection: %+v %v", after, err)
	}
}
