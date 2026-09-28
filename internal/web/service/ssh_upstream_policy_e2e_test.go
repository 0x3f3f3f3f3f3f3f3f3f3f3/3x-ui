//go:build linux

package service

import (
	"net"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/sshoutbound"
	"github.com/mhsanaei/3x-ui/v3/internal/testutil/sshdtest"
)

func sshUpstreamPolicyTemplate(t *testing.T, endpoint sshdtest.Endpoint) func(map[string]any) {
	t.Helper()
	_, port, _ := net.SplitHostPort(productionSSHAddress(t))
	t.Setenv("XUI_SSH_UPSTREAM_BRIDGE_PORT", port)
	return func(template map[string]any) {
		outbounds := template["outbounds"].([]any)
		for _, outbound := range outbounds {
			entry := outbound.(map[string]any)
			if entry["protocol"] == "freedom" {
				entry["streamSettings"] = map[string]any{"sockopt": map[string]any{"dialerProxy": "policy-ssh-upstream"}}
			}
		}
		template["outbounds"] = append(outbounds, map[string]any{
			"tag": "policy-ssh-upstream", "protocol": "ssh",
			"settings": sshoutbound.Config{Address: endpoint.Address, Port: endpoint.Port, User: endpoint.User, PrivateKey: endpoint.PrivateKey, HostKey: endpoint.HostKey},
		})
	}
}

func TestSSHUpstreamPolicyRatesAndRestart(t *testing.T) {
	upstream := sshdtest.Start(t)
	testClientPolicyProductionSSHSharedRates(t, sshUpstreamPolicyTemplate(t, upstream))
	if count := upstream.AuthenticatedConnections(t); count != 16 {
		t.Fatalf("expected 8 forwarded channels before and after restart through real OpenSSH; authenticated upstream connections=%d", count)
	}
}

func TestSSHUpstreamPolicyQuotaAndLifecycle(t *testing.T) {
	upstream := sshdtest.Start(t)
	testSSHInboundProductionXrayLifecycle(t, sshUpstreamPolicyTemplate(t, upstream))
	if count := upstream.AuthenticatedConnections(t); count < 1 {
		t.Fatal("managed ingress traffic bypassed the real SSH upstream")
	} else {
		t.Logf("actual OpenSSH upstream authenticated %d forwarded channels", count)
	}
}

func TestSSHUpstream_Postgres(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"runtime", TestSSHOutboundRunsThroughProductionXray},
		{"rates", TestSSHUpstreamPolicyRatesAndRestart},
		{"quota", TestSSHUpstreamPolicyQuotaAndLifecycle},
	} {
		t.Run(test.name, func(t *testing.T) { managedUsagePostgresSchema(t); test.run(t) })
	}
}
