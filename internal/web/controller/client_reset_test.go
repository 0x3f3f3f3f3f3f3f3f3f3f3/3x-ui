package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientTrafficResetHTTPBatchRetriesDoNotClearNewUsage(t *testing.T) {
	for _, path := range []string{"resetAllTraffics", "bulkResetTraffic"} {
		t.Run(path, func(t *testing.T) {
			dbtest.InitDB(t, filepath.Join(t.TempDir(), "reset-batch.db"))
			db := database.GetDB()
			owner := model.ClientRecord{Email: "batch-http"}
			if err := db.Create(&owner).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&xray.ClientTraffic{Email: owner.Email, Up: 10, Down: 20}).Error; err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			NewClientController(router.Group("/panel/api/clients"))
			for attempt := range 2 {
				request := httptest.NewRequest(http.MethodPost, "/panel/api/clients/"+path, strings.NewReader(`{"emails":["batch-http"],"requestId":"same-http-batch"}`))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				var result struct {
					Success bool `json:"success"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || !result.Success {
					t.Fatalf("batch HTTP reset: %s, %v", response.Body, err)
				}
				var traffic xray.ClientTraffic
				if err := db.First(&traffic, "email = ?", owner.Email).Error; err != nil {
					t.Fatal(err)
				}
				if traffic.Up != int64(attempt*33) || traffic.Down != 0 {
					t.Fatalf("HTTP retry captured a new reset boundary: %+v", traffic)
				}
				if err := db.Model(&traffic).Update("up", 33).Error; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestClientTrafficResetHTTPRejectsInvalidIdentityAndRequestBeforeWriting(t *testing.T) {
	for _, test := range []struct{ name, body, wantError string }{
		{"reused-email", `{"requestId":"retry-a","clientId":"former-owner"}`, "client identity changed"},
		{"unbound-request", `{"requestId":"retry-a"}`, "clientId is required with requestId"},
		{"invalid-request", `{"requestId":"bad\nrequest","clientId":"%s"}`, "client policy ledger rejected inconsistent state"},
		{"malformed-json", `{`, "unexpected EOF"},
		{"empty-legacy-request", ``, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			dbtest.InitDB(t, filepath.Join(t.TempDir(), "reset.db"))
			db := database.GetDB()
			owner := model.ClientRecord{Email: "reset-http"}
			if err := db.Create(&owner).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&owner).Update("enable", false).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&xray.ClientTraffic{Email: owner.Email, Up: 111, Down: 222, Enable: false}).Error; err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			NewClientController(router.Group("/panel/api/clients"))
			body := test.body
			if strings.Contains(body, "%s") {
				body = fmt.Sprintf(body, owner.StableID)
			}
			request := httptest.NewRequest(http.MethodPost, "/panel/api/clients/resetTraffic/reset-http", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var result struct {
				Success bool   `json:"success"`
				Msg     string `json:"msg"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			wantSuccess := test.wantError == ""
			if response.Code != http.StatusOK || result.Success != wantSuccess || !strings.Contains(result.Msg, test.wantError) {
				t.Fatalf("reset accepted a stale or invalid request: HTTP %d %s", response.Code, response.Body)
			}
			var traffic xray.ClientTraffic
			if err := db.First(&traffic, "email = ?", owner.Email).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&owner, owner.Id).Error; err != nil {
				t.Fatal(err)
			}
			if wantSuccess {
				if traffic.Up != 0 || traffic.Down != 0 || !traffic.Enable || !owner.Enable {
					t.Fatalf("empty legacy request lost its existing behavior: %+v, enabled=%t", traffic, owner.Enable)
				}
			} else if traffic.Up != 111 || traffic.Down != 222 || traffic.Enable || owner.Enable {
				t.Fatalf("invalid request changed counters or manual disable: %+v, enabled=%t", traffic, owner.Enable)
			}
		})
	}
}
