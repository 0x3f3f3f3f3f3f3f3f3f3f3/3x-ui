package runtime

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"

	"github.com/xtls/xray-core/app/clientpolicy"
	"google.golang.org/protobuf/proto"
)

func TestClientMappingEffectivePolicyDigestIgnoresOnlyIdentityAndVersion(t *testing.T) {
	p := &clientpolicy.PolicyConfig{ClientId: "canonical-owner", Version: 7, Enabled: true, MultiplierMicros: 2000000, QuotaBytes: 100, UploadBytesPerSecond: 32, DownloadBytesPerSecond: 64, BurstBytes: 16, ExpiresAt: -60000}
	digest, err := EffectiveClientPolicyDigest(p)
	if err != nil || len(digest) != 64 {
		t.Fatalf("valid effective policy cannot be proven: %q/%v", digest, err)
	}
	local := proto.Clone(p).(*clientpolicy.PolicyConfig)
	local.ClientId, local.Version = "node-local-owner", 1
	if actual, err := EffectiveClientPolicyDigest(local); err != nil || actual != digest || p.ClientId != "canonical-owner" || p.Version != 7 {
		t.Fatal("identity/version normalization changed effective policy or caller")
	}
	for _, field := range []string{"enable", "multiplier", "quota", "upload", "download", "burst", "expiry", "baseline", "remainder"} {
		t.Run(field, func(t *testing.T) {
			changed := proto.Clone(p).(*clientpolicy.PolicyConfig)
			switch field {
			case "enable":
				changed.Enabled = false
			case "multiplier":
				changed.MultiplierMicros++
			case "quota":
				changed.QuotaBytes++
			case "upload":
				changed.UploadBytesPerSecond++
			case "download":
				changed.DownloadBytesPerSecond++
			case "burst":
				changed.BurstBytes++
			case "expiry":
				changed.ExpiresAt--
			case "baseline":
				changed.QuotaBaselineBytes++
			case "remainder":
				changed.QuotaBaselineRemainder++
			}
			if actual, err := EffectiveClientPolicyDigest(changed); err != nil || actual == digest {
				t.Fatalf("effective policy change escaped proof: %q/%v", actual, err)
			}
		})
	}
	for _, policy := range []*clientpolicy.PolicyConfig{nil, {}, {ClientId: "owner", Version: math.MaxUint64, MultiplierMicros: 1000000, BurstBytes: 16}} {
		if digest, err := EffectiveClientPolicyDigest(policy); err == nil || digest != "" {
			t.Fatal("invalid effective policy produced an enrollment proof")
		}
	}
}

