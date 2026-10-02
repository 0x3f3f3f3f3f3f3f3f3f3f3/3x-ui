package sub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func bulkPolicyDecimal(micros int64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d.%06d", micros/1000000, micros%1000000), "0"), ".")
}

// This acceptance begins with the actual bulk JSON shape used by the form,
// then crosses authenticated controllers, SQL, generated core config, live
// native Snell/Tunnel traffic, exact fractional billing and durable reset state.
func TestClientPolicyBulkHTTPNativeBindingsAndTunnelAccounting(t *testing.T) {
	h := newNativeHTTPHarness(t, "bulk_policy_http")
	t.Logf("bulk traffic policy backend: %s", database.GetDB().Dialector.Name())
	mieru := h.add(t, "mieru", "bulk-mieru", `{"transport":"TCP","mtu":1400,"clients":[]}`)
	ssh := h.add(t, "ssh", "bulk-ssh", `{"allowPassword":true,"clients":[]}`)
	a := model.Client{Email: "bulk-policy-a", SubID: "bulk-policy-a-sub", Enable: true, TotalGB: 1000000, SSHPassword: "independent-business-password-a", Policy: &model.ClientPolicyOptions{UploadBytesPerSecond: 262144, DownloadBytesPerSecond: 1048576, Multiplier: "1.234567"}}
	b := model.Client{Email: "bulk-policy-b", SubID: "bulk-policy-b-sub", Enable: true, TotalGB: 1000000, SSHPassword: "independent-business-password-b", Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	result := h.api(t, http.MethodPost, "/panel/api/clients/bulkCreate", []service.ClientCreatePayload{
		{Client: a, InboundIds: []int{mieru.Id, ssh.Id}},
		{Client: b, InboundIds: []int{mieru.Id, ssh.Id}},
	})
	var created service.BulkCreateResult
	if err := json.Unmarshal(result, &created); err != nil || created.Created != 2 || len(created.Skipped) != 0 {
		t.Fatalf("bulk policy accounts were not both created: %s %v", result, err)
	}
	var owners []model.ClientRecord
	if err := database.GetDB().Where("email IN ?", []string{a.Email, b.Email}).Order("email").Find(&owners).Error; err != nil || len(owners) != 2 {
		t.Fatalf("bulk accounts missing from SQL: count=%d err=%v", len(owners), err)
	}
	for i, owner := range owners {
		want := []*model.ClientPolicyOptions{a.Policy, b.Policy}[i]
		if owner.Policy == nil || *owner.Policy != *want || owner.MieruUsername == "" || owner.MieruPassword == "" || owner.SSHUsername == "" {
			t.Fatalf("bulk policy or native credentials missing for account %d", i)
		}
		if _, err := uuid.Parse(owner.StableID); err != nil {
			t.Fatal("bulk account has no canonical identity")
		}
	}
	if owners[0].StableID == owners[1].StableID || owners[0].MieruPassword == owners[1].MieruPassword || owners[0].SSHPassword == owners[1].SSHPassword {
		t.Fatal("bulk accounts share identity or native credentials")
	}
	addOwned := func(protocol, tag, settings, ownerID string) model.Inbound {
		t.Helper()
		raw := h.api(t, http.MethodPost, "/panel/api/inbounds/add", map[string]any{"protocol": protocol, "tag": tag, "listen": "127.0.0.1", "port": sshHTTPPort(t), "enable": true, "ownerClientId": ownerID, "settings": settings, "streamSettings": "{}"})
		var inbound model.Inbound
		if err := json.Unmarshal(raw, &inbound); err != nil {
			t.Fatal(err)
		}
		return inbound
	}
	for i, owner := range owners {
		addOwned("snell", fmt.Sprintf("bulk-snell-%d", i), `{"version":6,"clients":[]}`, owner.StableID)
	}
	settings := fmt.Sprintf(`{"rewriteAddress":"127.0.0.1","rewritePort":%d,"allowedNetwork":"tcp,udp","clients":[]}`, h.target.Addr().(*net.TCPAddr).Port)
	first := addOwned("tunnel", "first-rule", settings, owners[0].StableID)
	second := addOwned("tunnel", "second-rule", settings, owners[0].StableID)
	sibling := addOwned("tunnel", "sibling-rule", settings, owners[1].StableID)
	packetTarget, err := net.ListenPacket("udp4", h.target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packetTarget.Close() })
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, peer, err := packetTarget.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = packetTarget.WriteTo(buffer[:n], peer)
		}
	}()
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	open := func(inbound model.Inbound, network string) net.Conn {
		t.Helper()
		flow, err := net.DialTimeout(network, fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = flow.Close() })
		return flow
	}
	packetEcho := func(flow net.Conn, payload []byte) {
		t.Helper()
		_ = flow.SetDeadline(time.Now().Add(3 * time.Second))
		if n, err := flow.Write(payload); err != nil || n != len(payload) {
			t.Fatalf("Tunnel UDP write n=%d err=%v", n, err)
		}
		buffer := make([]byte, len(payload)+1)
		if n, err := flow.Read(buffer); err != nil || !bytes.Equal(buffer[:n], payload) {
			t.Fatalf("Tunnel UDP boundary changed n=%d want=%d err=%v", n, len(payload), err)
		}
	}
	check := func(owner model.ClientRecord, raw, billedMicros, periodRaw, periodBilledMicros int64) {
		t.Helper()
		if _, _, err := h.svc.GetXrayTraffic(); err != nil {
			t.Fatal(err)
		}
		var total model.ClientPolicyTotal
		if err := database.GetDB().Where("client_id = ?", owner.StableID).First(&total).Error; err != nil || total.RawUpload != raw || total.RawDownload != raw || total.BilledBytes != billedMicros/1000000 {
			t.Fatalf("bulk/Tunnel SQL accounting mismatch: %+v raw=%d billedMicros=%d err=%v", total, raw, billedMicros, err)
		}
		var traffic xray.ClientTraffic
		response := h.api(t, http.MethodGet, "/panel/api/clients/traffic/"+owner.Email, nil)
		if err := json.Unmarshal(response, &traffic); err != nil || traffic.Accounting == nil {
			t.Fatalf("public API did not expose exact accounting: %s %v", response, err)
		}
		accounting := traffic.Accounting
		wantLifetime := xray.ClientPolicyUsage{Upload: strconv.FormatInt(raw, 10), Download: strconv.FormatInt(raw, 10), Billed: bulkPolicyDecimal(billedMicros), Uncertain: "0"}
		wantPeriod := xray.ClientPolicyUsage{Upload: strconv.FormatInt(periodRaw, 10), Download: strconv.FormatInt(periodRaw, 10), Billed: bulkPolicyDecimal(periodBilledMicros), Uncertain: "0"}
		if accounting.ClientID != owner.StableID || accounting.Lifetime != wantLifetime || accounting.Period != wantPeriod || accounting.Remaining == nil || *accounting.Remaining != bulkPolicyDecimal(1000000*1000000-periodBilledMicros) || accounting.PolicyPending || accounting.ResetPending || accounting.AppliedVersion != accounting.DesiredVersion {
			t.Fatalf("public billing/reset/version state mismatch: %+v lifetime=%+v period=%+v", accounting, wantLifetime, wantPeriod)
		}
	}
	flowOne, flowTwo := open(first, "tcp"), open(second, "tcp")
	packets, other := open(second, "udp"), open(sibling, "tcp")
	native := h.snellClient(t, a.SubID).tcp(t, h.target.Addr())
	sshHTTPEcho(t, flowOne, "one")
	sshHTTPEcho(t, flowTwo, "two")
	sshHTTPEcho(t, native, "native")
	packetEcho(packets, bytes.Repeat([]byte{0x81}, 13000))
	sshHTTPEcho(t, other, "sibling")
	initialRaw := int64(13012)
	initialBilled := initialRaw * 2 * 1234567
	check(owners[0], initialRaw, initialBilled, initialRaw, initialBilled)
	check(owners[1], 7, 28000000, 7, 28000000)
	h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, map[string]any{"email": a.Email, "enable": true, "totalGB": 1000000, "policy": model.ClientPolicyOptions{Multiplier: "2.5"}})
	sshHTTPEcho(t, flowOne, "new-rate")
	updatedRaw, updatedBilled := initialRaw+8, initialBilled+40000000
	check(owners[0], updatedRaw, updatedBilled, updatedRaw, updatedBilled)
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	check(owners[0], updatedRaw, updatedBilled, updatedRaw, updatedBilled)
	flowOne, packets, other = open(first, "tcp"), open(second, "udp"), open(sibling, "tcp")
	// TCP connect alone does not establish managed admission. Prove this is
	// a funded live flow before testing that a reset preserves its connection.
	sshHTTPEcho(t, flowOne, "ready")
	updatedRaw += 5
	updatedBilled += 25000000
	check(owners[0], updatedRaw, updatedBilled, updatedRaw, updatedBilled)
	h.api(t, http.MethodPost, "/panel/api/clients/resetTraffic/"+a.Email, service.ClientTrafficResetRequest{ClientID: owners[0].StableID, RequestID: uuid.NewString()})
	check(owners[0], updatedRaw, updatedBilled, 0, 0)
	sshHTTPEcho(t, flowOne, "new")
	packetEcho(packets, []byte("new-udp"))
	check(owners[0], updatedRaw+10, updatedBilled+50000000, 10, 50000000)
	h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, map[string]any{"email": a.Email, "enable": false, "totalGB": 1000000})
	sshHTTPClosed(t, flowOne)
	sshHTTPEcho(t, other, "alive")
	check(owners[1], 12, 48000000, 12, 48000000)
}
