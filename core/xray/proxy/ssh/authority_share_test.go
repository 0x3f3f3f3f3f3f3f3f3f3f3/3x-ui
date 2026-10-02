package ssh

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

func TestReverseWriterUsesGrantedDirectionalBurst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := clientpolicy.CreateStore(path, "reverse-source"); err != nil {
		t.Fatal(err)
	}
	object, err := common.CreateObject(context.Background(), &clientpolicy.Config{StateFile: path, InstanceId: "reverse-source"})
	if err != nil {
		t.Fatal(err)
	}
	e := object.(*clientpolicy.Engine)
	defer e.Close()
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	p := clientpolicy.Policy{ClientID: "owner", Version: 1, Enabled: true, Multiplier: 1500000, BurstBytes: 64, UploadRate: 1024, DownloadRate: 1024}
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	binding := clientpolicy.AuthorityBinding{AuthorityID: "issuer", Generation: 1, NodeID: "node"}
	boot := e.Capabilities().BootID
	if err := e.BindAuthority(boot, binding); err != nil {
		t.Fatal(err)
	}
	challenge, err := e.BeginAuthorityChallenge(boot)
	if err != nil {
		t.Fatal(err)
	}
	grant := clientpolicy.ExecutionGrant{Authority: binding, InstanceID: "reverse-source", BootID: boot, ClientID: p.ClientID, WindowID: "window", PolicyVersion: 1, GrantID: "grant", Sequence: 1, ChallengeID: challenge.ChallengeID, Capacity: 512, Upload: clientpolicy.AuthorityShare{Rate: 1024, Burst: 8}, Download: clientpolicy.AuthorityShare{Rate: 1024, Burst: 4}, LeaseDuration: time.Second}
	if _, err := e.InstallAuthorityGrant(grant); err != nil {
		t.Fatal(err)
	}
	lease, err := e.Open(context.Background(), clientpolicy.Metadata{ClientID: p.ClientID}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	payload := []byte("ssh-reverse-stream-larger-than-assigned-burst")
	for _, direction := range []clientpolicy.Direction{clientpolicy.Upload, clientpolicy.Download} {
		var delivered bytes.Buffer
		writer := &reverseWriter{writer: &delivered, session: lease, direction: direction}
		if err := buf.Copy(buf.NewReader(bytes.NewReader(payload)), writer); err != nil {
			t.Fatalf("direction %v: %v", direction, err)
		}
		if !bytes.Equal(delivered.Bytes(), payload) {
			t.Fatalf("direction %v lost or reordered bytes", direction)
		}
	}
	snapshot, err := e.Snapshot(p.ClientID)
	if err != nil || snapshot.Usage.RawUpload != uint64(len(payload)) || snapshot.Usage.RawDownload != uint64(len(payload)) || snapshot.Usage.BilledBytes != uint64(len(payload))*3 {
		t.Fatalf("SSH reverse accounting: %+v/%v", snapshot, err)
	}
}
