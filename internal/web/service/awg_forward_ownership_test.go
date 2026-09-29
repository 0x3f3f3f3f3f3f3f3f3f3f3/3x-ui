package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func awgForwardOwnershipFixture(t *testing.T, tag string, port, subnet, forwarded int) (*model.Inbound, model.Client) {
	t.Helper()
	var parsed amneziawg.InboundSettings
	if err := json.Unmarshal([]byte(awgRelayWindowSettingsWithForward(t, tag, fmt.Sprint(forwarded))), &parsed); err != nil {
		t.Fatal(err)
	}
	parsed.Server.SubnetIP = fmt.Sprintf("10.%d.0.0", subnet)
	parsed.Clients[0].AllowedIPs = []string{fmt.Sprintf("10.%d.0.2/32", subnet)}
	settings, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	return &model.Inbound{Tag: tag, Protocol: model.AmneziaWG, Port: port, Listen: "127.0.0.1", Enable: true, Settings: string(settings)}, parsed.Clients[0]
}

func TestAWGForwardOwnershipRejectsDuplicatePeers(t *testing.T) {
	testAWGForwardOwnershipRejectsDuplicatePeers(t, false)
}

func TestAWGForwardOwnershipRejectsDuplicatePeers_Postgres(t *testing.T) {
	testAWGForwardOwnershipRejectsDuplicatePeers(t, true)
}

func testAWGForwardOwnershipRejectsDuplicatePeers(t *testing.T, postgres bool) {
	for _, operation := range []string{"create-batch", "add-batch", "add-client", "update-client", "cross-inbound"} {
		for _, conflict := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/conflict=%t", operation, conflict), func(t *testing.T) {
				if postgres {
					managedUsagePostgresSchema(t)
				}
				setupConflictDB(t)
				inboundSvc, clientSvc := &InboundService{}, &ClientService{}
				owner, first := awgForwardOwnershipFixture(t, "forward-owner", 51992, 82, 34410)
				secondInbound, second := awgForwardOwnershipFixture(t, "forward-candidate", 51993, 83, 34411)
				secondPort := "34411"
				if conflict {
					secondPort = "34410"
				}
				if operation != "cross-inbound" {
					second.AllowedIPs = []string{"10.82.0.3/32"}
				}
				var parsed amneziawg.InboundSettings
				if err := json.Unmarshal([]byte(owner.Settings), &parsed); err != nil {
					t.Fatal(err)
				}
				if operation == "add-batch" {
					parsed.Clients = nil
				}
				if operation != "create-batch" {
					raw, err := json.Marshal(parsed)
					if err != nil {
						t.Fatal(err)
					}
					owner.Settings = string(raw)
					if _, _, err := inboundSvc.AddInbound(owner); err != nil {
						t.Fatal(err)
					}
				}
				if operation == "update-client" {
					if _, err := clientSvc.AddInboundClient(inboundSvc, &model.Inbound{Id: owner.Id, Settings: clientsSettings(t, []model.Client{second})}); err != nil {
						t.Fatal(err)
					}
					if err := database.GetDB().First(owner, owner.Id).Error; err != nil {
						t.Fatal(err)
					}
				}
				beforeSettings := owner.Settings
				counts := map[string]int64{}
				for _, table := range []string{"inbounds", "clients", "client_traffics", "client_inbounds"} {
					var count int64
					if err := database.GetDB().Table(table).Count(&count).Error; err != nil {
						t.Fatal(err)
					}
					counts[table] = count
				}
				second.ForwardedPorts = secondPort
				var err error
				switch operation {
				case "create-batch":
					parsed.Clients = []model.Client{first, second}
					raw, marshalErr := json.Marshal(parsed)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					owner.Settings = string(raw)
					_, _, err = inboundSvc.AddInbound(owner)
				case "add-batch":
					_, err = clientSvc.AddInboundClient(inboundSvc, &model.Inbound{Id: owner.Id, Settings: clientsSettings(t, []model.Client{first, second})})
				case "add-client":
					_, err = clientSvc.AddInboundClient(inboundSvc, &model.Inbound{Id: owner.Id, Settings: clientsSettings(t, []model.Client{second})})
				case "update-client":
					_, err = clientSvc.UpdateInboundClient(inboundSvc, &model.Inbound{Id: owner.Id, Settings: clientsSettings(t, []model.Client{second})}, second.Email)
				case "cross-inbound":
					var cross amneziawg.InboundSettings
					if parseErr := json.Unmarshal([]byte(secondInbound.Settings), &cross); parseErr != nil {
						t.Fatal(parseErr)
					}
					cross.Clients = []model.Client{second}
					raw, marshalErr := json.Marshal(cross)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					secondInbound.Settings = string(raw)
					_, _, err = inboundSvc.AddInbound(secondInbound)
				}
				if !conflict {
					if err != nil {
						t.Fatalf("disjoint forward rejected: %v", err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), "34410") || !strings.Contains(err.Error(), first.Email) {
					t.Fatalf("duplicate peer forward was not rejected with its owner: %v", err)
				}
				for table, before := range counts {
					var after int64
					if err := database.GetDB().Table(table).Count(&after).Error; err != nil || after != before {
						t.Fatalf("rejected claim changed %s: before=%d after=%d err=%v", table, before, after, err)
					}
				}
				if operation != "create-batch" {
					var saved model.Inbound
					if err := database.GetDB().First(&saved, owner.Id).Error; err != nil || saved.Settings != beforeSettings {
						t.Fatalf("rejected claim changed owner settings: %v", err)
					}
				}
			})
		}
	}
}

