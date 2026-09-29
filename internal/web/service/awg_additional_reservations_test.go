package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgnet"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestAWGClientForwardsRespectAdditionalReservations(t *testing.T) {
	testAWGClientForwardsRespectAdditionalReservations(t, false)
}

func testAWGClientForwardsRespectAdditionalReservations(t *testing.T, postgres bool) {
	for _, operation := range []string{"create-inbound", "add-client", "update-client", "update-inbound", "enable-inbound"} {
		for _, owner := range []string{"ssh-upstream", "template-listener", "metrics", "api", "amneziawg-egress", "free"} {
			t.Run(operation+"/"+owner, func(t *testing.T) {
				if postgres {
					managedUsagePostgresSchema(t)
				}
				setupConflictDB(t)
				t.Setenv("XUI_SSH_UPSTREAM_BRIDGE_PORT", "31409")
				port := 31409
				switch owner {
				case "template-listener", "api":
					port = 31410
					template := fmt.Sprintf(`{"inbounds":[{"tag":%q,"protocol":"trojan","listen":"127.0.0.1","port":%d}]}`, owner, port)
					if err := (&SettingService{}).saveSetting("xrayTemplateConfig", template); err != nil {
						t.Fatal(err)
					}
				case "metrics":
					port = 31410
					if err := (&SettingService{}).saveSetting("xrayTemplateConfig", `{"metrics":{"tag":"metrics","listen":"127.0.0.1:31410"}}`); err != nil {
						t.Fatal(err)
					}
				case "amneziawg-egress":
					port = amneziawgnet.EgressPort()
				case "free":
					port = 31411
				}
				inboundSvc, clientSvc := &InboundService{}, &ClientService{}
				settings := awgRelayWindowSettingsWithForward(t, "reservation-candidate", fmt.Sprint(port))
				candidate := &model.Inbound{Tag: "reservation-candidate", Protocol: model.AmneziaWG, Port: 51992, Listen: "127.0.0.1", Enable: true, Settings: settings}
				var beforeSettings string
				var beforeEnable bool
				beforeCounts := make(map[string]int64)
				if operation != "create-inbound" {
					var raw map[string]any
					if err := json.Unmarshal([]byte(settings), &raw); err != nil {
						t.Fatal(err)
					}
					raw["clients"] = []any{}
					encoded, err := json.Marshal(raw)
					if err != nil {
						t.Fatal(err)
					}
					baseline := mkInbound(t, 51992, model.AmneziaWG, string(encoded))
					candidate.Id = baseline.Id
					if operation == "update-client" || operation == "update-inbound" {
						data := &model.Inbound{Id: baseline.Id, Settings: clientsSettings(t, []model.Client{{Email: "reservation-candidate@relay-window", Enable: true}})}
						if _, err := clientSvc.AddInboundClient(inboundSvc, data); err != nil {
							t.Fatal(err)
						}
					}
					if operation == "enable-inbound" {
						if err := database.GetDB().Model(baseline).Updates(map[string]any{"settings": settings, "enable": false}).Error; err != nil {
							t.Fatal(err)
						}
					}
					if err := database.GetDB().First(baseline, baseline.Id).Error; err != nil {
						t.Fatal(err)
					}
					beforeSettings = baseline.Settings
					beforeEnable = baseline.Enable
				}
				for _, table := range []string{"inbounds", "clients", "client_traffics", "client_inbounds"} {
					var count int64
					if err := database.GetDB().Table(table).Count(&count).Error; err != nil {
						t.Fatal(err)
					}
					beforeCounts[table] = count
				}
				var err error
				switch operation {
				case "create-inbound":
					_, _, err = inboundSvc.AddInbound(candidate)
				case "update-inbound":
					_, _, err = inboundSvc.UpdateInbound(candidate)
				case "enable-inbound":
					_, err = inboundSvc.SetInboundEnable(candidate.Id, true)
				case "add-client":
					candidate.Settings = clientsSettings(t, []model.Client{{Email: "reservation-candidate@relay-window", Enable: true, ForwardedPorts: fmt.Sprint(port)}})
					_, err = clientSvc.AddInboundClient(inboundSvc, candidate)
				case "update-client":
					candidate.Settings = clientsSettings(t, []model.Client{{Email: "reservation-candidate@relay-window", Enable: true, ForwardedPorts: fmt.Sprint(port)}})
					_, err = clientSvc.UpdateInboundClient(inboundSvc, candidate, "reservation-candidate@relay-window")
				}
				if owner == "free" {
					if err != nil {
						t.Fatalf("unreserved forward must remain usable: %v", err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), owner) || !strings.Contains(err.Error(), fmt.Sprint(port)) {
					t.Fatalf("forward did not reject %s reservation on port %d: %v", owner, port, err)
				}
				for table, before := range beforeCounts {
					var after int64
					if err := database.GetDB().Table(table).Count(&after).Error; err != nil || after != before {
						t.Fatalf("rejected forward changed %s rows: before=%d after=%d err=%v", table, before, after, err)
					}
				}
				if operation != "create-inbound" {
					var saved model.Inbound
					if err := database.GetDB().First(&saved, candidate.Id).Error; err != nil || saved.Settings != beforeSettings || saved.Enable != beforeEnable {
						t.Fatalf("rejected forward changed existing inbound: %v", err)
					}
				}
			})
		}
	}
}

func TestAWGClientForwardsRespectAdditionalReservations_Postgres(t *testing.T) {
	testAWGClientForwardsRespectAdditionalReservations(t, true)
}

func TestAWGClientForwardMutationWaitsForReservation_Postgres(t *testing.T) {
	for _, operation := range []string{"add", "update", "import", "update-inbound", "enable-inbound", "bulk-enable"} {
		t.Run(operation, func(t *testing.T) {
			managedUsagePostgresSchema(t)
			setupConflictDB(t)
			db := database.GetDB()
			inbound := mkInbound(t, 51992, model.AmneziaWG, amneziawgClientTestSettings)
			clientSvc, inboundSvc := &ClientService{}, &InboundService{}
			client := model.Client{Email: "concurrent-forward", Enable: true}
			if operation == "update" || operation == "update-inbound" || operation == "enable-inbound" || operation == "bulk-enable" {
				if _, err := clientSvc.AddInboundClient(inboundSvc, &model.Inbound{Id: inbound.Id, Settings: clientsSettings(t, []model.Client{client})}); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "bulk-enable" {
				result, _, err := clientSvc.BulkSetEnable(inboundSvc, []string{client.Email}, false)
				if err != nil || result.Changed != 1 || len(result.Skipped) != 0 {
					t.Fatalf("disable seeded client: %+v %v", result, err)
				}
			}
			if err := db.First(inbound, inbound.Id).Error; err != nil {
				t.Fatal(err)
			}
			if operation == "update-inbound" || operation == "enable-inbound" || operation == "bulk-enable" {
				clients, err := inboundSvc.GetClients(inbound)
				if err != nil || len(clients) != 1 {
					t.Fatalf("read seeded client: %v", err)
				}
				client = clients[0]
			}
			var editedSettings map[string]any
			if err := json.Unmarshal([]byte(inbound.Settings), &editedSettings); err != nil {
				t.Fatal(err)
			}
			client.ForwardedPorts = "31410"
			editedSettings["clients"] = []model.Client{client}
			encodedSettings, err := json.Marshal(editedSettings)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "enable-inbound" || operation == "bulk-enable" {
				if err := db.Model(inbound).Updates(map[string]any{"settings": string(encodedSettings), "enable": operation == "bulk-enable"}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.First(inbound, inbound.Id).Error; err != nil {
					t.Fatal(err)
				}
			}
			before := inbound.Settings
			beforeEnable := inbound.Enable
			if operation == "import" {
				retained := &xray.ClientTraffic{Email: client.Email, PolicyID: "deleted-owner", Enable: true, Up: 7, Down: 9}
				if err := db.Create(retained).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := (&SettingService{}).saveSetting("xrayTemplateConfig", `{"inbounds":[]}`); err != nil {
				t.Fatal(err)
			}
			holder := db.Begin()
			if holder.Error != nil {
				t.Fatal(holder.Error)
			}
			t.Cleanup(func() { _ = holder.Rollback().Error })
			if err := lockListenerReservationsTx(holder); err != nil {
				t.Fatal(err)
			}
			var holderPID int
			if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
				t.Fatal(err)
			}
			pending := `{"inbounds":[{"tag":"pending-owner","protocol":"trojan","listen":"127.0.0.1","port":31410}]}`
			if err := holder.Model(&model.Setting{}).Where("key = ?", "xrayTemplateConfig").Update("value", pending).Error; err != nil {
				t.Fatal(err)
			}
			client.ForwardedPorts = "31410"
			input := &model.Inbound{Id: inbound.Id, Settings: clientsSettings(t, []model.Client{client})}
			if operation == "update-inbound" {
				copy := *inbound
				copy.Settings = string(encodedSettings)
				input = &copy
			}
			result := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				var err error
				switch operation {
				case "import":
					item := ClientCreatePayload{Client: client, InboundIds: []int{inbound.Id}, Traffic: &ClientPortableTraffic{Up: 7, Down: 9}}
					restored, _, restoreErr := clientSvc.ImportClients(inboundSvc, []ClientCreatePayload{item})
					err = restoreErr
					if err == nil {
						if restored.Created == 0 && len(restored.Skipped) == 1 {
							err = fmt.Errorf("import rejected: %s", restored.Skipped[0].Reason)
						} else {
							err = fmt.Errorf("unexpected import outcome: %+v", restored)
						}
					}
				case "bulk-enable":
					changed, _, changeErr := clientSvc.BulkSetEnable(inboundSvc, []string{client.Email}, true)
					err = changeErr
					if err == nil {
						if changed.Changed == 0 && len(changed.Skipped) == 1 {
							err = fmt.Errorf("bulk enable rejected: %s", changed.Skipped[0].Reason)
						} else {
							err = fmt.Errorf("unexpected bulk enable outcome: %+v", changed)
						}
					}
				case "update-inbound":
					_, _, err = inboundSvc.UpdateInbound(input)
				case "enable-inbound":
					_, err = inboundSvc.SetInboundEnable(inbound.Id, true)
				case "update":
					_, err = clientSvc.UpdateInboundClient(inboundSvc, input, client.Email)
				default:
					_, err = clientSvc.AddInboundClient(inboundSvc, input)
				}
				result <- err
			}()
			t.Cleanup(func() {
				_ = holder.Rollback().Error
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Error("forward mutation survived reservation-holder cleanup")
				}
			})
			deadline := time.Now().Add(3 * time.Second)
			for {
				select {
				case err := <-result:
					t.Fatalf("forward mutation did not wait for the other reservation transaction: %v", err)
				default:
				}
				var waiting int64
				if err := db.Raw("SELECT count(*) FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid)) AND wait_event = 'advisory'", holderPID).Scan(&waiting).Error; err != nil {
					t.Fatal(err)
				}
				if waiting > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("did not observe the actual reservation lock wait")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if operation == "import" {
				var id int
				if err := holder.Raw("SELECT id FROM client_traffics WHERE email = ? FOR UPDATE NOWAIT", client.Email).Scan(&id).Error; err != nil || id == 0 {
					t.Fatalf("import locked retained traffic before acquiring its reservation: id=%d err=%v", id, err)
				}
			}
			if err := holder.Commit().Error; err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err == nil || !strings.Contains(err.Error(), "pending-owner") || !strings.Contains(err.Error(), "31410") {
					t.Fatalf("forward mutation did not recheck the committed reservation: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("forward mutation did not finish after reservation commit")
			}
			if err := db.First(inbound, inbound.Id).Error; err != nil || inbound.Settings != before || inbound.Enable != beforeEnable {
				t.Fatalf("rejected concurrent forward changed stored settings: %v", err)
			}
			if operation == "bulk-enable" {
				assertEnableEverywhere(t, clientSvc, inboundSvc, inbound.Id, client.Email, false)
				if err := (&SettingService{}).saveSetting("xrayTemplateConfig", `{"inbounds":[]}`); err != nil {
					t.Fatal(err)
				}
				result, _, err := clientSvc.BulkSetEnable(inboundSvc, []string{client.Email}, true)
				if err != nil || result.Changed != 1 || len(result.Skipped) != 0 {
					t.Fatalf("enable after releasing reservation: %+v %v", result, err)
				}
				assertEnableEverywhere(t, clientSvc, inboundSvc, inbound.Id, client.Email, true)
			}
			if operation == "import" {
				var retained xray.ClientTraffic
				if err := db.Where("email = ?", client.Email).First(&retained).Error; err != nil || retained.PolicyID != "deleted-owner" || retained.Up != 7 || retained.Down != 9 {
					t.Fatalf("failed import changed retained accounting ownership: %v", err)
				}
			}
		})
	}
}
