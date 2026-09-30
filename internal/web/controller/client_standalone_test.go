package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
)

func TestClientStandaloneCreateJSONOnEmptyPanel(t *testing.T) {
	cleanup, err := testpg.IsolatePackage("standalone_client_create")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	newHostTestDB(t)
	a := &ClientController{}
	a.xrayService.IsNeedRestartAndSetFalse()
	engine := gin.New()
	engine.POST("/panel/api/clients/add", a.create)
	for _, body := range []string{
		`{"client":{"email":"empty-bindings"},"inboundIds":[]}`,
		`{"client":{"email":"omitted-bindings"}}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/panel/api/clients/add", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		var response entity.Msg
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusOK || !response.Success {
			t.Fatalf("standalone API rejected %s: status=%d body=%s err=%v", body, w.Code, w.Body.String(), err)
		}
	}
	var rows []model.ClientRecord
	if err := database.GetDB().Order("email").Find(&rows).Error; err != nil || len(rows) != 2 {
		t.Fatalf("standalone API did not persist both accounts: %+v %v", rows, err)
	}
	for _, row := range rows {
		if _, err := uuid.Parse(row.StableID); err != nil || !row.Enable || row.Policy != nil || row.UUID != "" || row.Password != "" || row.SubID == "" {
			t.Fatalf("standalone API changed omitted-field defaults: %+v %v", row, err)
		}
	}
	if rows[0].StableID == rows[1].StableID || rows[0].SubID == rows[1].SubID {
		t.Fatal("separate accounts share generated identity")
	}
	if a.xrayService.IsNeedRestartAndSetFalse() {
		t.Fatal("creating unattached accounts requested a core restart")
	}
}
