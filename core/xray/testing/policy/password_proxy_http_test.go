package policy_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/features/inbound"
)

func TestPasswordHTTPKeepAlivePreservesRequestIdentity(t *testing.T) {
	for _, protocol := range []string{"http", "mixed"} {
		for _, removal := range []string{"policy", "credential"} {
			t.Run(protocol+"/"+removal, func(t *testing.T) {
				// This origin deliberately retains the completed response connection,
				// leaving its dispatcher session alive after the next client request.
				origin, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { origin.Close() })
				releaseOrigin := make(chan struct{})
				defer close(releaseOrigin)
				observed := make(chan []byte, 1)
				responseBytes := "HTTP/1.1 200 OK\r\nContent-Length: 6\r\nContent-Type: text/plain\r\n\r\norigin"
				go func() {
					conn, err := origin.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					var raw bytes.Buffer
					request, err := http.ReadRequest(bufio.NewReader(io.TeeReader(conn, &raw)))
					if err != nil {
						return
					}
					if _, err := io.ReadAll(request.Body); err != nil {
						return
					}
					request.Body.Close()
					observed <- bytes.Clone(raw.Bytes())
					io.WriteString(conn, responseBytes)
					<-releaseOrigin
				}()
				target, received := loopbackEchoTarget(t, "tcp")
				authPort := port(t)
				config := passwordProxyConfig(t, protocol, authPort, port(t), target)
				settings := config["inbounds"].([]any)[1].(map[string]any)["settings"].(map[string]any)
				settings["accounts"] = append(settings["accounts"].([]any), map[string]any{"user": "other-user", "pass": "other-password", "clientId": "other", "email": "other-account"})
				policies := config["clientPolicy"].(map[string]any)
				policies["policies"] = append(policies["policies"].([]any), map[string]any{"clientId": "other", "version": 1, "enabled": true, "multiplierMicros": 1500000, "burstBytes": 65536})
				instance := start(t, loopbackJSON(t, config))
				conn := loopbackFlow(t, "tcp", authPort)
				conn.SetDeadline(time.Now().Add(2 * time.Second))
				auth := base64.StdEncoding.EncodeToString([]byte("authenticated-user:test-password"))
				_, err = fmt.Fprintf(conn, "POST http://%s/account-a HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\nProxy-Connection: keep-alive\r\nContent-Length: 4\r\n\r\nbody", origin.Addr(), origin.Addr(), auth)
				if err != nil {
					t.Fatal(err)
				}
				reader := bufio.NewReader(conn)
				response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != 200 || string(body) != "origin" {
					t.Fatalf("plain HTTP response: status=%d body=%q err=%v", response.StatusCode, body, err)
				}
				var raw []byte
				select {
				case raw = <-observed:
				case <-time.After(time.Second):
					t.Fatal("origin did not observe the request")
				}
				if !strings.HasSuffix(string(raw), "body") || bytes.Contains(raw, []byte("Proxy-Authorization")) {
					t.Fatalf("origin received an unexpected rewritten HTTP request: %q", raw)
				}
				engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
				before, err := engine.Snapshot("owner")
				total := uint64(len(raw) + len(responseBytes))
				wantA := clientpolicy.Usage{RawUpload: uint64(len(raw)), RawDownload: uint64(len(responseBytes)), BilledBytes: total * 3 / 2, Remainder: total * 1500000 % 1000000}
				if err != nil || before.Usage != wantA || before.ActiveSessions != 1 {
					t.Fatalf("plain HTTP accounting or retained session: %+v want=%+v err=%v", before, wantA, err)
				}
				auth = base64.StdEncoding.EncodeToString([]byte("other-user:other-password"))
				_, err = fmt.Fprintf(conn, "CONNECT 127.0.0.1:%d HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth)
				if err != nil {
					t.Fatal(err)
				}
				response, err = http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
				if err != nil || response.StatusCode != 200 || reader.Buffered() != 0 {
					t.Fatalf("second account CONNECT: response=%v err=%v buffered=%d", response, err, reader.Buffered())
				}
				conn.SetDeadline(time.Time{})
				exchange(t, conn, []byte("other!"))
				if removal == "policy" {
					policy, _, err := engine.GetClient("owner")
					if err != nil {
						t.Fatal(err)
					}
					policy.Version++
					policy.Enabled = false
					if err := engine.Apply(policy); err != nil {
						t.Fatal(err)
					}
				} else {
					manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
					handler, err := manager.GetHandler(context.Background(), "password")
					if err != nil {
						t.Fatal(err)
					}
					if err := (&command.RemoveUserOperation{Email: "canonical-account"}).ApplyInbound(context.Background(), handler); err != nil {
						t.Fatal(err)
					}
				}
				exchange(t, conn, []byte("alive!"))
				after, err := engine.Snapshot("owner")
				other, otherErr := engine.Snapshot("other")
				if err != nil || after.Usage != wantA || otherErr != nil || other.Usage != (clientpolicy.Usage{RawUpload: 12, RawDownload: 12, BilledBytes: 36}) || received.Load() != 12 {
					t.Fatalf("reused connection changed owner/history: owner=%+v other=%+v err=%v/%v target=%d", after, other, err, otherErr, received.Load())
				}
			})
		}
	}
}
