package service

import (
	"errors"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestTunnelOwnerAttachmentNeedsNoProtocolCredential(t *testing.T) {
	setupPolicyLedgerDB(t)
	cs, inboundService := &ClientService{}, &InboundService{}
	owner := &model.ClientRecord{Email: "forward-owner@example.test", SubID: "owner-sub", Enable: true}
	if err := database.GetDB().Create(owner).Error; err != nil {
		t.Fatal(err)
	}
	traffic := &xray.ClientTraffic{Email: owner.Email, Enable: true, Up: 123, Down: 456}
	if err := database.GetDB().Create(traffic).Error; err != nil {
		t.Fatal(err)
	}
	first := mkInbound(t, 24001, model.Tunnel, `{"address":"127.0.0.1","port":9001,"network":"tcp","clients":[]}`)
	second := mkInbound(t, 24002, model.Tunnel, `{"address":"127.0.0.1","port":9002,"network":"tcp,udp","clients":[]}`)
	if _, err := cs.Attach(inboundService, owner.Id, []int{first.Id, second.Id}); err != nil {
		t.Fatalf("attach credential-free listener owner: %v", err)
	}
	for _, inbound := range []*model.Inbound{first, second} {
		links := linksOf(t, inbound.Id)
		if len(links) != 1 || links[owner.Id].ClientId != owner.Id {
			t.Fatalf("listener %d ownership = %+v", inbound.Id, links)
		}
	}
	got, err := cs.GetRecordByEmail(nil, owner.Email)
	if err != nil || got.StableID != owner.StableID || got.UUID != "" || got.Password != "" {
		t.Fatalf("attachment changed identity or created a protocol credential: %+v, %v", got, err)
	}
	if err := database.GetDB().Where("email = ?", owner.Email).First(traffic).Error; err != nil {
		t.Fatal(err)
	}
	if traffic.Up != 123 || traffic.Down != 456 {
		t.Fatalf("attaching a second rule rewrote shared usage: %+v", traffic)
	}
	updated := got.ToClient()
	updated.Email = "renamed-forward-owner@example.test"
	if _, err := cs.Update(inboundService, owner.Id, *updated, 0); err != nil {
		t.Fatalf("rename listener owner without a protocol credential: %v", err)
	}
	renamed, err := cs.GetRecordByEmail(nil, updated.Email)
	if err != nil || renamed.StableID != owner.StableID || renamed.Id != owner.Id {
		t.Fatalf("rename replaced listener identity: %+v, %v", renamed, err)
	}
	if _, err := cs.Detach(inboundService, owner.Id, []int{first.Id}); err != nil {
		t.Fatal(err)
	}
	if len(linksOf(t, first.Id)) != 0 || len(linksOf(t, second.Id)) != 1 {
		t.Fatal("detaching one listener changed the sibling ownership")
	}
}

func TestTunnelOwnerCreateAndReplacePreservesClientRecords(t *testing.T) {
	setupPolicyLedgerDB(t)
	cs, inboundService := &ClientService{}, &InboundService{}
	first := model.Client{Email: "original-owner@example.test", Enable: true, SubID: "original-owner"}
	inbound, _, err := inboundService.AddInbound(&model.Inbound{
		Tag: "created-tunnel", Protocol: model.Tunnel, Listen: "127.0.0.1", Port: 24005, Enable: true,
		Settings: clientsSettings(t, []model.Client{first}),
	})
	if err != nil {
		t.Fatalf("create owned listener without a protocol credential: %v", err)
	}
	oldOwner, err := cs.GetRecordByEmail(nil, first.Email)
	if err != nil {
		t.Fatal(err)
	}
	second := model.Client{Email: "replacement-owner@example.test", Enable: true, SubID: "replacement-owner"}
	replacement := *inbound
	replacement.Settings = clientsSettings(t, []model.Client{second})
	if _, _, err := inboundService.UpdateInbound(&replacement); err != nil {
		t.Fatalf("replace listener owner: %v", err)
	}
	newOwner, err := cs.GetRecordByEmail(nil, second.Email)
	if err != nil || newOwner.StableID == oldOwner.StableID {
		t.Fatalf("replacement reused the previous identity: %+v, %v", newOwner, err)
	}
	links := linksOf(t, inbound.Id)
	if len(links) != 1 || links[newOwner.Id].ClientId != newOwner.Id {
		t.Fatalf("replacement ownership: %+v", links)
	}
	preserved, err := cs.GetRecordByEmail(nil, first.Email)
	if err != nil || preserved.StableID != oldOwner.StableID {
		t.Fatalf("reassignment removed the original account: %+v, %v", preserved, err)
	}
	replacement.Settings = clientsSettings(t, []model.Client{first, second})
	if _, _, err := inboundService.UpdateInbound(&replacement); !errors.Is(err, ErrTunnelOwnerConflict) {
		t.Fatalf("ambiguous inbound update returned %v", err)
	}
	if links := linksOf(t, inbound.Id); len(links) != 1 || links[newOwner.Id].ClientId != newOwner.Id {
		t.Fatalf("rejected replacement changed the owner: %+v", links)
	}
	replacement.Settings = clientsSettings(t, nil)
	updated, _, err := inboundService.UpdateInbound(&replacement)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enable {
		t.Fatal("removing the owner returned an enabled anonymous listener")
	}
	var stored model.Inbound
	if err := database.GetDB().First(&stored, inbound.Id).Error; err != nil || stored.Enable || len(linksOf(t, inbound.Id)) != 0 {
		t.Fatalf("owner removal left the forwarding resource active: %+v %v", stored, err)
	}
	for _, record := range []*model.ClientRecord{oldOwner, newOwner} {
		preserved, err := cs.GetRecordByEmail(nil, record.Email)
		if err != nil || preserved.StableID != record.StableID {
			t.Fatalf("removing listener ownership deleted its reusable account: %+v %v", preserved, err)
		}
	}
}

func TestTunnelOwnerRejectsAmbiguousMembershipBeforeWriting(t *testing.T) {
	for _, operation := range []string{"sync", "delta", "add"} {
		t.Run(operation, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			cs, inboundService := &ClientService{}, &InboundService{}
			first := model.Client{Email: "first@example.test", ID: "first-credential", Enable: false, SubID: "first-sub", Comment: "preserved"}
			second := model.Client{Email: "second@example.test", ID: "second-credential", Enable: true, SubID: "second-sub"}
			inbound := mkInbound(t, 24003, model.Tunnel, clientsSettings(t, []model.Client{first}))
			if err := cs.SyncInbound(nil, inbound.Id, []model.Client{first}); err != nil {
				t.Fatal(err)
			}
			firstID := recordID(t, first.Email)
			first.Comment = "must roll back"
			var err error
			switch operation {
			case "sync":
				err = cs.SyncInbound(nil, inbound.Id, []model.Client{first, second})
			case "delta":
				err = cs.ApplyInboundClientDelta(nil, inbound.Id, []model.Client{second}, nil)
			case "add":
				_, err = cs.AddInboundClient(inboundService, &model.Inbound{Id: inbound.Id, Settings: clientsSettings(t, []model.Client{second})})
			}
			if !errors.Is(err, ErrTunnelOwnerConflict) {
				t.Fatalf("ambiguous %s returned %v, want exclusive-owner rejection", operation, err)
			}
			links := linksOf(t, inbound.Id)
			if len(links) != 1 || links[firstID].ClientId != firstID {
				t.Fatalf("failed mutation changed owner: %+v", links)
			}
			var records []model.ClientRecord
			if err := database.GetDB().Find(&records).Error; err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].Comment != "preserved" || records[0].Enable {
				t.Fatalf("failed mutation persisted client changes: %+v", records)
			}
			var stored model.Inbound
			if err := database.GetDB().First(&stored, inbound.Id).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Settings != inbound.Settings {
				t.Fatal("failed mutation persisted inbound settings")
			}
		})
	}
}

func TestTunnelOwnerConcurrentAttachmentsChooseOneOwner(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbound := mkInbound(t, 24004, model.Tunnel, `{"address":"127.0.0.1","port":9001,"network":"tcp","clients":[]}`)
	owners := []*model.ClientRecord{
		{Email: "racing-a@example.test", UUID: "credential-a", SubID: "race-a", Enable: true},
		{Email: "racing-b@example.test", UUID: "credential-b", SubID: "race-b", Enable: true},
	}
	if err := database.GetDB().Create(owners).Error; err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, owner := range owners {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := (&ClientService{}).Attach(&InboundService{}, owner.Id, []int{inbound.Id})
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	success, rejected := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrTunnelOwnerConflict) {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent attachment failure: %v", err)
		}
	}
	if success != 1 || rejected != 1 || len(linksOf(t, inbound.Id)) != 1 {
		t.Fatalf("competing ownership assignments: success=%d rejected=%d links=%+v", success, rejected, linksOf(t, inbound.Id))
	}
	var count int64
	if err := database.GetDB().Model(&xray.ClientTraffic{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("losing attachment left accounting behind: count=%d err=%v", count, err)
	}
}
