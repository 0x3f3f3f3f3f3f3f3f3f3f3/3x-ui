package sub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	miClient "github.com/enfein/mieru/v3/apis/client"
	apicommon "github.com/enfein/mieru/v3/apis/common"
	pb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/web/controller"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestMieruHTTPExportRealCoreLifecycle(t *testing.T) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the built native core")
	}
	for _, transport := range []string{"TCP", "UDP"} {
		t.Run(transport, func(t *testing.T) {
			cleanup, err := testpg.IsolatePackage("mieru_http_" + transport)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			seedSubDB(t)
			dir := t.TempDir()
			t.Setenv("XUI_BIN_FOLDER", dir)
			t.Setenv("XUI_LOG_FOLDER", dir)
			if err := os.Symlink(binary, filepath.Join(dir, xray.GetBinaryName())); err != nil {
				t.Fatal(err)
			}
			freePort := func() int {
				t.Helper()
				l, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				port := l.Addr().(*net.TCPAddr).Port
				_ = l.Close()
				return port
			}
			apiPort := freePort()
			template := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"api","services":["HandlerService","StatsService","RoutingService"]},"inbounds":[{"tag":"api","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"address":"127.0.0.1"}}],"outbounds":[{"protocol":"freedom","tag":"direct","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}],"routing":{"rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"}]},"stats":{},"policy":{"levels":{"0":{"statsUserUplink":true,"statsUserDownlink":true}}}}`, apiPort)
			if err := database.GetDB().Create(&model.Setting{Key: "xrayTemplateConfig", Value: template}).Error; err != nil {
				t.Fatal(err)
			}
			svc := &service.XrayService{}
			restore := service.SetXrayProcessForTest(nil)
			previousManager := panelruntime.GetManager()
			panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{APIEndpoint: svc.GetXrayAPIEndpoint, SetNeedRestart: svc.SetToNeedRestart, ManagedChange: svc.ReconcileManagedChange}))
			service.StartTrafficWriter()
			t.Cleanup(func() {
				_ = svc.StopXray()
				service.StopTrafficWriter()
				restore()
				panelruntime.SetManager(previousManager)
				svc.IsNeedRestartAndSetFalse()
			})
			target, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = target.Close() })
			go func() {
				for {
					c, err := target.Accept()
					if err != nil {
						return
					}
					go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
				}
			}()
			router := gin.New()
			router.Use(func(c *gin.Context) { session.SetAPIAuthUser(c, &model.User{Id: 1}) })
			controller.NewInboundController(router.Group("/panel/api/inbounds"))
			controller.NewClientController(router.Group("/panel/api/clients"))
			NewSUBController(router.Group("/"), WithSUBEncryption(false))
			api := func(method, path string, payload any) json.RawMessage {
				t.Helper()
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(method, path, bytes.NewReader(raw))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				var envelope struct {
					Success bool            `json:"success"`
					Msg     string          `json:"msg"`
					Obj     json.RawMessage `json:"obj"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || w.Code != 200 || !envelope.Success {
					t.Fatalf("public API %s %s: %d %s %v", method, path, w.Code, w.Body.String(), err)
				}
				return envelope.Obj
			}
			add := func(protocol, tag, settings string, port int) model.Inbound {
				t.Helper()
				raw := api(http.MethodPost, "/panel/api/inbounds/add", map[string]any{"protocol": protocol, "tag": tag, "listen": "127.0.0.1", "port": port, "enable": true, "settings": settings, "streamSettings": "{}"})
				var inbound model.Inbound
				if err := json.Unmarshal(raw, &inbound); err != nil {
					t.Fatal(err)
				}
				return inbound
			}
			listener := add("mieru", "native", fmt.Sprintf(`{"transport":%q,"mtu":1400,"clients":[]}`, transport), freePort())
			tunnel := add("tunnel", "shared", fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, target.Addr().(*net.TCPAddr).Port), freePort())
			a := model.Client{Email: "http-owner", SubID: "http-owner-sub", Enable: true, TotalGB: 10000, MieruUsername: "独立-user", MieruPassword: "独立-password", Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
			b := model.Client{Email: "http-sibling", SubID: "http-sibling-sub", Enable: true, MieruUsername: "sibling-user", MieruPassword: "sibling-password"}
			api(http.MethodPost, "/panel/api/clients/add", service.ClientCreatePayload{Client: a, InboundIds: []int{listener.Id, tunnel.Id}})
			api(http.MethodPost, "/panel/api/clients/add", service.ClientCreatePayload{Client: b, InboundIds: []int{listener.Id}})
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			officialProfile := func(subID string) *pb.ClientProfile {
				t.Helper()
				req := httptest.NewRequest(http.MethodGet, "/sub/"+subID+"?format=mieru", nil)
				req.Host = "127.0.0.1"
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				config := &pb.ClientConfig{}
				if w.Code != 200 {
					t.Fatalf("public official export: %d %s", w.Code, w.Body.String())
				}
				if err := protojson.Unmarshal(w.Body.Bytes(), config); err != nil {
					t.Fatal(err)
				}
				if len(config.Profiles) != 1 {
					t.Fatalf("unexpected exported profiles: %v", config)
				}
				return config.Profiles[0]
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			dial := func(subID string) net.Conn {
				t.Helper()
				profile := officialProfile(subID)
				client := miClient.NewClient()
				if err := client.Store(&miClient.ClientConfig{Profile: profile, Resolver: apicommon.HostMapResolver{Hosts: map[string]net.IP{"localhost": net.IPv4(127, 0, 0, 1)}}}); err != nil {
					t.Fatal(err)
				}
				if err := client.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = client.Stop() })
				conn, err := client.DialContext(ctx, target.Addr())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				return conn
			}
			echo := func(c net.Conn, payload string) {
				t.Helper()
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				if _, err := io.WriteString(c, payload); err != nil {
					t.Fatal(err)
				}
				reply := make([]byte, len(payload))
				if _, err := io.ReadFull(c, reply); err != nil || string(reply) != payload {
					t.Fatalf("actual native target reply: %q %v", reply, err)
				}
				_ = c.SetDeadline(time.Time{})
			}
			closed := func(c net.Conn) {
				t.Helper()
				_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
				_, err := c.Read(make([]byte, 1))
				if err == nil {
					t.Fatal("revoked flow remained open")
				}
				var networkError net.Error
				if errors.As(err, &networkError) && networkError.Timeout() {
					t.Fatalf("revoked flow not closed: %v", err)
				}
			}
			first, other := dial(a.SubID), dial(b.SubID)
			shared, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = shared.Close() })
			echo(first, "native")
			echo(other, "sibling")
			echo(shared, "shared")
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			var owner model.ClientRecord
			if err := database.GetDB().Where("email = ?", a.Email).First(&owner).Error; err != nil {
				t.Fatal(err)
			}
			var total model.ClientPolicyTotal
			if err := database.GetDB().Where("client_id = ?", owner.StableID).First(&total).Error; err != nil {
				t.Fatal(err)
			}
			if total.RawUpload != 12 || total.RawDownload != 12 || total.BilledBytes != 48 {
				t.Fatalf("HTTP native/Tunnel exact shared ledger: %+v", total)
			}
			a.MieruPassword = "rotated-password"
			api(http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			closed(first)
			echo(other, "after-rotation")
			echo(shared, "alive")
			current := dial(a.SubID)
			echo(current, "rotated")
			detail := api(http.MethodGet, "/panel/api/clients/get/"+a.Email, nil)
			var view struct {
				Client model.ClientRecord `json:"client"`
			}
			if err := json.Unmarshal(detail, &view); err != nil {
				t.Fatal(err)
			}
			if view.Client.StableID != owner.StableID || view.Client.MieruUsername != a.MieruUsername || view.Client.MieruPassword != a.MieruPassword {
				t.Fatal("public read changed native credentials or canonical owner")
			}
			exported := api(http.MethodGet, "/panel/api/clients/export", nil)
			var portable []service.ClientCreatePayload
			if err := json.Unmarshal(exported, &portable); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range portable {
				if item.Client.Email == a.Email {
					found = item.Client.MieruPassword == a.MieruPassword && len(item.InboundIds) == 2
				}
			}
			if !found {
				t.Fatal("public backup export lost pair or membership")
			}
			api(http.MethodPost, "/panel/api/clients/import", map[string]any{"data": string(exported)})
			echo(current, "reimport")
			echo(other, "preserved")
			a.ExpiryTime = time.Now().Add(750 * time.Millisecond).UnixMilli()
			api(http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			closed(current)
			closed(shared)
			echo(other, "after-expiry")
			a.ExpiryTime = 0
			api(http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			enabled := dial(a.SubID)
			echo(enabled, "renewed")
			a.Enable = false
			api(http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			closed(enabled)
			echo(other, "after-disable")
			a.Enable = true
			api(http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			quotaFlow := dial(a.SubID)
			echo(quotaFlow, "enabled")
			a.TotalGB = 1
			api(http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			closed(quotaFlow)
			echo(other, "after-quota")
			a.TotalGB = 10000
			api(http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			deleted := dial(a.SubID)
			echo(deleted, "quota-renewed")
			api(http.MethodPost, "/panel/api/clients/del/"+a.Email, nil)
			closed(deleted)
			echo(other, "after-delete")
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			reopened := dial(b.SubID)
			echo(reopened, "restart")
			api(http.MethodPost, fmt.Sprintf("/panel/api/inbounds/del/%d", listener.Id), nil)
			closed(reopened)
			if transport == "TCP" {
				l, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", listener.Port))
				if err != nil {
					t.Fatal(err)
				}
				_ = l.Close()
			} else {
				l, err := net.ListenPacket("udp4", fmt.Sprintf("127.0.0.1:%d", listener.Port))
				if err != nil {
					t.Fatal(err)
				}
				_ = l.Close()
			}
		})
	}
}
