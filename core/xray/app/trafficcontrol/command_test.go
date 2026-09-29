package trafficcontrol_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/xtls/xray-core/app/commander"
	"github.com/xtls/xray-core/app/proxyman"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	statsapp "github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/app/trafficcontrol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/stats"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func privateCore(t *testing.T, withStats bool) (*core.Instance, []trafficcontrol.TrafficControlServiceClient) {
	t.Helper()
	dir, err := os.MkdirTemp("", "drain-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	config := &core.Config{App: []*serial.TypedMessage{
		serial.ToTypedMessage(&proxyman.InboundConfig{}), serial.ToTypedMessage(&proxyman.OutboundConfig{}),
	}}
	if withStats {
		config.App = append(config.App, serial.ToTypedMessage(&statsapp.Config{}))
	}
	var endpoints []string
	for _, name := range []string{"one.sock", "two.sock"} {
		endpoint := filepath.Join(dir, name)
		endpoints = append(endpoints, endpoint)
		config.App = append(config.App, serial.ToTypedMessage(&commander.Config{Tag: name, Listen: endpoint, Service: []*serial.TypedMessage{serial.ToTypedMessage(&trafficcontrol.Config{})}}))
	}
	s, err := core.New(config)
	if err != nil {
		t.Fatalf("private control construction: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	var clients []trafficcontrol.TrafficControlServiceClient
	for _, path := range endpoints {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("socket permissions: %v %v", info, err)
		}
		conn, err := grpc.NewClient("unix://"+path, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		clients = append(clients, trafficcontrol.NewTrafficControlServiceClient(conn))
	}
	return s, clients
}

func TestPrivateTrafficDrainRPCSharesBootAndFinalResult(t *testing.T) {
	s, clients := privateCore(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := clients[0].GetCapabilities(ctx, &trafficcontrol.Empty{})
	if err != nil || first.GetBootId() == "" || first.GetApiVersion() != 1 || !slices.Contains(first.GetCapabilities(), "boot-scoped-final-counters-v1") {
		t.Fatalf("capabilities: %v %v", first, err)
	}
	second, err := clients[1].GetCapabilities(ctx, &trafficcontrol.Empty{})
	if err != nil || first.GetBootId() != second.GetBootId() {
		t.Fatalf("services disagree on child identity: %v %v", second, err)
	}
	if _, err := clients[0].Drain(ctx, &trafficcontrol.DrainRequest{ExpectedBootId: "different-child"}); status.Code(err) != codes.Aborted {
		t.Fatalf("wrong child: %v", err)
	}
	m := s.GetFeature(stats.ManagerType()).(stats.Manager)
	counter, err := m.RegisterCounter("user>>>alice>>>traffic>>>uplink")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := stats.BeginIO(counter)
	if err != nil {
		t.Fatalf("wrong child closed statistics: %v", err)
	}
	counter.Add(712)
	stats.EndIO(lease)
	for _, client := range clients {
		final, err := client.Drain(ctx, &trafficcontrol.DrainRequest{ExpectedBootId: first.BootId})
		if err != nil || final.GetBootId() != first.BootId || final.GetCounters()["user>>>alice>>>traffic>>>uplink"] != 712 {
			t.Fatalf("final accounting: %v %v", final, err)
		}
	}
	if _, err := stats.BeginIO(counter); err == nil {
		t.Fatal("business accounting reopened after final reply")
	}
}

func TestPrivateTrafficDrainRPCRejectsNoopStats(t *testing.T) {
	_, clients := privateCore(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	capabilities, err := clients[0].GetCapabilities(ctx, &trafficcontrol.Empty{})
	if err != nil || len(capabilities.GetCapabilities()) != 0 {
		t.Fatalf("noop advertised accounting: %v %v", capabilities, err)
	}
	if _, err := clients[0].Drain(ctx, &trafficcontrol.DrainRequest{ExpectedBootId: capabilities.GetBootId()}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("noop drain: %v", err)
	}
}

func TestTrafficControlRejectsNonPrivateTransport(t *testing.T) {
	s, err := core.New(&core.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	object, err := core.CreateObject(s, &trafficcontrol.Config{})
	if err != nil {
		t.Fatal(err)
	}
	service := object.(trafficcontrol.TrafficControlServiceServer)
	for _, ctx := range []context.Context{context.Background(), peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}})} {
		if _, err := service.GetCapabilities(ctx, &trafficcontrol.Empty{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("capabilities accepted public peer: %v", err)
		}
		if _, err := service.Drain(ctx, &trafficcontrol.DrainRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("drain accepted public peer: %v", err)
		}
	}
	for _, path := range []string{"", "127.0.0.1:10085", "@abstract-control"} {
		_, err := core.CreateObject(s, &commander.Config{Tag: "control", Listen: path, Service: []*serial.TypedMessage{serial.ToTypedMessage(&trafficcontrol.Config{})}})
		if err == nil {
			t.Fatalf("private service accepted %q", path)
		}
	}
}

func TestPrivateTrafficDrainPagesLargeSnapshot(t *testing.T) {
	s, clients := privateCore(t, true)
	m := s.GetFeature(stats.ManagerType()).(stats.Manager)
	const users = 100001
	for i := range users {
		for _, direction := range []string{"uplink", "downlink"} {
			counter, err := m.RegisterCounter(fmt.Sprintf("user>>>customer-%06d@example.invalid>>>traffic>>>%s", i, direction))
			if err != nil {
				t.Fatal(err)
			}
			counter.Add(int64(i + 1))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	caps, err := clients[0].GetCapabilities(ctx, &trafficcontrol.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, 2*users)
	after := ""
	for page := 0; ; page++ {
		response, err := clients[page%2].Drain(ctx, &trafficcontrol.DrainRequest{ExpectedBootId: caps.BootId, AfterName: after})
		if err != nil {
			t.Fatalf("default RPC client could not read final page %d: %v", page, err)
		}
		if response.BootId != caps.BootId || len(response.Counters) > 1000 || proto.Size(response) > 1<<20 {
			t.Fatalf("invalid page: count=%d size=%d", len(response.Counters), proto.Size(response))
		}
		for name, value := range response.Counters {
			if name <= after || seen[name] || value <= 0 {
				t.Fatalf("duplicate, stale or invalid counter %q=%d", name, value)
			}
			seen[name] = true
		}
		if response.NextName == "" {
			break
		}
		if response.NextName <= after {
			t.Fatal("page cursor did not advance")
		}
		after = response.NextName
	}
	if len(seen) != 2*users {
		t.Fatalf("final snapshot lost counters: %d", len(seen))
	}
}

func TestPrivateTrafficDrainPageValidationAndByteLimit(t *testing.T) {
	s, clients := privateCore(t, true)
	m := s.GetFeature(stats.ManagerType()).(stats.Manager)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	caps, err := clients[0].GetCapabilities(ctx, &trafficcontrol.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients[0].Drain(ctx, &trafficcontrol.DrainRequest{ExpectedBootId: caps.BootId, Limit: 1001}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("oversize page accepted: %v", err)
	}
	if _, err := clients[0].Drain(ctx, &trafficcontrol.DrainRequest{ExpectedBootId: caps.BootId, AfterName: "unknown-first-cursor"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown initial cursor accepted: %v", err)
	}
	for i := range 20 {
		counter, err := m.RegisterCounter(fmt.Sprintf("%02d-%s", i, strings.Repeat("x", 65536)))
		if err != nil {
			t.Fatal(err)
		}
		lease, err := stats.BeginIO(counter)
		if err != nil {
			t.Fatalf("invalid request closed business: %v", err)
		}
		counter.Add(int64(i + 1))
		stats.EndIO(lease)
	}
	after := ""
	count := 0
	pages := 0
	for {
		response, err := clients[0].Drain(ctx, &trafficcontrol.DrainRequest{ExpectedBootId: caps.BootId, AfterName: after})
		if err != nil {
			t.Fatal(err)
		}
		if proto.Size(response) > 1<<20 {
			t.Fatalf("page exceeded byte budget: %d", proto.Size(response))
		}
		count += len(response.Counters)
		pages++
		if response.NextName == "" {
			break
		}
		after = response.NextName
	}
	if count != 20 || pages < 2 {
		t.Fatalf("byte pagination lost counters: count=%d pages=%d", count, pages)
	}
	if _, err := clients[0].Drain(ctx, &trafficcontrol.DrainRequest{ExpectedBootId: caps.BootId, AfterName: "unknown-cursor"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid cursor accepted: %v", err)
	}
	first, err := clients[0].Drain(ctx, &trafficcontrol.DrainRequest{ExpectedBootId: caps.BootId, Limit: 1})
	if err != nil || len(first.GetCounters()) != 1 || first.GetNextName() == "" {
		t.Fatalf("one-counter page: %v", err)
	}
}
