package routedbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestBridgeRefusesAnonymousDowngradeWithoutSendingCredentials(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	id := uuid.NewString()
	b, err := New("managed", netip.MustParseAddrPort(l.Addr().String()), []ClientBinding{{PolicyID: id, Email: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		reader := bufio.NewReader(conn)
		if _, err := reader.ReadString('\n'); err != nil {
			done <- err
			return
		}
		var greeting [3]byte
		if _, err := io.ReadFull(reader, greeting[:]); err != nil {
			done <- err
			return
		}
		if greeting != [3]byte{5, 1, 2} {
			done <- errors.New("bridge offered anonymous access")
			return
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			done <- err
			return
		}
		_, err = reader.ReadByte()
		done <- err
	}()
	conn, err := b.DialTCP(context.Background(), id, netip.MustParseAddrPort("127.0.0.2:34567"), "route.invalid", 443)
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "did not require credential authentication") {
		t.Fatalf("anonymous backend was accepted: %v", err)
	}
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("bridge continued after authentication downgrade: %v", err)
	}
}

func TestBridgeCancellationClosesStalledHandshake(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	id := uuid.NewString()
	b, err := New("managed", netip.MustParseAddrPort(l.Addr().String()), []ClientBinding{{PolicyID: id, Email: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		conn, err := b.DialTCP(ctx, id, netip.MustParseAddrPort("[::1]:45678"), "route.invalid", 443)
		if conn != nil {
			_ = conn.Close()
		}
		result <- err
	}()
	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(conn)
	header, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(header, "PROXY TCP6 ::1 ::1 45678 ") {
		t.Fatalf("IPv6 authenticated source was lost: %q, %v", header, err)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("stalled handshake did not fail closed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled bridge handshake outlived one second")
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatalf("cancellation left the backend socket open: %v", err)
	}
}

func TestBridgeSeparatesBillingLevelWithoutOverwritingExistingPolicy(t *testing.T) {
	b, err := New("managed", netip.MustParseAddrPort("127.0.0.1:30001"), []ClientBinding{{PolicyID: uuid.NewString(), Email: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &xray.Config{
		Policy:         json_util.RawMessage(`{"levels":{"0":{"connIdle":31,"statsUserUplink":true},"4294967295":{"connIdle":47}}}`),
		InboundConfigs: []xray.InboundConfig{{Tag: "native", Port: 30002, Settings: json_util.RawMessage(`{"userLevel":4294967294}`)}},
	}
	if err := b.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Levels map[string]struct {
			Idle int  `json:"connIdle"`
			Up   bool `json:"statsUserUplink"`
			Down bool `json:"statsUserDownlink"`
		}
	}
	if err := json.Unmarshal(cfg.Policy, &policy); err != nil {
		t.Fatal(err)
	}
	var inbound struct {
		Level uint32 `json:"userLevel"`
	}
	if err := json.Unmarshal(cfg.InboundConfigs[1].Settings, &inbound); err != nil {
		t.Fatal(err)
	}
	separate, exists := policy.Levels["4294967293"]
	if !exists || inbound.Level != 4294967293 || separate.Idle != 31 || separate.Up || separate.Down || !policy.Levels["0"].Up || policy.Levels["4294967295"].Idle != 47 {
		t.Fatalf("bridge changed a native policy or duplicated its billing: %+v, %+v", policy, inbound)
	}
	before, _ := json.Marshal(cfg)
	if err := b.Apply(cfg); !errors.Is(err, ErrConfig) {
		t.Fatalf("bridge reused an occupied listener: %v", err)
	}
	after, _ := json.Marshal(cfg)
	if !bytes.Equal(before, after) {
		t.Fatal("failed bridge application partially changed the Xray configuration")
	}
}
