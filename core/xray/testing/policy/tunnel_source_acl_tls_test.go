package policy_test

import (
	gotls "crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/common/protocol/tls/cert"
)

func TestTunnelSourceACLPreservesVerifiedTLSAndPayloadAccounting(t *testing.T) {
	certificate, err := cert.Generate(nil, cert.Authority(true), cert.DNSNames("acl.test"), cert.KeyUsage(x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment|x509.KeyUsageCertSign))
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM := certificate.ToPEM()
	stream, err := json.Marshal(map[string]any{"network": "raw", "security": "tls", "rawSettings": map[string]any{"header": map[string]any{"type": "none"}}, "tlsSettings": map[string]any{"certificates": []any{map[string]any{"certificate": strings.Split(strings.TrimSpace(string(certPEM)), "\n"), "key": strings.Split(strings.TrimSpace(string(keyPEM)), "\n")}}}})
	if err != nil {
		t.Fatal(err)
	}
	target := tcpEcho(t)
	listen := port(t)
	instance := start(t, fmt.Sprintf(`{"log":{"loglevel":"error"},"clientPolicy":{"policies":[{"clientId":"tls-owner","version":1,"enabled":true,"multiplierMicros":1000000,"burstBytes":65536}]},"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":%d,"clientId":"tls-owner","allowedSourceCidrs":["127.0.0.1/32"]},"streamSettings":%s}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow"}]}}]}`, listen, target.Addr().(*net.TCPAddr).Port, stream))
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid certificate")
	}
	tlsConfig := &gotls.Config{RootCAs: roots, ServerName: "acl.test", MinVersion: gotls.VersionTLS12}
	allowed, err := gotls.DialWithDialer(&net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1")}}, "tcp", fmt.Sprintf("127.0.0.1:%d", listen), tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = allowed.Close() })
	exchange(t, allowed, []byte("tls-ok"))
	denied, err := gotls.DialWithDialer(&net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.2")}}, "tcp", fmt.Sprintf("127.0.0.1:%d", listen), tlsConfig)
	if denied != nil {
		_ = denied.Close()
	}
	if err == nil {
		t.Fatal("denied source completed TLS handshake")
	}
	engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
	snap, err := engine.Snapshot("tls-owner")
	if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 6, RawDownload: 6, BilledBytes: 12}) {
		t.Fatalf("TLS framing or rejected traffic was billed: %+v %v", snap, err)
	}
}
