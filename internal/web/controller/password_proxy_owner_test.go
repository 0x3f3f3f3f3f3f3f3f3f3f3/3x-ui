package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPasswordProxyOwnerAPIResourceContracts(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
			cleanup, err := testpg.IsolatePackage("password_owner_api_" + string(protocol))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			newHostTestDB(t)
			db := database.GetDB()
			owners := []model.ClientRecord{
				{Email: "first-owner", UUID: uuid.NewString(), Password: "shared-first", Enable: true, Policy: &model.ClientPolicyOptions{Multiplier: "1.5"}},
				{Email: "second-owner", UUID: uuid.NewString(), Password: "shared-second", Enable: true},
			}
			if err := db.Create(&owners).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&xray.ClientTraffic{Email: owners[0].Email, Up: 123, Down: 456}).Error; err != nil {
				t.Fatal(err)
			}
			accounts := []map[string]any{
				{"user": "resource", "pass": "resource-password", "ownerClientId": owners[0].StableID},
				{"user": "RESOURCE", "pass": "alias-password", "ownerClientId": owners[0].StableID},
			}
			settings := map[string]any{"accounts": accounts, "auth": "password"}
			controller := &InboundController{}
			engine := gin.New()
			engine.Use(func(c *gin.Context) { session.SetAPIAuthUser(c, &model.User{Id: 1}) })
			engine.POST("/add", controller.addInbound)
			engine.POST("/update/:id", controller.updateInbound)
			engine.GET("/get/:id", controller.getInbound)
			engine.POST("/import", controller.importInbound)
			command := map[string]any{"protocol": protocol, "port": 24521, "enable": false, "settings": settings}
			response := doHostReq(t, engine, http.MethodPost, "/add", command)
			if !response.Success {
				t.Fatalf("owner selection rejected: %s", response.Msg)
			}
			var inbound model.Inbound
			if err := json.Unmarshal(response.Obj, &inbound); err != nil {
				t.Fatal(err)
			}
			accounts[0]["pass"] = "rotated-resource"
			accounts[1]["ownerClientId"] = owners[1].StableID
			response = doHostReq(t, engine, http.MethodPost, fmt.Sprintf("/update/%d", inbound.Id), command)
			if !response.Success {
				t.Fatalf("resource update rejected: %s", response.Msg)
			}
			response = doHostReq(t, engine, http.MethodGet, fmt.Sprintf("/get/%d", inbound.Id), nil)
			if !response.Success {
				t.Fatalf("owned detail rejected: %s", response.Msg)
			}
			if err := json.Unmarshal(response.Obj, &inbound); err != nil {
				t.Fatal(err)
			}
			var exported map[string]any
			if err := json.Unmarshal([]byte(inbound.Settings), &exported); err != nil {
				t.Fatal(err)
			}
			wantJSON, _ := json.Marshal(accounts)
			gotJSON, _ := json.Marshal(exported["accounts"])
			if string(gotJSON) != string(wantJSON) || exported["clients"] != nil {
				t.Fatalf("export changed resource credentials/ownership: %s", inbound.Settings)
			}
			assertOwners := func() {
				t.Helper()
				for _, want := range owners {
					var got model.ClientRecord
					if err := db.First(&got, want.Id).Error; err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("API changed canonical client: %+v %v", got, err)
					}
				}
				var usage xray.ClientTraffic
				if err := db.First(&usage, "email = ?", owners[0].Email).Error; err != nil || usage.Up != 123 || usage.Down != 456 {
					t.Fatalf("API changed owner history: %+v %v", usage, err)
				}
			}
			assertOwners()
			// Full database recovery carries canonical history; resource imports
			// cannot attach caller-supplied history to an existing owner.
			inbound.Port = 24522
			inbound.ClientStats = []xray.ClientTraffic{{Email: owners[0].Email, Up: 999, Down: 999}}
			data, _ := json.Marshal(&inbound)
			req := httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(url.Values{"data": {string(data)}}.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			var rejected hostEnvelope
			if err := json.Unmarshal(w.Body.Bytes(), &rejected); err != nil || rejected.Success {
				t.Fatalf("import bypassed canonical history guard: %s %v", w.Body.String(), err)
			}
			// A resource-only import explicitly selects existing destination UUIDs.
			inbound.ClientStats = nil
			data, _ = json.Marshal(&inbound)
			req = httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(url.Values{"data": {string(data)}}.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w = httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			var imported hostEnvelope
			if err := json.Unmarshal(w.Body.Bytes(), &imported); err != nil || !imported.Success {
				t.Fatalf("resource-only import lost valid ownership: %s %v", w.Body.String(), err)
			}
			assertOwners()
			for _, bad := range []string{"remote-listener", "client-mirror", "traffic-mirror"} {
				invalid := map[string]any{"protocol": protocol, "port": 24523, "enable": false, "settings": settings}
				switch bad {
				case "remote-listener":
					invalid["nodeId"] = 7
				case "client-mirror":
					invalid["settings"] = map[string]any{"auth": "password", "accounts": accounts, "clients": []model.Client{*owners[0].ToClient()}}
				case "traffic-mirror":
					invalid["clientStats"] = []xray.ClientTraffic{{Email: owners[0].Email, Up: 999}}
				}
				if response := doHostReq(t, engine, http.MethodPost, "/add", invalid); response.Success || !strings.Contains(response.Msg, "invalid password proxy account ownership") {
					t.Fatalf("%s bypassed owner command guard: %+v", bad, response)
				}
			}
			assertOwners()
			accounts[0]["ownerClientId"] = uuid.NewString()
			settingsJSON, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			inbound.Settings = string(settingsJSON)
			inbound.Port = 24523
			data, _ = json.Marshal(&inbound)
			req = httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(url.Values{"data": {string(data)}}.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w = httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			if err := json.Unmarshal(w.Body.Bytes(), &rejected); err != nil || rejected.Success || !strings.Contains(rejected.Msg, "invalid password proxy account ownership") {
				t.Fatalf("import inferred an unknown source owner: %s %v", w.Body.String(), err)
			}
			command["port"] = 24523
			response = doHostReq(t, engine, http.MethodPost, "/add", command)
			if response.Success {
				t.Fatal("unknown source-panel UUID created an owner")
			}
			var count int64
			if err := db.Model(&model.Inbound{}).Count(&count).Error; err != nil || count != 2 {
				t.Fatalf("rejected command/import left rows: %d %v", count, err)
			}
			assertOwners()
		})
	}
}
