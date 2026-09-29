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

func TestAWGForwardOwnershipRechecksEnable(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		for _, operation := range []string{"enable-client", "enable-inbound", "enable-inbound-edit", "bulk-enable"} {
			t.Run(fmt.Sprintf("postgres=%t/%s", postgres, operation), func(t *testing.T) {
				if postgres {
					managedUsagePostgresSchema(t)
				}
				setupConflictDB(t)
				inboundSvc, clientSvc := &InboundService{}, &ClientService{}
				owner, first := awgForwardOwnershipFixture(t, "active-owner", 51992, 82, 34410)
				if _, _, err := inboundSvc.AddInbound(owner); err != nil {
					t.Fatal(err)
				}
				candidate, client := awgForwardOwnershipFixture(t, "inactive-candidate", 51993, 83, 34411)
				var parsed amneziawg.InboundSettings
				if err := json.Unmarshal([]byte(candidate.Settings), &parsed); err != nil {
					t.Fatal(err)
				}
				inboundOperation := strings.HasPrefix(operation, "enable-inbound")
				if inboundOperation {
					candidate.Enable = false
				} else {
					parsed.Clients[0].Enable = false
				}
				encoded, err := json.Marshal(parsed)
				if err != nil {
					t.Fatal(err)
				}
				candidate.Settings = string(encoded)
				if _, _, err := inboundSvc.AddInbound(candidate); err != nil {
					t.Fatal(err)
				}
				// Simulate a restored inactive row whose port was assigned elsewhere.
				parsed.Clients[0].ForwardedPorts = "34410"
				encoded, err = json.Marshal(parsed)
				if err != nil {
					t.Fatal(err)
				}
				before := string(encoded)
				if err := database.GetDB().Model(candidate).Update("settings", before).Error; err != nil {
					t.Fatal(err)
				}
				client.ForwardedPorts, client.Enable = "34410", true
				enable := func() error {
					switch operation {
					case "enable-client":
						_, err := clientSvc.UpdateInboundClient(inboundSvc, &model.Inbound{Id: candidate.Id, Settings: clientsSettings(t, []model.Client{client})}, client.Email)
						return err
					case "enable-inbound":
						_, err := inboundSvc.SetInboundEnable(candidate.Id, true)
						return err
					case "enable-inbound-edit":
						candidate.Enable = true
						_, _, err := inboundSvc.UpdateInbound(candidate)
						return err
					default:
						result, _, err := clientSvc.BulkSetEnable(inboundSvc, []string{client.Email}, true)
						if err == nil && (result.Changed != 1 || len(result.Skipped) != 0) {
							err = fmt.Errorf("bulk enable: %+v", result)
						}
						return err
					}
				}
				if err := enable(); err == nil || !strings.Contains(err.Error(), first.Email) || !strings.Contains(err.Error(), "34410") {
					t.Fatalf("enable did not reject the current peer owner: %v", err)
				}
				if err := database.GetDB().First(candidate, candidate.Id).Error; err != nil || candidate.Settings != before {
					t.Fatalf("rejected enable changed client settings: %v", err)
				}
				if inboundOperation {
					if candidate.Enable {
						t.Fatal("rejected enable changed inbound state")
					}
				} else {
					assertEnableEverywhere(t, clientSvc, inboundSvc, candidate.Id, client.Email, false)
				}
				if _, err := inboundSvc.DelInbound(owner.Id); err != nil {
					t.Fatal(err)
				}
				if err := enable(); err != nil {
					t.Fatalf("enable after the owner was removed: %v", err)
				}
				assertEnableEverywhere(t, clientSvc, inboundSvc, candidate.Id, client.Email, true)
			})
		}
	}
}
