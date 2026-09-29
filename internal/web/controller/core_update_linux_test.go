//go:build linux

package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/web/locale"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

type coreUpdateRecorder struct{ *httptest.ResponseRecorder }

func (r coreUpdateRecorder) SetWriteDeadline(time.Time) error { return nil }

func TestManagedCoreRoutesReportContainerFailureInRequestedLanguage(t *testing.T) {
	newHostTestDB(t)
	t.Setenv("XUI_IN_DOCKER", "true")
	if err := locale.InitLocalizer(os.DirFS(".."), &service.SettingService{}); err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.Use(locale.LocalizerMiddleware())
	(&ServerController{}).initRouter(engine.Group("/panel/api/server"))
	for _, lang := range []string{"en-US", "zh-CN", "uk-UA"} {
		for _, endpoint := range []string{"getManagedCoreReleases", "installXray/fixture"} {
			t.Run(lang+"/"+endpoint, func(t *testing.T) {
				method := http.MethodGet
				if strings.HasPrefix(endpoint, "installXray") {
					method = http.MethodPost
				}
				req := httptest.NewRequest(method, "/panel/api/server/"+endpoint, nil)
				req.Header.Set("Accept-Language", lang)
				out := httptest.NewRecorder()
				engine.ServeHTTP(coreUpdateRecorder{out}, req)
				var envelope hostEnvelope
				if err := json.Unmarshal(out.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				want := "update the managed fork container image through your container runtime"
				failure := "Managed core update failed"
				if lang == "zh-CN" {
					want = "请通过容器运行时更新此 fork 的镜像"
					failure = "托管核心更新失败"
				}
				if out.Code != http.StatusOK || envelope.Success || !strings.Contains(envelope.Msg, want) {
					t.Fatalf("container failure was not localized: %d %+v", out.Code, envelope)
				}
				if method == http.MethodPost && (!strings.Contains(envelope.Msg, failure) || strings.Contains(envelope.Msg, "successfully")) {
					t.Fatalf("failure response has the wrong outcome title: %q", envelope.Msg)
				}
			})
		}
	}
}
