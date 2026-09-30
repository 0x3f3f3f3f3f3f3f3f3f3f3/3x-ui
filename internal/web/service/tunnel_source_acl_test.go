package service

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func sourceACLSettings(t *testing.T, sources any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"allowedNetwork": "tcp,udp", "rewriteAddress": "127.0.0.1", "rewritePort": 9001, "clients": []any{}, "allowedSourceCidrs": sources})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTunnelSourceACLRejectsInvalidCreateAndUpdateAtomically(t *testing.T) {
	for _, mode := range []string{"create", "update"} {
		for _, tc := range []struct {
			name        string
			sources     any
			stream      string
			protocol    model.Protocol
			rawSettings string
		}{
			{name: "case-variant", rawSettings: `{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":9001,"AllowedSourceCidrs":["invalid"]}`},
			{name: "mixed-case-last-nonempty", rawSettings: `{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":9001,"allowedSourceCidrs":[],"AllowedSourceCidrs":["127.0.0.1/32"]}`},
			{name: "mixed-case-last-empty", rawSettings: `{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":9001,"allowedSourceCidrs":["127.0.0.1/32"],"AllowedSourceCidrs":[]}`},
			{name: "bad-prefix", sources: []string{"127.0.0.1/33"}},
			{name: "bad-type", sources: "127.0.0.1/32"},
			{name: "proxy", sources: []string{"127.0.0.1/32"}, stream: `{"sockopt":{"acceptProxyProtocol":true}}`},
			{name: "websocket", sources: []string{"127.0.0.1/32"}, stream: `{"network":"ws"}`},
			{name: "different-protocol", sources: []string{"127.0.0.1/32"}, protocol: model.HTTP},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				setupPolicyLedgerDB(t)
				db := database.GetDB()
				owner := &model.ClientRecord{Email: "acl-owner", Enable: true}
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
					request = &model.Inbound{}
					*request = before
				}
				request.Settings = sourceACLSettings(t, tc.sources)
				if tc.rawSettings != "" {
					request.Settings = tc.rawSettings
				}
				if tc.stream != "" {
					request.StreamSettings = tc.stream
				}
				if tc.protocol != "" {
					request.Protocol = tc.protocol
					request.OwnerClientID = nil
				}
				var err error
				if mode == "create" {
					_, _, err = svc.AddInbound(request)
				} else {
					_, _, err = svc.UpdateInbound(request)
				}
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "source") {
					t.Fatalf("invalid source ACL was accepted or rejected for unrelated reason: %v", err)
				}
				if mode == "create" {
					for _, row := range []any{&model.Inbound{}, &model.ClientInbound{}} {
						var count int64
						if err := db.Model(row).Count(&count).Error; err != nil || count != 0 {
							t.Fatalf("invalid ACL leaked %T rows: %d %v", row, count, err)
						}
					}
				} else {
					var after model.Inbound
					if err := db.First(&after, before.Id).Error; err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(before, after) {
						t.Fatal("invalid ACL changed the saved inbound")
					}
				}
				var saved model.ClientRecord
				if err := db.First(&saved, owner.Id).Error; err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(*owner, saved) {
					t.Fatal("invalid ACL changed the canonical owner")
				}
			})
		}
	}
}