func TestNodeClientMappingJSONCanonicalAndBounded(t *testing.T) {
	request := NodeClientMappingRequest{Binding: NodeAuthorityControlBinding{ExpectedInstanceID: "node-source", ExpectedBootID: strings.Repeat("b", 32), AuthorityID: "coordinator", Generation: 1, NodeID: "node-a"}, GlobalClientID: "11111111-1111-4111-8111-111111111111", LocalClientID: "22222222-2222-4222-8222-222222222222", GlobalPolicyVersion: 9007199254740993, LocalPolicyVersion: 1, ExpectedPolicyDigest: strings.Repeat("a", 64)}
	raw, err := json.Marshal(request)
	if err != nil || !bytes.Contains(raw, []byte(`"globalPolicyVersion":"9007199254740993"`)) {
		t.Fatalf("canonical mapping request lost exact version: %s/%v", raw, err)
	}
	result := NodeClientMappingResult{NodeAuthorityControlIdentity: NodeAuthorityControlIdentity{InstanceID: request.Binding.ExpectedInstanceID, BootID: request.Binding.ExpectedBootID, ExecutionRole: request.Binding.Role()}, Mapping: policyauthority.ClientMapping{Authority: policyauthority.Identity{AuthorityID: request.Binding.AuthorityID, Generation: 1}, NodeAnchor: policyauthority.Identity{AuthorityID: "node-anchor", Generation: 9007199254740995}, NodeID: request.Binding.NodeID, SourceID: request.Binding.ExpectedInstanceID, GlobalClientID: request.GlobalClientID, LocalClientID: request.LocalClientID, GlobalPolicyVersion: request.GlobalPolicyVersion, LocalPolicyVersion: request.LocalPolicyVersion, PolicyDigest: request.ExpectedPolicyDigest}}
	response, err := json.Marshal(result)
	if err != nil || !bytes.Contains(response, []byte(`"generation":"9007199254740995"`)) || !bytes.Contains(response, []byte(`"globalPolicyVersion":"9007199254740993"`)) {
		t.Errorf("new mapping evidence lost exact string integers: %s/%v", response, err)
	}
	var decoded NodeClientMappingRequest
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded != request {
		t.Fatalf("mapping request roundtrip: %+v/%v", decoded, err)
	}
	var proof NodeClientMappingResult
	if err := json.Unmarshal(response, &proof); err != nil || proof != result || proof.Validate(request) != nil {
		t.Errorf("mapping result roundtrip: %+v/%v", proof, err)
	}
	cases := map[string][]byte{
		"duplicate":         append([]byte(`{"globalClientId":"`+request.GlobalClientID+`",`), raw[1:]...),
		"escaped-duplicate": append([]byte(`{"globalClient\u0049d":"`+request.GlobalClientID+`",`), raw[1:]...),
		"case-alias":        bytes.Replace(raw, []byte(`"globalClientId"`), []byte(`"GlobalClientId"`), 1),
		"unknown":           append([]byte(`{"untrusted":true,`), raw[1:]...),
		"null":              bytes.Replace(raw, []byte(`"localClientId":"`+request.LocalClientID+`"`), []byte(`"localClientId":null`), 1),
		"leading-zero":      bytes.Replace(raw, []byte(`"localPolicyVersion":"1"`), []byte(`"localPolicyVersion":"01"`), 1),
		"numeric-version":   bytes.Replace(raw, []byte(`"localPolicyVersion":"1"`), []byte(`"localPolicyVersion":1`), 1),
		"missing":           bytes.Replace(raw, []byte(`"localPolicyVersion":"1",`), nil, 1),
		"oversize":          append(append(append([]byte{}, raw[:len(raw)-1]...), bytes.Repeat([]byte(" "), NodeAuthorityMessageLimit)...), '}'),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			var decoded NodeClientMappingRequest
			if err := json.Unmarshal(input, &decoded); err == nil {
				t.Fatal("ambiguous or unbounded enrollment request accepted")
			}
		})
	}
	responseCases := map[string][]byte{
		"duplicate-anchor":   bytes.Replace(response, []byte(`"nodeAnchor":`), []byte(`"nodeAnchor":{"authorityId":"forged","generation":"1"},"nodeAnchor":`), 1),
		"unknown-evidence":   bytes.Replace(response, []byte(`"mapping":{`), []byte(`"mapping":{"untrusted":true,`), 1),
		"numeric-generation": bytes.Replace(response, []byte(`"generation":"9007199254740995"`), []byte(`"generation":9007199254740995`), 1),
		"generation-alias":   bytes.Replace(response, []byte(`"generation":"9007199254740995"`), []byte(`"Generation":"9007199254740995"`), 1),
		"null-anchor":        bytes.Replace(response, []byte(`"nodeAnchor":{"authorityId":"node-anchor","generation":"9007199254740995"}`), []byte(`"nodeAnchor":null`), 1),
		"numeric-version":    bytes.Replace(response, []byte(`"globalPolicyVersion":"9007199254740993"`), []byte(`"globalPolicyVersion":9007199254740993`), 1),
		"wrong-source":       bytes.Replace(response, []byte(`"sourceId":"node-source"`), []byte(`"sourceId":"foreign-source"`), 1),
		"wrong-role":         bytes.Replace(response, []byte(`"mode":"delegated"`), []byte(`"mode":"local"`), 1),
		"uppercase-digest":   bytes.Replace(response, []byte(strings.Repeat("a", 64)), []byte(strings.Repeat("A", 64)), 1),
		"oversize":           append(append(append([]byte{}, response[:len(response)-1]...), bytes.Repeat([]byte(" "), NodeAuthorityMessageLimit)...), '}'),
	}
	for name, input := range responseCases {
		t.Run("response-"+name, func(t *testing.T) {
			var decoded NodeClientMappingResult
			if err := json.Unmarshal(input, &decoded); err == nil {
				t.Fatal("ambiguous or forged enrollment evidence accepted")
			}
		})
	}
}
