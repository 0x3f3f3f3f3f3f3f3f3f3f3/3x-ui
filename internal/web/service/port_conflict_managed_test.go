package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgnet"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestManagedBridgeReservationsRejectConflictingSaves(t *testing.T) {
	for _, protocol := range []model.Protocol{model.SSH, model.Mieru} {
		for _, direction := range []string{"public-to-bridge", "bridge-to-public", "bridge-to-bridge", "bridge-to-api", "bridge-to-egress"} {
			t.Run(string(protocol)+"/"+direction, func(t *testing.T) {
				setupConflictDB(t)
				svc := &InboundService{}
				owner := &model.Inbound{
					Tag: "reserved-owner", Protocol: protocol, Listen: "127.0.0.1", Port: 31400,
					Settings: `{"bridgePort":31401,"clients":[]}`,
				}
				candidate := &model.Inbound{
					Tag: "candidate", Protocol: model.VLESS, Listen: "0.0.0.0", Port: 31401,
					Settings: `{"clients":[]}`,
				}
				wantOwner, wantPort := "reserved-owner", 31401
				switch direction {
				case "bridge-to-public":
					owner.Protocol, owner.Port, owner.Settings = model.VLESS, 31401, `{"clients":[]}`
					candidate.Protocol, candidate.Port, candidate.Settings = protocol, 31402, `{"bridgePort":31401,"clients":[]}`
				case "bridge-to-bridge":
					candidate.Protocol, candidate.Port, candidate.Settings = model.Mieru, 31402, `{"bridgePort":31401,"clients":[]}`
				case "bridge-to-api", "bridge-to-egress":
					wantOwner, wantPort = "api", reservedAPIPort()
					if direction == "bridge-to-egress" {
						wantOwner, wantPort = "amneziawg-egress", amneziawgnet.EgressPort()
					}
					candidate.Protocol, candidate.Port = protocol, 31402
					candidate.Settings = fmt.Sprintf(`{"bridgePort":%d,"clients":[]}`, wantPort)
				}
				if _, _, err := svc.AddInbound(owner); err != nil {
					t.Fatal(err)
				}
				if _, _, err := svc.AddInbound(candidate); err == nil || !strings.Contains(err.Error(), wantOwner) || !strings.Contains(err.Error(), fmt.Sprint(wantPort)) {
					t.Fatalf("save must reject the reserved listener and name its owner and port; got %v", err)
				}
				var rows int64
				if err := database.GetDB().Model(&model.Inbound{}).Count(&rows).Error; err != nil || rows != 1 {
					t.Fatalf("conflicting save changed stored rows: count=%d err=%v", rows, err)
				}
			})
		}
	}
}

func TestManagedBridgeReservationsRespectAddressTransportAndNode(t *testing.T) {
	for _, tc := range []struct {
		name, listen string
		protocol     model.Protocol
		node         bool
	}{
		{"udp-on-loopback", "127.0.0.1", model.Hysteria, false},
		{"tcp-other-address", "127.0.0.2", model.VLESS, false},
		{"tcp-other-node", "127.0.0.1", model.VLESS, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupConflictDB(t)
			seedInboundConflict(t, "managed-owner", "127.0.0.1", 31400, model.Mieru, "", `{"bridgePort":31401}`)
			candidate := &model.Inbound{Protocol: tc.protocol, Listen: tc.listen, Port: 31401, Settings: `{"clients":[]}`}
			if tc.node {
				nodeID := 1
				candidate.NodeID = &nodeID
			}
			if hit, err := (&InboundService{}).checkPortConflict(candidate, 0); err != nil || hit != nil {
				t.Fatalf("independent bind must remain usable: hit=%v err=%v", hit, err)
			}
		})
	}
}

