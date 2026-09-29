package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

func TestResponseDeadlineSurvivesGzipAndAllowsLongOperation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ResponseControlMiddleware(), gzip.Gzip(gzip.DefaultCompression))
	engine.POST("/owned-long-operation", func(c *gin.Context) {
		if !strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
			t.Error("fixture did not exercise the gzip writer")
		}
		if err := SetResponseWriteDeadline(c, time.Now().Add(time.Second)); err != nil {
			t.Error(err)
			c.Status(http.StatusInternalServerError)
			return
		}
		time.Sleep(80 * time.Millisecond)
		c.String(http.StatusOK, "operation complete")
	})
	server := httptest.NewUnstartedServer(engine)
	server.Config.WriteTimeout = 20 * time.Millisecond
	server.Start()
	defer server.Close()
	client := server.Client()
	client.Timeout = 2 * time.Second
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/owned-long-operation", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("operation outlived the ordinary HTTP write deadline without extending it: %v", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(data) != "operation complete" {
		t.Fatalf("long-operation response = %d %q, %v", response.StatusCode, data, err)
	}
}