func TestTunnelSourceACLOwnerDetachAndReenable(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	svc, clients := &InboundService{}, &ClientService{}
	if _, err := clients.Create(svc, &ClientCreatePayload{Client: model.Client{Email: "acl-detach-owner", Enable: true}}); err != nil {
		t.Fatal(err)
	}
	owner, err := clients.GetRecordByEmail(nil, "acl-detach-owner")
	if err != nil {
		t.Fatal(err)
	}
	request := tunnelOwnerRequest(t, owner.StableID, map[string]any{"enable": true})
	request.Settings = sourceACLSettings(t, []string{"127.0.0.1/32"})
	inbound, _, err := svc.AddInbound(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Detach(svc, owner.Id, []int{inbound.Id}); err != nil {
		t.Fatal(err)
	}
	var detached model.Inbound
	if err := db.First(&detached, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	if detached.Enable || !strings.Contains(detached.Settings, "allowedSourceCidrs") {
		t.Fatal("last-owner detach did not preserve the disabled ACL")
	}
	if _, err := svc.SetInboundEnable(inbound.Id, true); err == nil {
		t.Fatal("re-enabled ACL without canonical owner")
	}
	var stillDetached model.Inbound
	if err := db.First(&stillDetached, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(detached, stillDetached) {
		t.Fatal("failed re-enable changed the detached inbound")
	}
	if _, err := clients.Attach(svc, owner.Id, []int{inbound.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetInboundEnable(inbound.Id, true); err != nil {
		t.Fatal(err)
	}
}

func TestTunnelSourceACLRequiresCanonicalOwnerWhenEnabled(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &InboundService{}
	request := &model.Inbound{Protocol: model.Tunnel, Listen: "127.0.0.1", Port: 24101, Enable: true, Settings: sourceACLSettings(t, []string{"127.0.0.1/32"}), StreamSettings: `{}`}
	if _, _, err := svc.AddInbound(request); err == nil {
		t.Fatal("enabled source ACL without canonical owner")
	}
	request.Id = 0
	request.Enable = false
	dormant, _, err := svc.AddInbound(request)
	if err != nil {
		t.Fatal(err)
	}
	if dormant.Enable {
		t.Fatal("disabled ownerless ACL was enabled")
	}
	if _, err := svc.SetInboundEnable(dormant.Id, true); err == nil {
		t.Fatal("restored ownerless ACL was enabled")
	}
}

func TestTunnelSourceACLRestoredRowsCannotBypassValidation(t *testing.T) {
	for _, scenario := range []string{"unowned", "case-variant", "mixed-case", "invalid-prefix", "wrong-protocol"} {
		t.Run(scenario, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			policyConfigTemplate(t)
			db := database.GetDB()
			row := &model.Inbound{Protocol: model.Tunnel, Listen: "127.0.0.1", Port: 24101, Enable: true, Tag: "restored-acl", Settings: sourceACLSettings(t, []string{"127.0.0.1/32"}), StreamSettings: `{}`}
			if scenario == "case-variant" {
				row.Settings = `{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":9001,"AllowedSourceCidrs":["127.0.0.1/32"]}`
			}
			if scenario == "mixed-case" {
				row.Settings = `{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":9001,"allowedSourceCidrs":[],"AllowedSourceCidrs":["127.0.0.1/32"]}`
			}
			if scenario == "invalid-prefix" {
				row.Settings = sourceACLSettings(t, []string{"invalid"})
			}
			if scenario == "wrong-protocol" {
				row.Protocol = model.HTTP
			}
			if err := db.Create(row).Error; err != nil {
				t.Fatal(err)
			}
			if scenario != "unowned" {
				owner := &model.ClientRecord{Email: "restored-owner", Enable: true}
				if err := db.Create(owner).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&model.ClientInbound{ClientId: owner.Id, InboundId: row.Id}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, err := (&XrayService{}).GetXrayConfig(); err == nil || !strings.Contains(strings.ToLower(err.Error()), "source") {
				t.Fatalf("restored source ACL bypassed validation: %v", err)
			}
		})
	}
}

func TestTunnelSourceACLDeleteOwnerWithDriftedSettings(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(fmt.Sprint("bulk=", bulk), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			svc, clients := &InboundService{}, &ClientService{}
			if _, err := clients.Create(svc, &ClientCreatePayload{Client: model.Client{Email: "acl-delete-owner", Enable: true}}); err != nil {
				t.Fatal(err)
			}
			owner, err := clients.GetRecordByEmail(nil, "acl-delete-owner")
			if err != nil {
				t.Fatal(err)
			}
			request := tunnelOwnerRequest(t, owner.StableID, map[string]any{"enable": true})
			settings := sourceACLSettings(t, []string{"127.0.0.1/32"})
			request.Settings = settings
			inbound, _, err := svc.AddInbound(request)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Model(inbound).Update("settings", settings).Error; err != nil {
				t.Fatal(err)
			}
			if bulk {
				result, _, err := clients.BulkDelete(svc, []string{owner.Email}, false)
				if err != nil {
					t.Fatal(err)
				}
				if result.Deleted != 1 {
					t.Fatalf("delete result: %+v", result)
				}
			} else if _, err := clients.Delete(svc, owner.Id, false); err != nil {
				t.Fatal(err)
			}
			var saved model.Inbound
			if err := db.First(&saved, inbound.Id).Error; err != nil {
				t.Fatal(err)
			}
			var beforeSettings, afterSettings map[string]any
			if err := json.Unmarshal([]byte(settings), &beforeSettings); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(saved.Settings), &afterSettings); err != nil {
				t.Fatal(err)
			}
			if saved.Enable || !reflect.DeepEqual(beforeSettings, afterSettings) {
				t.Fatalf("owner removal left enabled ACL or changed it: %+v", saved)
			}
			var owners int64
			if err := db.Model(&model.ClientInbound{}).Where("inbound_id = ?", inbound.Id).Count(&owners).Error; err != nil || owners != 0 {
				t.Fatalf("remaining owners: %d %v", owners, err)
			}
		})
	}
}
