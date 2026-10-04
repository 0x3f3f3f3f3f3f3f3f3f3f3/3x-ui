package clientpolicy

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestPayloadQuotaClosesAfterFinalAdmittedDelivery(t *testing.T) {
	e := NewEngine()
	defer e.Close()
	p := testPolicy("last-payload")
	p.QuotaBytes = 2
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	var paidClosed, idleClosed atomic.Bool
	paid := openSession(t, e, p.ClientID, func() { paidClosed.Store(true) })
	openSession(t, e, p.ClientID, func() { idleClosed.Store(true) })
	if err := paid.Admit(Upload, 1); err != nil {
		t.Fatal(err)
	}
	finish, err := paid.AdmitPayload(Download, 1)
	if err != nil || finish == nil || paidClosed.Load() || !idleClosed.Load() {
		t.Fatal("last paid payload closed before delivery", err)
	}
	if _, err := e.Open(context.Background(), Metadata{ClientID: p.ClientID}, nil); !errors.Is(err, ErrRestricted) {
		t.Fatal("pending delivery allowed quota reconnect", err)
	}
	if _, err := paid.StreamChunkSize(Upload); !errors.Is(err, ErrRestricted) || paidClosed.Load() {
		t.Fatal("concurrent quota check interrupted paid delivery", err)
	}
	finish()
	finish()
	snapshot, err := e.Snapshot(p.ClientID)
	if err != nil || !paidClosed.Load() || snapshot.ActiveSessions != 0 || snapshot.Usage.BilledBytes != 2 {
		t.Fatalf("completed last payload retained session: %+v/%v", snapshot, err)
	}
}

func TestPendingQuotaPayloadDoesNotDelayDisable(t *testing.T) {
	e := NewEngine()
	defer e.Close()
	p := testPolicy("pending-disable")
	p.QuotaBytes = 2
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	var closed atomic.Bool
	s := openSession(t, e, p.ClientID, func() { closed.Store(true) })
	finish, err := s.AdmitPayload(Upload, 2)
	if err != nil || closed.Load() {
		t.Fatal("pending final payload not admitted", err)
	}
	p.Version++
	p.Enabled = false
	if err := e.Apply(p); err != nil || !closed.Load() {
		t.Fatal("paid payload delayed explicit disable", err)
	}
	finish()
}
