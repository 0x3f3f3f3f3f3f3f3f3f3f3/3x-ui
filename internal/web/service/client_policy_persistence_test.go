package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestClientPolicySyncPreservesAbsentAndExplicitPolicy(t *testing.T) {
	for _, tt := range []struct {
		name     string
		stored   *model.ClientPolicyOptions
		incoming *model.ClientPolicyOptions
		want     *model.ClientPolicyOptions
	}{
		{name: "absent"},
		{name: "explicit-default", stored: &model.ClientPolicyOptions{}, want: &model.ClientPolicyOptions{}},
		{name: "existing-policy", stored: &model.ClientPolicyOptions{UploadBytesPerSecond: 4096, Multiplier: "2"}, want: &model.ClientPolicyOptions{UploadBytesPerSecond: 4096, Multiplier: "2"}},
		{name: "reset-to-default", stored: &model.ClientPolicyOptions{DownloadBytesPerSecond: 8192, Multiplier: "3"}, incoming: &model.ClientPolicyOptions{}, want: &model.ClientPolicyOptions{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupBulkDB(t)
			client := model.Client{ID: "8c080a27-68b7-461c-b7ba-1d815f934057", Email: "policy-presence", Enable: true, Policy: tt.stored}
			inbound := mkInbound(t, 21270, model.VLESS, clientsSettings(t, []model.Client{client}))
			svc := &ClientService{}
			if err := svc.SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
				t.Fatal(err)
			}
			client.Policy = tt.incoming
			client.ExpiryTime = 1893456000000
			client.Comment = "metadata update"
			if err := svc.SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
				t.Fatal(err)
			}
			var current model.ClientRecord
			if err := database.GetDB().Where("email = ?", client.Email).First(&current).Error; err != nil {
				t.Fatal(err)
			}
			if current.ExpiryTime != client.ExpiryTime || current.Comment != client.Comment {
				t.Fatalf("metadata was not saved: expiry=%d comment=%q", current.ExpiryTime, current.Comment)
			}
			if !sameClientPolicy(current.Policy, tt.want) {
				t.Fatalf("metadata update changed policy presence/value: got %+v, want %+v", current.Policy, tt.want)
			}
		})
	}
}
