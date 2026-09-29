package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestManagedReservationsIncludeSSHUpstreamBridge(t *testing.T) {
	for _, override := range []string{"", "31409"} {
		for _, protocol := range []model.Protocol{model.SSH, model.Mieru} {
			for _, private := range []bool{false, true} {
				t.Run(fmt.Sprintf("override=%s/%s/private=%v", override, protocol, private), func(t *testing.T) {
					setupConflictDB(t)
					t.Setenv("XUI_SSH_UPSTREAM_BRIDGE_PORT", override)
					reserved, err := sshOutboundBridgePort()
					if err != nil {
						t.Fatal(err)
					}
					port, bridge := reserved, 31402
					if private {
						port, bridge = 31402, reserved
					}
					inbound := &model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: port, Settings: fmt.Sprintf(`{"bridgePort":%d,"clients":[]}`, bridge)}
					if _, _, err := (&InboundService{}).AddInbound(inbound); err == nil || !strings.Contains(err.Error(), "ssh-upstream") || !strings.Contains(err.Error(), fmt.Sprint(reserved)) {
						t.Fatalf("save did not reserve the SSH upstream loopback bridge: %v", err)
					}
					var count int64
					if err := database.GetDB().Model(&model.Inbound{}).Count(&count).Error; err != nil || count != 0 {
						t.Fatal("rejected upstream bridge conflict persisted an inbound")
					}
				})
			}
		}
	}
}

func TestManagedReservationsIncludeMTProtoRoutingBridge(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, protocol := range []model.Protocol{model.SSH, model.Mieru} {
			t.Run(fmt.Sprintf("%s/reverse=%v", protocol, reverse), func(t *testing.T) {
				setupConflictDB(t)
				owner := &model.Inbound{Tag: "routed-owner", Protocol: model.MTProto, Listen: "127.0.0.1", Port: 31400, Settings: `{"routeThroughXray":true,"routeXrayPort":31401,"clients":[]}`}
				candidate := &model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: 31402, Settings: `{"bridgePort":31401,"clients":[]}`}
				if reverse {
					owner.Protocol, owner.Settings = protocol, `{"bridgePort":31401,"clients":[]}`
					candidate.Protocol, candidate.Settings = model.MTProto, `{"routeThroughXray":true,"routeXrayPort":31401,"clients":[]}`
				}
				if _, _, err := (&InboundService{}).AddInbound(owner); err != nil {
					t.Fatal(err)
				}
				if _, _, err := (&InboundService{}).AddInbound(candidate); err == nil || !strings.Contains(err.Error(), "routed-owner") || !strings.Contains(err.Error(), "31401") {
					t.Fatalf("save did not reject overlapping private routing bridges: %v", err)
				}
				var count int64
				if err := database.GetDB().Model(&model.Inbound{}).Count(&count).Error; err != nil || count != 1 {
					t.Fatal("rejected routing bridge conflict changed stored inbounds")
				}
			})
		}
	}
}

func TestManagedReservationsIncludeTemplateListeners(t *testing.T) {
	for _, mode := range []string{"single", "metrics"} {
		t.Run(mode, func(t *testing.T) {
			setupConflictDB(t)
			template := map[string]any{"inbounds": []any{map[string]any{"tag": "custom-listener", "protocol": "trojan", "listen": "127.0.0.1", "port": 31401}}}
			owner := "custom-listener"
			if mode == "metrics" {
				template = map[string]any{"metrics": map[string]any{"tag": "metrics", "listen": "127.0.0.1:31401"}}
				owner = "metrics"
			}
			encoded, err := json.Marshal(template)
			if err != nil {
				t.Fatal(err)
			}
			if err := (&SettingService{}).saveSetting("xrayTemplateConfig", string(encoded)); err != nil {
				t.Fatal(err)
			}
			candidate := &model.Inbound{Protocol: model.Mieru, Listen: "127.0.0.1", Port: 31403, Settings: `{"bridgePort":31401,"clients":[]}`}
			if _, _, err := (&InboundService{}).AddInbound(candidate); err == nil || !strings.Contains(err.Error(), owner) || !strings.Contains(err.Error(), "31401") {
				t.Fatalf("save did not reserve the template's actual listener: %v", err)
			}
		})
	}
}