func TestAWGForwardCannotOverlapEditedPublicListener(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(fmt.Sprintf("postgres=%t", postgres), func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			setupConflictDB(t)
			svc := &InboundService{}
			inbound, _ := awgForwardOwnershipFixture(t, "self-owner", 51992, 82, 34410)
			if _, _, err := svc.AddInbound(inbound); err != nil {
				t.Fatal(err)
			}
			before := inbound.Settings
			inbound.Port = 34410
			if _, _, err := svc.UpdateInbound(inbound); err == nil || !strings.Contains(err.Error(), "34410") {
				t.Fatalf("editing public listener onto its own forward was not rejected: %v", err)
			}
			var saved model.Inbound
			if err := database.GetDB().First(&saved, inbound.Id).Error; err != nil || saved.Port != 51992 || saved.Settings != before {
				t.Fatalf("rejected self collision changed stored inbound: port=%d err=%v", saved.Port, err)
			}
		})
	}
}

func TestAWGForwardOwnershipAllowsSelfAndConflictRemoval(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		for _, operation := range []string{"update-client", "update-inbound", "disable-client", "disable-inbound", "disable-inbound-edit", "bulk-disable"} {
			t.Run(fmt.Sprintf("postgres=%t/%s", postgres, operation), func(t *testing.T) {
				if postgres {
					managedUsagePostgresSchema(t)
				}
				setupConflictDB(t)
				inboundSvc, clientSvc := &InboundService{}, &ClientService{}
				inbound, first := awgForwardOwnershipFixture(t, "stable-owner", 51992, 82, 34410)
				if _, _, err := inboundSvc.AddInbound(inbound); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(operation, "disable") {
					other, _ := awgForwardOwnershipFixture(t, "legacy-conflict", 51993, 83, 34410)
					if err := database.GetDB().Create(other).Error; err != nil {
						t.Fatal(err)
					}
				}
				var err error
				switch operation {
				case "update-client":
					_, err = clientSvc.UpdateInboundClient(inboundSvc, &model.Inbound{Id: inbound.Id, Settings: clientsSettings(t, []model.Client{first})}, first.Email)
				case "update-inbound":
					inbound.Remark = "same owner, unchanged forward"
					_, _, err = inboundSvc.UpdateInbound(inbound)
				case "disable-client":
					first.Enable = false
					_, err = clientSvc.UpdateInboundClient(inboundSvc, &model.Inbound{Id: inbound.Id, Settings: clientsSettings(t, []model.Client{first})}, first.Email)
				case "disable-inbound":
					_, err = inboundSvc.SetInboundEnable(inbound.Id, false)
				case "disable-inbound-edit":
					inbound.Enable = false
					_, _, err = inboundSvc.UpdateInbound(inbound)
				case "bulk-disable":
					result, _, changeErr := clientSvc.BulkSetEnable(inboundSvc, []string{first.Email}, false)
					err = changeErr
					if err == nil && (result.Changed != 1 || len(result.Skipped) != 0) {
						err = fmt.Errorf("disable outcome: %+v", result)
					}
				}
				if err != nil {
					t.Fatalf("retaining self or removing a legacy conflict must succeed: %v", err)
				}
				if operation == "disable-client" || operation == "bulk-disable" {
					assertEnableEverywhere(t, clientSvc, inboundSvc, inbound.Id, first.Email, false)
				}
				if strings.HasPrefix(operation, "disable-inbound") {
					var saved model.Inbound
					if err := database.GetDB().First(&saved, inbound.Id).Error; err != nil || saved.Enable {
						t.Fatalf("inbound was not disabled: %v", err)
					}
				}
			})
		}
	}
}

