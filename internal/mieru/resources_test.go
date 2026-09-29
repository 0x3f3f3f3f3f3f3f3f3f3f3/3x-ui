package mieru

import (
	"bytes"
	"io"
	"strconv"
	"testing"
	"time"

	apimodel "github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/metrics"
	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestNativeCredentialChurnDoesNotRetainDiagnosticUserGroups(t *testing.T) {
	db, ledger, controller := mieruDB(t)
	_, user := mieruUser(t, db, ledger, controller, 1000)
	server := startNative(t, controller, "udp", user)
	target := nativeEcho(t, "udp")
	for range 12 {
		client := officialClient(t, server.Addresses()[0], user)
		conn := wireEcho(t, client, target, []byte("accounted"))
		server.mu.Lock()
		var identity string
		for name := range server.clients {
			identity = name
		}
		server.mu.Unlock()
		if got := metrics.GetMetricsForUser(identity); len(got) != 0 {
			t.Fatalf("native authentication generation retained %d diagnostic time series outside the policy ledger", len(got))
		}
		user.Password = uuid.NewString()
		if err := server.UpdateClients([]Client{user}); err != nil {
			t.Fatal(err)
		}
		requireClosedWire(t, conn)
		if err := client.Stop(); err != nil {
			t.Fatal(err)
		}
	}
	account, err := ledger.Read(t.Context(), user.PolicyID)
	if err != nil || account.Up != 108 || account.Down != 108 || account.Billed != 216 {
		t.Fatalf("credential generations changed durable payload accounting: %+v / %v", account, err)
	}
}

func TestNativePreAcceptSessionsRespectTheServerBound(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			var users []Client
			for range 4 {
				_, user := mieruUser(t, db, ledger, controller, 1000)
				users = append(users, user)
			}
			var bindings []Binding
			for range 5 {
				bindings = append(bindings, Binding{Network: network, Address: "127.0.0.1:0"})
			}
			// Distinct loopback addresses provide independent native packet underlays without fixed ports.
			for i := range bindings {
				bindings[i].Address = "127.0.0." + strconv.Itoa(i+1) + ":0"
			}
			server, err := New(Config{InboundTag: "preaccept-bound", Bindings: bindings, Clients: users}, controller, directTestDial)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			target := nativeEcho(t, "tcp")
			peer := wireEcho(t, officialClient(t, server.Addresses()[0], users[3]), target, []byte("peer"))
			server.mu.Lock()
			locked := true
			defer func() {
				if locked {
					server.mu.Unlock()
				}
			}()
			for i, address := range server.Addresses() {
				mux := clientMux(t, address, users[i%3])
				for range 70 {
					conn := rawNativeSession(t, mux)
					if _, err := conn.Write([]byte{5}); err != nil {
						t.Fatal(err)
					}
				}
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if got := len(server.mux.ExportSessionInfoList().GetItems()); got > maxSessions {
					t.Fatalf("native engine allocated %d sessions before adapter admission; limit is %d", got, maxSessions)
				}
				time.Sleep(5 * time.Millisecond)
			}
			stats := server.mux.ServerResourceStats()
			if stats.PeakSessions != maxSessions || stats.Rejected == 0 {
				t.Fatalf("fixture did not exercise the shared native admission bound: %+v", stats)
			}
			echoExisting(t, peer, target)
			server.mu.Unlock()
			locked = false
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}
			if stats := server.mux.ServerResourceStats(); stats.Sessions != 0 || stats.BufferedBytes != 0 {
				t.Fatalf("shutdown retained resources after admission overload: %+v", stats)
			}
		})
	}
}

func TestNativeClosedSessionsReleaseMetadataBeforeAnotherAdmission(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			_, user := mieruUser(t, db, ledger, controller, 1000)
			server := startNative(t, controller, network, user)
			client := officialClient(t, server.Addresses()[0], user)
			target := nativeEcho(t, "tcp")
			for range 12 {
				conn := wireEcho(t, client, target, []byte("churn"))
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(time.Second)
				for server.mux.ServerResourceStats().Sessions != 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				stats := server.mux.ServerResourceStats()
				if retained := len(server.mux.ExportSessionInfoList().GetItems()); retained != 0 || stats.Sessions != 0 || stats.BufferedBytes != 0 {
					t.Fatalf("finished native session retained metadata after releasing its admission slot: retained=%d stats=%+v", retained, stats)
				}
			}
		})
	}
}

func TestNativePausedAdmissionBoundsPayloadAndResumesWholeStream(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			record, user := mieruUser(t, db, ledger, controller, 1000)
			if err := db.Model(&record).Update("total_gb", 16<<20).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", record.Email).Update("total", 16<<20).Error; err != nil {
				t.Fatal(err)
			}
			server := startNative(t, controller, network, user)
			target := nativeEcho(t, "tcp")
			mux := clientMux(t, server.Addresses()[0], user)
			conn := rawNativeSession(t, mux)
			payload := bytes.Repeat([]byte("managed-buffer"), 128<<10)
			server.mu.Lock()
			locked := true
			defer func() {
				if locked {
					server.mu.Unlock()
				}
			}()
			if _, err := conn.Write(nativeRequest(t, target)); err != nil {
				t.Fatal(err)
			}
			written := make(chan error, 1)
			go func() {
				_, err := conn.Write(payload)
				written <- err
			}()
			deadline := time.Now().Add(3 * time.Second)
			var saturatedAt time.Time
			for {
				stats := server.mux.ServerResourceStats()
				if stats.PeakBufferedBytes > 5*(128<<10) {
					t.Fatalf("one native session exceeded its bounded trees and staging queue: %+v", stats)
				}
				if stats.BufferedBytes >= 128<<10 {
					if saturatedAt.IsZero() {
						saturatedAt = time.Now()
					}
					if time.Since(saturatedAt) >= 300*time.Millisecond {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatalf("fixture never established native receive backpressure: %+v", stats)
				}
				time.Sleep(5 * time.Millisecond)
			}
			server.mu.Unlock()
			locked = false
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			var response apimodel.Response
			if err := response.ReadFromSocks5(conn); err != nil || response.Reply != 0 {
				t.Fatalf("resumed request failed: %+v / %v", response, err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			got := make([]byte, len(payload))
			reader := handshakeReader{conn: conn, deadline: time.Now().Add(10 * time.Second)}
			if n, err := io.ReadFull(reader, got); err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("native resource backpressure lost application payload after %d bytes: %v; %+v", n, err, server.mux.ServerResourceStats())
			}
			if err := <-written; err != nil {
				t.Fatal(err)
			}
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}
			stats := server.mux.ServerResourceStats()
			if stats.Sessions != 0 || stats.BufferedBytes != 0 || stats.PeakBufferedBytes > 5*(128<<10) {
				t.Fatalf("native shutdown retained owned buffers or session slots: %+v", stats)
			}
			account, err := ledger.Read(t.Context(), user.PolicyID)
			if err != nil || account.Up != int64(len(payload)) || account.Down != int64(len(payload)) || account.Billed != 2*int64(len(payload)) {
				t.Fatalf("native queue limits changed payload accounting: %+v / %v", account, err)
			}
		})
	}
}