func TestTemplateSaveRejectsManagedReservations(t *testing.T) {
	for _, protocol := range []model.Protocol{model.SSH, model.Mieru, model.MTProto} {
		for _, mode := range []string{"public", "bridge", "metrics"} {
			t.Run(string(protocol)+"/"+mode, func(t *testing.T) {
				setupConflictDB(t)
				settings := `{"bridgePort":31401,"clients":[]}`
				if protocol == model.MTProto {
					settings = `{"routeThroughXray":true,"routeXrayPort":31401,"clients":[]}`
				}
				owner := &model.Inbound{Tag: "managed-owner", Protocol: protocol, Listen: "127.0.0.1", Port: 31400, Settings: settings}
				if _, _, err := (&InboundService{}).AddInbound(owner); err != nil {
					t.Fatal(err)
				}
				svc := &XraySettingService{}
				before, err := svc.GetXrayConfigTemplate()
				if err != nil {
					t.Fatal(err)
				}
				port := 31401
				if mode == "public" {
					port = 31400
				}
				template := map[string]any{"inbounds": []any{map[string]any{"tag": "custom-listener", "protocol": "trojan", "listen": "127.0.0.1", "port": port}}}
				if mode == "metrics" {
					template = map[string]any{"metrics": map[string]any{"tag": "metrics", "listen": "127.0.0.1:31401"}}
				}
				encoded, err := json.Marshal(template)
				if err != nil {
					t.Fatal(err)
				}
				if err := svc.SaveXraySetting(string(encoded)); err == nil || !strings.Contains(err.Error(), "managed-owner") {
					t.Fatalf("template save did not reject existing listener ownership: %v", err)
				}
				after, err := svc.GetXrayConfigTemplate()
				if err != nil || before != after {
					t.Fatal("rejected template changed persisted configuration")
				}
			})
		}
	}
}

func TestAdditionalRelayReservationsRetainBindScope(t *testing.T) {
	for _, owner := range []string{"ssh-upstream", "mtproto", "template"} {
		for _, mode := range []string{"udp", "other-address", "other-node"} {
			t.Run(owner+"/"+mode, func(t *testing.T) {
				setupConflictDB(t)
				t.Setenv("XUI_SSH_UPSTREAM_BRIDGE_PORT", "31409")
				port := 31401
				switch owner {
				case "ssh-upstream":
					port = 31409
				case "mtproto":
					seedInboundConflict(t, "routed-owner", "127.0.0.1", 31400, model.MTProto, "", `{"routeThroughXray":true,"routeXrayPort":31401}`)
				case "template":
					if err := (&SettingService{}).saveSetting("xrayTemplateConfig", `{"inbounds":[{"tag":"custom-listener","protocol":"trojan","listen":"127.0.0.1","port":31401}]}`); err != nil {
						t.Fatal(err)
					}
				}
				candidate := &model.Inbound{Protocol: model.VLESS, Listen: "127.0.0.1", Port: port}
				switch mode {
				case "udp":
					candidate.Protocol = model.Hysteria
				case "other-address":
					candidate.Listen = "127.0.0.2"
				case "other-node":
					node := 1
					candidate.NodeID = &node
				}
				if hit, err := (&InboundService{}).checkPortConflict(candidate, 0); err != nil || hit != nil {
					t.Fatalf("independent resource rejected: hit=%v err=%v", hit, err)
				}
			})
		}
	}
}

func TestTemplateReservationUsesCoreSocketSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, listen, protocol, settings, stream string
		candidate                                model.Protocol
		wantConflict                             bool
	}{
		{"localhost", "localhost", "trojan", `{}`, `{}`, model.VLESS, true},
		{"mapped-ip", "::ffff:127.0.0.1", "trojan", `{}`, `{}`, model.VLESS, true},
		{"wildcard-alias", "0:0:0:0:0:0:0:0", "trojan", `{}`, `{}`, model.VLESS, true},
		{"ipv6-only", "::", "trojan", `{}`, `{"sockopt":{"v6only":true}}`, model.VLESS, false},
		{"socks-udp", "127.0.0.1", "socks", `{"udp":true}`, `{}`, model.Hysteria, true},
		{"dokodemo-udp", "127.0.0.1", "dokodemo-door", `{"allowedNetwork":"udp"}`, `{}`, model.Hysteria, true},
		{"dokodemo-tcp-free", "127.0.0.1", "dokodemo-door", `{"allowedNetwork":"udp"}`, `{}`, model.VLESS, false},
		{"dokodemo-legacy-udp", "127.0.0.1", "dokodemo-door", `{"network":"udp"}`, `{}`, model.Hysteria, true},
		{"dokodemo-array", "127.0.0.1", "dokodemo-door", `{"allowedNetwork":["udp"]}`, `{}`, model.Hysteria, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupConflictDB(t)
			template := fmt.Sprintf(`{"inbounds":[{"tag":"socket-owner","listen":%q,"port":31400,"protocol":%q,"settings":%s,"streamSettings":%s}]}`, tc.listen, tc.protocol, tc.settings, tc.stream)
			if err := (&SettingService{}).saveSetting("xrayTemplateConfig", template); err != nil {
				t.Fatal(err)
			}
			inbound := &model.Inbound{Protocol: tc.candidate, Listen: "127.0.0.1", Port: 31400}
			conflict, err := (&InboundService{}).checkPortConflict(inbound, 0)
			if err != nil || (conflict != nil) != tc.wantConflict {
				t.Fatalf("actual socket conflict=%v err=%v, want conflict=%v", conflict, err, tc.wantConflict)
			}
		})
	}
}

func TestTemplateSaveAndInboundCreationReserveAtomically(t *testing.T) {
	setupConflictDB(t)
	for round := range 20 {
		port := 31600 + round
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			results <- (&XraySettingService{}).SaveXraySetting(fmt.Sprintf(`{"inbounds":[{"tag":"racing-template","protocol":"trojan","listen":"127.0.0.1","port":%d}]}`, port))
		}()
		go func() {
			<-start
			_, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: model.Mieru, Listen: "127.0.0.1", Port: 31700 + round, Settings: fmt.Sprintf(`{"bridgePort":%d,"clients":[]}`, port)})
			results <- err
		}()
		close(start)
		first, second := <-results, <-results
		if (first == nil) == (second == nil) {
			t.Fatalf("round %d must have exactly one committed owner: %v / %v", round, first, second)
		}
		for _, err := range []error{first, second} {
			if err != nil && !strings.Contains(err.Error(), fmt.Sprint(port)) {
				t.Fatalf("rejection did not name the conflicting port: %v", err)
			}
		}
	}
}

func TestTemplateSaveAndInboundCreationReserveAtomically_Postgres(t *testing.T) {
	managedUsagePostgresSchema(t)
	TestTemplateSaveAndInboundCreationReserveAtomically(t)
}

func TestMTProtoRoutingBridgeCannotOverlapOwnPublicListener(t *testing.T) {
	setupConflictDB(t)
	inbound := &model.Inbound{Protocol: model.MTProto, Listen: "127.0.0.1", Port: 31400, Settings: `{"routeThroughXray":true,"routeXrayPort":31400,"clients":[]}`}
	if _, _, err := (&InboundService{}).AddInbound(inbound); err == nil {
		t.Fatal("accepted overlapping public and private MTProto TCP listeners")
	}
	var count int64
	if err := database.GetDB().Model(&model.Inbound{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("failed self-conflict persisted a row")
	}
}