func TestAWGForwardCanUsePublicPortReleasedBySameEdit(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(fmt.Sprintf("postgres=%t", postgres), func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			setupConflictDB(t)
			svc := &InboundService{}
			inbound, _ := awgForwardOwnershipFixture(t, "moving-owner", 51992, 82, 34410)
			if _, _, err := svc.AddInbound(inbound); err != nil {
				t.Fatal(err)
			}
			var parsed amneziawg.InboundSettings
			if err := json.Unmarshal([]byte(inbound.Settings), &parsed); err != nil {
				t.Fatal(err)
			}
			parsed.Clients[0].ForwardedPorts = "51992"
			encoded, err := json.Marshal(parsed)
			if err != nil {
				t.Fatal(err)
			}
			inbound.Port, inbound.Settings = 51994, string(encoded)
			if _, _, err := svc.UpdateInbound(inbound); err != nil {
				t.Fatalf("released public port must be available in the same edit: %v", err)
			}
			var saved model.Inbound
			if err := database.GetDB().First(&saved, inbound.Id).Error; err != nil || saved.Port != 51994 {
				t.Fatalf("public listener did not move: %v", err)
			}
			clients, err := svc.GetClients(&saved)
			if err != nil || len(clients) != 1 || clients[0].ForwardedPorts != "51992" {
				t.Fatalf("released port was not assigned to the forward: %v", err)
			}
		})
	}
}

func TestAWGForwardOwnershipCanDisableOneOfSeveralLegacyConflicts(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(fmt.Sprintf("postgres=%t", postgres), func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			setupConflictDB(t)
			inboundSvc, clientSvc := &InboundService{}, &ClientService{}
			inbound, first := awgForwardOwnershipFixture(t, "disable-one", 51992, 82, 34410)
			if _, _, err := inboundSvc.AddInbound(inbound); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				_, peer := awgForwardOwnershipFixture(t, fmt.Sprintf("legacy-%d", i), 51993+i, 83+i, 34411+i)
				peer.AllowedIPs = []string{fmt.Sprintf("10.82.0.%d/32", 3+i)}
				if _, err := clientSvc.AddInboundClient(inboundSvc, &model.Inbound{Id: inbound.Id, Settings: clientsSettings(t, []model.Client{peer})}); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.GetDB().First(inbound, inbound.Id).Error; err != nil {
				t.Fatal(err)
			}
			var parsed amneziawg.InboundSettings
			if err := json.Unmarshal([]byte(inbound.Settings), &parsed); err != nil {
				t.Fatal(err)
			}
			for i := range parsed.Clients {
				parsed.Clients[i].ForwardedPorts = "34410"
			}
			encoded, err := json.Marshal(parsed)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Model(inbound).Update("settings", string(encoded)).Error; err != nil {
				t.Fatal(err)
			}
			first.Enable = false
			if _, err := clientSvc.UpdateInboundClient(inboundSvc, &model.Inbound{Id: inbound.Id, Settings: clientsSettings(t, []model.Client{first})}, first.Email); err != nil {
				t.Fatalf("unrelated legacy conflicts must not prevent removing this client's claims: %v", err)
			}
			assertEnableEverywhere(t, clientSvc, inboundSvc, inbound.Id, first.Email, false)
		})
	}
}

func TestAWGForwardDisableIgnoresLegacyFixedReservation(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(fmt.Sprintf("postgres=%t", postgres), func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			setupConflictDB(t)
			inboundSvc, clientSvc := &InboundService{}, &ClientService{}
			inbound, first := awgForwardOwnershipFixture(t, "disable-fixed-conflict", 51992, 82, 34410)
			if _, _, err := inboundSvc.AddInbound(inbound); err != nil {
				t.Fatal(err)
			}
			if err := (&SettingService{}).saveSetting("xrayTemplateConfig", `{"inbounds":[{"tag":"legacy-fixed-owner","protocol":"trojan","listen":"127.0.0.1","port":34410}]}`); err != nil {
				t.Fatal(err)
			}
			first.Enable = false
			if _, err := clientSvc.UpdateInboundClient(inboundSvc, &model.Inbound{Id: inbound.Id, Settings: clientsSettings(t, []model.Client{first})}, first.Email); err != nil {
				t.Fatalf("legacy fixed-port conflict must not prevent disabling its client: %v", err)
			}
			assertEnableEverywhere(t, clientSvc, inboundSvc, inbound.Id, first.Email, false)
			first.Enable = true
			if _, err := clientSvc.UpdateInboundClient(inboundSvc, &model.Inbound{Id: inbound.Id, Settings: clientsSettings(t, []model.Client{first})}, first.Email); err == nil || !strings.Contains(err.Error(), "legacy-fixed-owner") {
				t.Fatalf("re-enabling the conflict must still fail: %v", err)
			}
			assertEnableEverywhere(t, clientSvc, inboundSvc, inbound.Id, first.Email, false)
		})
	}
}

