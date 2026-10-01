package runtime

import (
	"errors"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestSnellRemovalResolvesFlatCanonicalIdentity(t *testing.T) {
	cfg := &xray.Config{InboundConfigs: []xray.InboundConfig{{Tag: "native", Protocol: "snell", Settings: []byte(`{"version":5,"psk":"native-psk","email":"owner","clientId":"00000000-0000-4000-8000-000000000001","clients":[{"email":"owner","clientId":"forged"}]}`)}}}
	id, err := managedUserIdentity(cfg, xray.UserOp{Protocol: "snell", Tag: "native", Email: "owner"})
	if err != nil || id != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("flat native identity not resolved: %q %v", id, err)
	}
	for _, op := range []xray.UserOp{{Protocol: "snell", Tag: "native", Email: "another-owner"}, {Protocol: "snell", Tag: "missing", Email: "owner"}} {
		if _, err := managedUserIdentity(cfg, op); !errors.Is(err, xray.ErrClientPolicyCapability) {
			t.Fatal("untrusted removal identity was accepted")
		}
	}
}
