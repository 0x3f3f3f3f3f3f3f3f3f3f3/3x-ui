package ssh

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/common/buf"
)

func TestReverseWriterDeliversFinalPaidQuotaPayload(t *testing.T) {
	engine := clientpolicy.NewEngine()
	defer engine.Close()
	policy := clientpolicy.Policy{ClientID: "reverse-last-payload", Version: 1, Enabled: true, Multiplier: 1000000, QuotaBytes: 2, BurstBytes: 65536}
	if err := engine.Apply(policy); err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	lease, err := engine.Open(context.Background(), clientpolicy.Metadata{ClientID: policy.ClientID}, func() { _ = server.Close() })
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := lease.Admit(clientpolicy.Upload, 1); err != nil {
		t.Fatal(err)
	}
	delivered := make(chan error, 1)
	go func() {
		var raw [1]byte
		_, err := io.ReadFull(client, raw[:])
		if err == nil && raw[0] != 'd' {
			err = io.ErrUnexpectedEOF
		}
		delivered <- err
	}()
	writer := &reverseWriter{writer: server, session: lease, direction: clientpolicy.Download}
	if err := writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte("d"))); err != nil {
		t.Fatal("paid reverse payload closed before delivery", err)
	}
	if err := <-delivered; err != nil {
		t.Fatal("paid reverse byte did not reach peer", err)
	}
	snapshot, err := engine.Snapshot(policy.ClientID)
	if err != nil || snapshot.ActiveSessions != 0 || snapshot.Usage.BilledBytes != 2 {
		t.Fatal("reverse writer retained exhausted session", err)
	}
}