func TestAWGForwardOwnershipUsesRuntimeTargetGate(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		for _, state := range []string{"enabled", "disabled-inbound", "disabled-client", "no-email", "v6-inactive", "v6-active"} {
			t.Run(fmt.Sprintf("postgres=%t/%s", postgres, state), func(t *testing.T) {
				if postgres {
					managedUsagePostgresSchema(t)
				}
				setupConflictDB(t)
				owner, _ := awgForwardOwnershipFixture(t, "target-owner", 51992, 82, 34410)
				var parsed amneziawg.InboundSettings
				if err := json.Unmarshal([]byte(owner.Settings), &parsed); err != nil {
					t.Fatal(err)
				}
				switch state {
				case "disabled-inbound":
					owner.Enable = false
				case "disabled-client":
					parsed.Clients[0].Enable = false
				case "no-email":
					parsed.Clients[0].Email = ""
				case "v6-inactive", "v6-active":
					parsed.Clients[0].AllowedIPs = []string{"fd82::2/128"}
					parsed.Server.IPv6Subnet = "fd82::/64"
					parsed.Server.IPv6Enabled = state == "v6-active"
				}
				encoded, err := json.Marshal(parsed)
				if err != nil {
					t.Fatal(err)
				}
				owner.Settings = string(encoded)
				if err := database.GetDB().Create(owner).Error; err != nil {
					t.Fatal(err)
				}
				candidate, _ := awgForwardOwnershipFixture(t, "target-candidate", 51993, 83, 34410)
				_, _, err = (&InboundService{}).AddInbound(candidate)
				wantConflict := state == "enabled" || state == "disabled-inbound" || state == "v6-active"
				if wantConflict {
					if err == nil || !strings.Contains(err.Error(), "target-owner") || !strings.Contains(err.Error(), "34410") {
						t.Fatalf("reserved target lost its forward claim: %v", err)
					}
				} else if err != nil {
					t.Fatalf("a peer without a runtime forward target must not hold the port: %v", err)
				}
			})
		}
	}
}

func TestAWGForwardOwnershipAcrossCanonicalAttachments(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		for _, forwarding := range []bool{false, true} {
			t.Run(fmt.Sprintf("postgres=%t/forwarding=%t", postgres, forwarding), func(t *testing.T) {
				if postgres {
					managedUsagePostgresSchema(t)
				}
				setupConflictDB(t)
				inboundSvc, clientSvc := &InboundService{}, &ClientService{}
				owner, first := awgForwardOwnershipFixture(t, "canonical-owner", 51992, 82, 34410)
				if !forwarding {
					var parsed amneziawg.InboundSettings
					if err := json.Unmarshal([]byte(owner.Settings), &parsed); err != nil {
						t.Fatal(err)
					}
					parsed.Clients[0].ForwardedPorts = ""
					encoded, err := json.Marshal(parsed)
					if err != nil {
						t.Fatal(err)
					}
					owner.Settings = string(encoded)
				}
				if _, _, err := inboundSvc.AddInbound(owner); err != nil {
					t.Fatal(err)
				}
				target, _ := awgForwardOwnershipFixture(t, "canonical-target", 51993, 82, 34411)
				var empty amneziawg.InboundSettings
				if err := json.Unmarshal([]byte(target.Settings), &empty); err != nil {
					t.Fatal(err)
				}
				empty.Clients = nil
				encoded, err := json.Marshal(empty)
				if err != nil {
					t.Fatal(err)
				}
				target.Settings = string(encoded)
				if _, _, err := inboundSvc.AddInbound(target); err != nil {
					t.Fatal(err)
				}
				before := target.Settings
				result, _, err := clientSvc.BulkAttach(inboundSvc, []string{first.Email}, []int{target.Id})
				if err != nil {
					t.Fatal(err)
				}
				if !forwarding {
					if len(result.Attached) != 1 || len(result.Errors) != 0 {
						t.Fatalf("shared identity without a duplicate listener must remain attachable: %+v", result)
					}
					return
				}
				if len(result.Attached) != 0 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "34410") || !strings.Contains(result.Errors[0], first.Email) {
					t.Fatalf("same canonical identity must not create a second owner of one host port: %+v", result)
				}
				if err := database.GetDB().First(target, target.Id).Error; err != nil || target.Settings != before {
					t.Fatalf("rejected shared attachment changed target settings: %v", err)
				}
				var count int64
				if err := database.GetDB().Model(&model.ClientInbound{}).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("rejected shared attachment changed ownership links: count=%d err=%v", count, err)
				}
			})
		}
	}
}
