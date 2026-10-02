package service

import (
	"errors"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestManagedCandidateCannotPrepareAgainstReplacedDatabase(t *testing.T) {
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	inbound := mkInbound(t, 24319, model.Tunnel, `{"network":"tcp","address":"127.0.0.1","port":1111}`)
	if err := (&ClientService{}).SyncInbound(nil, inbound.Id, []model.Client{{Email: "generation-owner", Enable: true}}); err != nil {
		t.Fatal(err)
	}
	candidate, err := (&XrayService{}).compileManagedXrayConfig(policyConfigState(t))
	if err != nil {
		t.Fatal(err)
	}
	before := database.GetDB()
	if err := database.InitDB(config.GetDBPath()); err != nil {
		t.Fatal(err)
	}
	if database.GetDB() == before {
		t.Fatal("fixture did not replace the database generation")
	}
	if _, err := candidate.prepare(); !errors.Is(err, ErrDatabaseReplaced) {
		t.Fatalf("old candidate seeded policy into the replacement database: %v", err)
	}
	if _, err := (&XrayService{}).GetManagedXrayConfig(policyConfigState(t)); err != nil {
		t.Fatalf("fresh candidate could not seed current database: %v", err)
	}
}
