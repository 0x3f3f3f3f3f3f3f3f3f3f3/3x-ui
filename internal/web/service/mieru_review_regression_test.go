package service

import (
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/link"
)

func TestMieruOmittedUpdateCannotRevertConcurrentRotation(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &InboundService{}
	created, _, err := svc.AddInbound(&model.Inbound{Tag: "review-native", Protocol: model.Mieru, Port: 24270, Settings: `{"transport":"TCP","clients":[{"email":"rotation-owner","enable":true,"mieruUsername":"wire","mieruPassword":"before"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	rotated := false
	const callback = "test:mieru-rotate-before-serialized-write"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if rotated || tx.Statement.Table != "inbounds" {
			return
		}
		rotated = true
		if err := db.Model(&model.ClientRecord{}).Where("email = ?", "rotation-owner").Update("mieru_password", "after").Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	created.Settings = `{"transport":"TCP","clients":[{"email":"rotation-owner","enable":true,"comment":"omitted secrets"}]}`
	_, _, err = svc.UpdateInbound(created)
	if !rotated {
		t.Fatal("rotation hook did not execute")
	}
	stored, getErr := (&ClientService{}).GetRecordByEmail(nil, "rotation-owner")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored.MieruPassword != "after" {
		t.Fatalf("omitted public update reverted rotation: password=%q updateErr=%v", stored.MieruPassword, err)
	}
}

func TestMieruJSONRepeatedEndpointRetainsSubscriptionTags(t *testing.T) {
	body := []byte(`{"profiles":[{"profileName":"first","user":{"name":"user","password":"secret"},"servers":[{"domainName":"native.example.test","portBindings":[{"port":8443,"protocol":"TCP"}]}]},{"profileName":"second","user":{"name":"user","password":"secret"},"servers":[{"domainName":"native.example.test","portBindings":[{"port":8443,"protocol":"TCP"}]}]}],"activeProfile":"first","socks5Port":1080}`)
	first, ids, err := link.ParseSubscriptionBody(body)
	if err != nil {
		t.Fatal(err)
	}
	tags := assignStableTags(first, ids, map[string]string{}, nil, 7, "")
	prev := map[string]string{}
	pos := map[int]string{}
	for i, id := range ids {
		prev[id] = tags[i]
		pos[i] = tags[i]
	}
	next, nextIDs, err := link.ParseSubscriptionBody(body)
	if err != nil {
		t.Fatal(err)
	}
	nextTags := assignStableTags(next, nextIDs, prev, pos, 7, "")
	if tags[0] != nextTags[0] || tags[1] != nextTags[1] {
		t.Fatalf("unchanged official JSON shifts tags: first=%v refresh=%v duplicateIDs=%t", tags, nextTags, ids[0] == ids[1])
	}
}
