package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/app/clientpolicy"
)

func TestAuthorityResetEvidenceMustMatchCommittedBoundary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		baseline policyauthority.Usage
		explicit bool
		wantErr  bool
	}{
		{"exact-delayed-boundary", policyauthority.Usage{RawUpload: 10, RawDownload: 2, BilledBytes: 10, Remainder: 500000}, true, false},
		{"conflicting-upload", policyauthority.Usage{RawUpload: 5, RawDownload: 2, BilledBytes: 10, Remainder: 500000}, true, true},
		{"conflicting-download", policyauthority.Usage{RawUpload: 10, RawDownload: 1, BilledBytes: 10, Remainder: 500000}, true, true},
		{"conflicting-billed", policyauthority.Usage{RawUpload: 10, RawDownload: 2, BilledBytes: 5, Remainder: 500000}, true, true},
		{"conflicting-fraction", policyauthority.Usage{RawUpload: 10, RawDownload: 2, BilledBytes: 10, Remainder: 250000}, true, true},
		{"legacy-exact-boundary", policyauthority.Usage{RawUpload: 10, RawDownload: 2, BilledBytes: 10, Remainder: 500000}, false, false},
		{"legacy-conflicting-boundary", policyauthority.Usage{RawUpload: 10, RawDownload: 2, BilledBytes: 5, Remainder: 500000}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "0957528d-17e2-475a-a1c4-18d3c54b807f"
			reset := &model.ClientPolicyReset{Id: 1, ClientID: id, RequestID: "reset-proof", InstanceID: "proof-source", Epoch: 1, Sequence: 1, RawUpload: 10, RawDownload: 2, BilledBytes: 10, Remainder: 500000, PolicyVersion: 2}
			policy := clientpolicy.Policy{ClientID: id, Version: 2, Enabled: true, Multiplier: 1000000, BurstBytes: 65536, QuotaBytes: 100, QuotaBaselineBytes: 10, QuotaBaselineRemainder: 500000}
			digest := sha256.Sum256([]byte(id + "/" + reset.RequestID))
			encoded, err := json.Marshal(authorityPolicyEvidence{Schema: 1, SourceID: reset.InstanceID, Policy: policy, Reset: reset})
			if err != nil {
				t.Fatal(err)
			}
			change := policyauthority.Change{Request: policyauthority.ChangeRequest{ClientID: id, RequestID: "desired:2", Policy: authorityPolicy(policy, "reset:"+hex.EncodeToString(digest[:])), Reset: true, HasResetBaseline: tc.explicit, Evidence: string(encoded)}}
			if tc.explicit {
				change.Request.ResetBaseline = tc.baseline
				// Applying a pending reset later must retain the earlier boundary.
				change.UsageBoundary = policyauthority.Usage{RawUpload: 20, RawDownload: 2, BilledBytes: 20}
			} else {
				change.UsageBoundary = tc.baseline
			}
			proof, err := decodeAuthorityEvidence(change, reset.InstanceID)
			if tc.wantErr {
				if !errors.Is(err, ErrClientPolicyLedger) {
					t.Fatalf("accepted conflicting reset history: %+v %v", proof, err)
				}
			} else if err != nil || proof.Reset == nil || *proof.Reset != *reset {
				t.Fatalf("lost valid protected boundary: %+v %v", proof, err)
			}
		})
	}
}
