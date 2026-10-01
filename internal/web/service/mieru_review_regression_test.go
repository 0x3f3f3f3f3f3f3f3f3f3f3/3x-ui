package service

import (
	"encoding/json"
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

func TestMieruClientUpdateRefusesReusedCanonicalLabel(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	payload, err := json.Marshal(map[string]any{"transport": "TCP", "clients": []model.Client{{Email: "selected-owner", SubID: "canonical-race-sub", Enable: true, MieruUsername: "original-wire", MieruPassword: "original-password"}}})
	if err != nil {
		t.Fatal(err)
	}
	listener, _, err := inbounds.AddInbound(&model.Inbound{Tag: "mieru-owner-race", Protocol: model.Mieru, Port: 24693, Settings: string(payload)})
	if err != nil {
		t.Fatal(err)
	}
	otherListener, _, err := inbounds.AddInbound(&model.Inbound{Tag: "mieru-other-owner-race", Protocol: model.Mieru, Port: 24694, Settings: `{"transport":"TCP","clients":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := clients.GetRecordByEmail(nil, "selected-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Attach(inbounds, selected.Id, []int{otherListener.Id}); err != nil {
		t.Fatal(err)
	}
	var acquired *model.ClientRecord
	reads := 0
	changed := false
	db := database.GetDB()
	const callback = "test:mieru-reuse-after-inbound-read"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if changed || tx.Statement.Table != "inbounds" {
			return
		}
		captured, ok := tx.Statement.Dest.(*model.Inbound)
		if !ok || captured.Id != listener.Id {
			return
		}
		reads++
		if reads != 2 {
			return
		}
		changed = true
		if _, err := clients.Update(inbounds, selected.Id, model.Client{Email: "selected-renamed", Enable: true}, 0, otherListener.Id); err != nil {
			tx.AddError(err)
			return
		}
		if _, err := clients.Create(inbounds, &ClientCreatePayload{Client: model.Client{Email: "selected-owner", Enable: true, MieruUsername: "other-wire", MieruPassword: "other-password"}}); err != nil {
			tx.AddError(err)
			return
		}
		var err error
		acquired, err = clients.GetRecordByEmail(nil, "selected-owner")
		if err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	_, err = clients.Update(inbounds, selected.Id, model.Client{Email: "selected-owner", Enable: true, MieruUsername: "requested-wire", MieruPassword: "requested-password"}, 0, listener.Id)
	if !changed || acquired == nil {
		t.Fatalf("public interleaving hook did not run: reads=%d changed=%t outerErr=%v", reads, changed, err)
	}
	var current model.ClientRecord
	if e := db.First(&current, acquired.Id).Error; e != nil {
		t.Fatal(e)
	}
	var links int64
	if e := db.Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", acquired.Id, listener.Id).Count(&links).Error; e != nil {
		t.Fatal(e)
	}
	if err == nil || current.MieruUsername != "other-wire" || current.MieruPassword != "other-password" || links != 0 {
		t.Fatalf("selected stable owner was not fenced against PUBLIC rename + label reuse: error=%v unrelatedUsername=%q unrelatedPasswordChanged=%t unrelatedListenerLinks=%d", err, current.MieruUsername, current.MieruPassword != "other-password", links)
	}
}
