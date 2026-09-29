//go:build linux

package xray

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/trafficcontrol"
	"google.golang.org/grpc"
)

type finalPageServer struct {
	trafficcontrol.UnimplementedTrafficControlServiceServer
	pages    []*trafficcontrol.DrainResponse
	requests []*trafficcontrol.DrainRequest
}

func (*finalPageServer) GetCapabilities(context.Context, *trafficcontrol.Empty) (*trafficcontrol.Capabilities, error) {
	return &trafficcontrol.Capabilities{ApiVersion: 1, BootId: "test-child", Capabilities: []string{"boot-scoped-final-counters-v1"}}, nil
}

func (s *finalPageServer) Drain(_ context.Context, r *trafficcontrol.DrainRequest) (*trafficcontrol.DrainResponse, error) {
	s.requests = append(s.requests, r)
	return s.pages[len(s.requests)-1], nil
}

func pageControlFixture(t *testing.T, server *finalPageServer) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "traffic-pages-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	trafficcontrol.RegisterTrafficControlServiceServer(grpcServer, server)
	go grpcServer.Serve(listener)
	t.Cleanup(grpcServer.Stop)
	return path
}

func TestTrafficControlClientRejectsInconsistentPages(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages []*trafficcontrol.DrainResponse
	}{
		{"foreign boot", []*trafficcontrol.DrainResponse{{BootId: "different", Counters: map[string]int64{"a": 1}}}},
		{"negative counter", []*trafficcontrol.DrainResponse{{BootId: "test-child", Counters: map[string]int64{"a": -1}}}},
		{"empty continuation", []*trafficcontrol.DrainResponse{{BootId: "test-child", NextName: "a"}}},
		{"wrong cursor", []*trafficcontrol.DrainResponse{{BootId: "test-child", Counters: map[string]int64{"a": 1}, NextName: "b"}}},
		{"duplicate page", []*trafficcontrol.DrainResponse{{BootId: "test-child", Counters: map[string]int64{"a": 1}, NextName: "a"}, {BootId: "test-child", Counters: map[string]int64{"a": 2}}}},
		{"byte limit", []*trafficcontrol.DrainResponse{{BootId: "test-child", Counters: map[string]int64{strings.Repeat("a", 1<<20): 1}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &finalPageServer{pages: tc.pages}
			path := pageControlFixture(t, server)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, err := dialTrafficControl(ctx, path, "test-child", os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			defer client.connection.Close()
			if values, err := client.finalCounters(ctx); err == nil || values != nil {
				t.Fatalf("inconsistent snapshot accepted: %v %v", values, err)
			}
		})
	}
}

func TestTrafficControlClientPinsEveryPageAndRejectsWrongPeer(t *testing.T) {
	server := &finalPageServer{pages: []*trafficcontrol.DrainResponse{{BootId: "test-child", Counters: map[string]int64{"a": 1}, NextName: "a"}, {BootId: "test-child", Counters: map[string]int64{"b": 2}}}}
	path := pageControlFixture(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if client, err := dialTrafficControl(ctx, path, "other-child", os.Getpid()); err == nil {
		client.connection.Close()
		t.Fatal("wrong boot accepted")
	}
	if client, err := dialTrafficControl(ctx, path, "test-child", os.Getpid()+1); err == nil {
		client.connection.Close()
		t.Fatal("wrong PID accepted")
	}
	client, err := dialTrafficControl(ctx, path, "test-child", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer client.connection.Close()
	values, err := client.finalCounters(ctx)
	if err != nil || len(values) != 2 || values["a"] != 1 || values["b"] != 2 {
		t.Fatalf("snapshot: %v %v", values, err)
	}
	for i, r := range server.requests {
		if r.ExpectedBootId != "test-child" || r.Limit != 1000 || i == 1 && r.AfterName != "a" {
			t.Fatalf("page request lost fence: %v", r)
		}
	}
}
