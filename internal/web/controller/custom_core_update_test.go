package controller

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/web/locale"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

func TestCustomCoreOfficialUpdateHTTPExplainsRefusal(t *testing.T) {
	// Use the shipped English translations without changing global localizers.
	data, err := os.ReadFile("../translation/en-US.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle := i18n.NewBundle(language.AmericanEnglish)
	bundle.RegisterUnmarshalFunc("json", json.Unmarshal)
	if _, err := bundle.ParseMessageFileBytes(data, "en-US.json"); err != nil {
		t.Fatal(err)
	}
	localizer := i18n.NewLocalizer(bundle, "en-US")
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("I18n", func(_ locale.I18nType, key string, _ ...string) string {
			message, err := localizer.Localize(&i18n.LocalizeConfig{MessageID: key})
			if err != nil {
				t.Fatal(err)
			}
			return message
		})
	})
	controller := new(ServerController)
	engine.POST("/panel/api/server/installXray/:version", controller.installXray)
	response := doPanelUpdateReq(t, engine, http.MethodPost, "/panel/api/server/installXray/v26.9.9")
	if response.Success || strings.Contains(strings.ToLower(response.Msg), "success") {
		t.Fatalf("refused replacement must not announce success: %+v", response)
	}
	for _, detail := range []string{"Custom Xray-core", "Snell", "mieru", "SSH", "package"} {
		if !strings.Contains(response.Msg, detail) {
			t.Errorf("refusal %q does not explain %q", response.Msg, detail)
		}
	}
}
