package outbound_test

import (
	"context"
	"fmt"
	"io"
	gonet "net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/policy"
	"github.com/xtls/xray-core/app/proxyman"
	. "github.com/xtls/xray-core/app/proxyman/outbound"
	"github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	core "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/outbound"
	featurestats "github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/proxy/freedom"
)

func TestInterfaces(t *testing.T) {
	_ = outbound.Handler(new(Handler))
	_ = outbound.Manager(new(Manager))
}

const xrayKey core.XrayKey = 1

func TestOutboundWithAndWithoutStatCounters(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			v, err := core.New(&core.Config{App: []*serial.TypedMessage{
				serial.ToTypedMessage(&stats.Config{}),
				serial.ToTypedMessage(&policy.Config{System: &policy.SystemPolicy{Stats: &policy.SystemPolicy_Stats{OutboundUplink: enabled, OutboundDownlink: enabled}}}),
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			ctx := context.WithValue(context.Background(), xrayKey, v)
			ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{}})
			h, err := NewHandler(ctx, &core.OutboundHandlerConfig{Tag: "tag", ProxySettings: serial.ToTypedMessage(&freedom.Config{FinalRules: []*freedom.FinalRuleConfig{{Action: freedom.RuleAction_Allow}}})})
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			listener, err := gonet.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			conn, err := h.(*Handler).Dial(ctx, net.TCPDestination(net.LocalHostIP, net.Port(listener.Addr().(*gonet.TCPAddr).Port)))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			peer, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			_ = peer.SetDeadline(time.Now().Add(time.Second))
			if _, err := conn.Write([]byte("abc")); err != nil {
				t.Fatal(err)
			}
			payload := make([]byte, 3)
			if _, err := io.ReadFull(peer, payload); err != nil || string(payload) != "abc" {
				t.Fatalf("uplink transfer = %q %v", payload, err)
			}
			if _, err := peer.Write([]byte("xyz")); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(conn, payload); err != nil || string(payload) != "xyz" {
				t.Fatalf("downlink transfer = %q %v", payload, err)
			}
			manager := v.GetFeature(featurestats.ManagerType()).(*stats.Manager)
			for _, direction := range []string{"uplink", "downlink"} {
				counter := manager.GetCounter("outbound>>>tag>>>traffic>>>" + direction)
				if !enabled {
					if counter != nil {
						t.Fatalf("disabled %s accounting registered a counter", direction)
					}
					continue
				}
				if counter == nil || counter.Value() != 3 {
					t.Fatalf("enabled %s accounting did not count exactly three bytes: %v", direction, counter)
				}
			}
		})
	}
}

func TestTagsCache(t *testing.T) {
	test_duration := 10 * time.Second
	threads_num := 50
	delay := 10 * time.Millisecond
	tags_prefix := "node"

	tags := sync.Map{}
	counter := atomic.Uint64{}

	ohm, err := New(context.Background(), &proxyman.OutboundConfig{})
	if err != nil {
		t.Error("failed to create outbound handler manager")
	}
	config := &core.Config{
		App: []*serial.TypedMessage{},
	}
	v, _ := core.New(config)
	v.AddFeature(ohm)
	ctx := context.WithValue(context.Background(), xrayKey, v)

	var stop_add_rm atomic.Bool
	wg_add_rm := sync.WaitGroup{}
	addHandlers := func() {
		defer wg_add_rm.Done()
		for !stop_add_rm.Load() {
			time.Sleep(delay)
			idx := counter.Add(1)
			tag := fmt.Sprintf("%s%d", tags_prefix, idx)
			cfg := &core.OutboundHandlerConfig{
				Tag:           tag,
				ProxySettings: serial.ToTypedMessage(&freedom.Config{FinalRules: []*freedom.FinalRuleConfig{{Action: freedom.RuleAction_Allow}}}),
			}
			if h, err := NewHandler(ctx, cfg); err == nil {
				if err := ohm.AddHandler(ctx, h); err == nil {
					// t.Log("add handler:", tag)
					tags.Store(tag, nil)
				} else {
					t.Error("failed to add handler:", tag)
				}
			} else {
				t.Error("failed to create handler:", tag)
			}
		}
	}

	rmHandlers := func() {
		defer wg_add_rm.Done()
		for !stop_add_rm.Load() {
			time.Sleep(delay)
			tags.Range(func(key interface{}, value interface{}) bool {
				if _, ok := tags.LoadAndDelete(key); ok {
					// t.Log("remove handler:", key)
					ohm.RemoveHandler(ctx, key.(string))
					return false
				}
				return true
			})
		}
	}

	selectors := []string{tags_prefix}
	wg_get := sync.WaitGroup{}
	var stop_get atomic.Bool
	getTags := func() {
		defer wg_get.Done()
		for !stop_get.Load() {
			time.Sleep(delay)
			_ = ohm.Select(selectors)
			// t.Logf("get tags: %v", tag)
		}
	}

	for i := 0; i < threads_num; i++ {
		wg_add_rm.Add(2)
		go rmHandlers()
		go addHandlers()
		wg_get.Add(1)
		go getTags()
	}

	time.Sleep(test_duration)
	stop_add_rm.Store(true)
	wg_add_rm.Wait()
	stop_get.Store(true)
	wg_get.Wait()
}