func TestManagedListenerAddressAliasesCannotBypassBridgeReservations(t *testing.T) {
	for _, protocol := range []model.Protocol{model.SSH, model.Mieru} {
		for _, tc := range []struct {
			listen, canonical, refusal string
		}{
			{"[::]", "", "managed-owner"},
			{"0:0:0:0:0:0:0:0", "", "managed-owner"},
			{"::ffff:127.0.0.1", "", "managed-owner"},
			{"[127.0.0.1]", "", "managed-owner"},
			{"[::1]", "::1", ""},
			{"::ffff:127.0.0.2", "127.0.0.2", ""},
			{"[::", "", "literal IP"},
			{"[[::]]", "", "literal IP"},
		} {
			t.Run(string(protocol)+"/"+tc.listen, func(t *testing.T) {
				setupConflictDB(t)
				seedInboundConflict(t, "managed-owner", "127.0.0.1", 31400, model.Mieru, "", `{"bridgePort":31401}`)
				inbound := &model.Inbound{Protocol: protocol, Listen: tc.listen, Port: 31401, Settings: `{"bridgePort":31402,"clients":[]}`}
				_, _, err := (&InboundService{}).AddInbound(inbound)
				if tc.refusal != "" {
					if err == nil || !strings.Contains(err.Error(), tc.refusal) {
						t.Fatalf("listener alias bypassed reservation/validation: %v", err)
					}
					var rows int64
					if err := database.GetDB().Model(&model.Inbound{}).Count(&rows).Error; err != nil || rows != 1 {
						t.Fatalf("invalid listener persisted: rows=%d err=%v", rows, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				stored, err := (&InboundService{}).GetInbound(inbound.Id)
				if err != nil {
					t.Fatal(err)
				}
				if stored.Listen != tc.canonical {
					t.Fatalf("literal listener was not canonicalized: got=%q want=%q", stored.Listen, tc.canonical)
				}
			})
		}
	}
}

func TestManagedBridgeUpdateKeepsReservationUntilSuccessfulSave(t *testing.T) {
	setupConflictDB(t)
	svc := &InboundService{}
	owner := &model.Inbound{Protocol: model.Mieru, Listen: "127.0.0.1", Port: 31400, Settings: `{"network":"udp","bridgePort":31401,"clients":[]}`}
	if _, _, err := svc.AddInbound(owner); err != nil {
		t.Fatal(err)
	}
	updated := *owner
	updated.Settings = `{"clients":[]}`
	if _, _, err := svc.UpdateInbound(&updated); err != nil {
		t.Fatal(err)
	}
	seedInboundConflict(t, "conflicting-update", "0.0.0.0", 31402, model.VLESS, "", `{"clients":[]}`)
	updated.Settings = `{"bridgePort":31402,"clients":[]}`
	if _, _, err := svc.UpdateInbound(&updated); err == nil || !strings.Contains(err.Error(), "conflicting-update") {
		t.Fatalf("bridge update must reject a reserved public bind: %v", err)
	}
	stored, err := svc.GetInbound(owner.Id)
	if err != nil {
		t.Fatal(err)
	}
	var settings mieruInboundSettings
	if err := json.Unmarshal([]byte(stored.Settings), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.BridgePort != 31401 || settings.Network != "udp" {
		t.Fatalf("rejected update lost the original reservation or transport: port=%d network=%q", settings.BridgePort, settings.Network)
	}
	for _, port := range []int{31401, 31403} {
		hit, err := svc.checkPortConflict(&model.Inbound{Protocol: model.VLESS, Listen: "127.0.0.1", Port: port}, 0)
		if err != nil || (hit != nil) != (port == 31401) {
			t.Fatalf("reservation for port %d: hit=%v err=%v", port, hit, err)
		}
	}
}

func TestManagedBridgeReservationsIncludeAmneziaWGResources(t *testing.T) {
	for _, direction := range []string{"bridge-to-relay", "relay-to-bridge", "bridge-to-forward", "forward-to-bridge"} {
		t.Run(direction, func(t *testing.T) {
			setupConflictDB(t)
			svc := &InboundService{}
			awg := &model.Inbound{Tag: "awg-owner", Protocol: model.AmneziaWG, Port: 51820, Settings: awgRelayWindowSettingsWithForward(t, "awg-owner", "31401")}
			if err := database.GetDB().Create(awg).Error; err != nil {
				t.Fatal(err)
			}
			bridgePort := 31401
			if direction == "bridge-to-relay" || direction == "relay-to-bridge" {
				bridgePort = amneziawgnet.SOCKSPortForInbound(awg.Id)
			}
			managed := &model.Inbound{Tag: "managed-owner", Protocol: model.Mieru, Port: 31400, Settings: fmt.Sprintf(`{"bridgePort":%d,"clients":[]}`, bridgePort)}
			switch direction {
			case "bridge-to-relay", "bridge-to-forward":
				if _, _, err := svc.AddInbound(managed); err == nil || !strings.Contains(err.Error(), "awg-owner") {
					t.Fatalf("managed bridge must reject AWG resource: %v", err)
				}
			case "relay-to-bridge":
				if err := database.GetDB().Create(managed).Error; err != nil {
					t.Fatal(err)
				}
				hit, err := checkAmneziawgnetSocksReverseConflict(database.GetDB(), awg.Id)
				if err != nil || hit == nil || hit.InboundID != managed.Id {
					t.Fatalf("AWG relay must reject managed bridge: hit=%v err=%v", hit, err)
				}
			case "forward-to-bridge":
				if err := database.GetDB().Create(managed).Error; err != nil {
					t.Fatal(err)
				}
				if err := svc.checkAmneziaWGForwardedPorts(database.GetDB(), awg); err == nil || !strings.Contains(err.Error(), "managed-owner") {
					t.Fatalf("AWG forward must reject disabled managed reservation: %v", err)
				}
			}
		})
	}
}
