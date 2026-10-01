//go:build linux

package xray

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func nativeConfigProofProbe(t *testing.T, p *Process) reflect.Value {
	t.Helper()
	method := reflect.ValueOf(p).MethodByName("NativeTrafficConfigProof")
	if !method.IsValid() {
		t.Fatal("running child exposes no immutable startup configuration proof")
	}
	values := method.Call(nil)
	if len(values) != 1 || values[0].Kind() != reflect.Pointer {
		t.Fatal("startup proof does not return an optional detached value")
	}
	return values[0]
}

func nativeProofDigest(t *testing.T, data []byte) string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object any
	if err := decoder.Decode(&object); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

func TestNativeTrafficConfigProofCapturedBeforeFirstPoll(t *testing.T) {
	p, _, address := finalTrafficProcess(t, false)
	if p.trafficSequence != 0 || p.trafficPending != nil || len(p.trafficCursor) != 0 {
		t.Fatal("fixture has already polled before startup-proof assertion")
	}
	proof := nativeConfigProofProbe(t, p)
	if proof.IsNil() {
		t.Fatal("actual configured child has unknown startup evidence")
	}
	logical, err := json.Marshal(p.GetConfig())
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(p.configPath)
	if err != nil {
		t.Fatal(err)
	}
	value := proof.Elem()
	if value.FieldByName("ConfigDigest").String() != nativeProofDigest(t, logical) || value.FieldByName("EffectiveConfigDigest").String() != nativeProofDigest(t, written) || !value.FieldByName("ConfigStable").Bool() {
		t.Fatal("startup proof does not bind both desired and actual written configuration")
	}
	exchangeFinalTraffic(t, address, []byte("proof-before-poll"))
}

func TestNativeTrafficConfigProofCannotRestoreAfterConfigChange(t *testing.T) {
	p, _, _ := finalTrafficProcess(t, false)
	original := p.GetConfig()
	before := nativeConfigProofProbe(t, p)
	if before.IsNil() {
		t.Fatal("configured child lacks startup proof")
	}
	digest := before.Elem().FieldByName("ConfigDigest").String()
	changed := *original
	changed.LogConfig = []byte(`{"loglevel":"warning"}`)
	p.SetConfig(&changed)
	p.SetConfig(original)
	after := nativeConfigProofProbe(t, p)
	if after.IsNil() || after.Elem().FieldByName("ConfigStable").Bool() || after.Elem().FieldByName("ConfigDigest").String() != digest {
		t.Fatal("restoring config rewrote startup identity or erased intervening drift")
	}
	// A caller changing a returned snapshot cannot revive future eligibility.
	after.Elem().FieldByName("ConfigStable").SetBool(true)
	if nativeConfigProofProbe(t, p).Elem().FieldByName("ConfigStable").Bool() {
		t.Fatal("caller changed Process proof through the returned pointer")
	}
}

func TestNativeTrafficConfigProofPreservesCanonicalEquivalentConfig(t *testing.T) {
	p, _, _ := finalTrafficProcess(t, false)
	before := nativeConfigProofProbe(t, p)
	if before.IsNil() {
		t.Fatal("configured child lacks startup proof")
	}
	current := *p.GetConfig()
	current.OutboundConfigs = []byte(`[{ "settings" : { "finalRules" : [{ "ip" : ["127.0.0.1"], "action" : "allow" }] }, "protocol" : "freedom" }]`)
	p.SetConfig(&current)
	after := nativeConfigProofProbe(t, p)
	if after.IsNil() || !after.Elem().FieldByName("ConfigStable").Bool() || after.Elem().FieldByName("ConfigDigest").String() != before.Elem().FieldByName("ConfigDigest").String() {
		t.Fatal("object-key/whitespace changes invalidated equivalent startup configuration")
	}
}

func TestNativeTrafficConfigProofUnknownCommandRemainsUnknown(t *testing.T) {
	p := &Process{startProcessHelper(t, "default-term")}
	if !nativeConfigProofProbe(t, p).IsNil() {
		t.Fatal("direct command acquired fabricated written-config proof")
	}
	p.SetConfig(&Config{})
	if !nativeConfigProofProbe(t, p).IsNil() {
		t.Fatal("later config assignment fabricated startup evidence")
	}
}

func nativeBatchProofProbe(t *testing.T, batch *TrafficBatch) reflect.Value {
	t.Helper()
	field := reflect.ValueOf(batch).Elem().FieldByName("ConfigProof")
	if !field.IsValid() || field.Kind() != reflect.Pointer || field.IsNil() {
		t.Fatal("actual native pending batch has no detached startup configuration proof")
	}
	return field.Elem()
}

func TestNativeTrafficConfigProofPendingAndFinalSnapshots(t *testing.T) {
	p, final, address := finalTrafficProcess(t, false)
	exchangeFinalTraffic(t, address, []byte("first"))
	injected := errors.New("startup-proof commit acknowledgement unavailable")
	var batchID, digest string
	if _, _, err := p.SettleTraffic(func(batch *TrafficBatch) error {
		proof := nativeBatchProofProbe(t, batch)
		batchID, digest = batch.ID, proof.FieldByName("ConfigDigest").String()
		if !proof.FieldByName("ConfigStable").Bool() {
			t.Fatal("unchanged startup has an unstable initial batch")
		}
		return injected
	}); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	changed := *p.GetConfig()
	changed.LogConfig = []byte(`{"loglevel":"warning"}`)
	p.SetConfig(&changed)
	if _, _, err := p.SettleTraffic(func(batch *TrafficBatch) error {
		proof := nativeBatchProofProbe(t, batch)
		if batch.ID != batchID || !proof.FieldByName("ConfigStable").Bool() || proof.FieldByName("ConfigDigest").String() != digest {
			t.Fatal("later config replaced evidence in the original pending retry")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	exchangeFinalTraffic(t, address, []byte("growth"))
	if _, _, err := p.SettleTraffic(func(batch *TrafficBatch) error {
		if nativeBatchProofProbe(t, batch).FieldByName("ConfigStable").Bool() {
			t.Fatal("new batch erased observed configuration drift")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := final.SettleFinalTraffic(ctx, func(batch *TrafficBatch) error {
		proof := nativeBatchProofProbe(t, batch)
		if !batch.Final || proof.FieldByName("ConfigStable").Bool() || proof.FieldByName("ConfigDigest").String() != digest {
			t.Fatal("final drain lost its immutable startup identity or drift flag")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeTrafficConfigProofInstallDetectsPreStartChange(t *testing.T) {
	startup := &Config{LogConfig: []byte(`{"loglevel":"none"}`)}
	written, err := json.Marshal(startup)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := prepareTrafficConfigProof(startup, written)
	if err != nil {
		t.Fatal(err)
	}
	p := &Process{&process{config: startup}}
	p.SetConfig(&Config{LogConfig: []byte(`{"loglevel":"warning"}`)})
	if err := p.startCommandOwned(exec.Command("/bin/sleep", "30"), "", proof); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop() })
	if current := p.NativeTrafficConfigProof(); current == nil || current.ConfigStable || current.ConfigDigest != proof.ConfigDigest {
		t.Fatal("configuration changed between proof preparation and installation without invalidating stability")
	}
}

func TestNativeTrafficConfigProofRestartAndFailedStart(t *testing.T) {
	p, _, _ := finalTrafficProcess(t, false)
	before := p.NativeTrafficConfigProof()
	changed := *p.GetConfig()
	changed.LogConfig = []byte(`{"loglevel":"warning"}`)
	p.SetConfig(&changed)
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	after := p.NativeTrafficConfigProof()
	if after == nil || !after.ConfigStable || after.ConfigDigest == before.ConfigDigest || !before.ConfigStable {
		t.Fatal("new child did not receive fresh startup evidence, or rewrote an old snapshot")
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := p.startCommandOwned(exec.Command("/nonexistent/startup-proof-child"), "", after); err == nil {
		t.Fatal("invalid command unexpectedly started")
	}
	if p.NativeTrafficConfigProof() != nil {
		t.Fatal("failed start left evidence attributed to a running child")
	}
}

func TestNativeTrafficConfigProofPolicyMutationAndConcurrency(t *testing.T) {
	p, _, _ := finalTrafficProcess(t, false)
	original := p.GetConfig()
	if p.CompareAndSetClientPolicy([]byte("stale"), []byte(`{"instanceId":"never-applied"}`)) || !p.NativeTrafficConfigProof().ConfigStable {
		t.Fatal("failed comparison invalidated startup evidence")
	}
	if !p.CompareAndSetClientPolicy(original.ClientPolicy, []byte(`{"instanceId":"applied"}`)) {
		t.Fatal("expected policy comparison to succeed")
	}
	p.SetConfig(original)
	if p.NativeTrafficConfigProof().ConfigStable {
		t.Fatal("acknowledged policy mutation was erased by config restoration")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			p.SetConfig(original)
			_ = p.NativeTrafficConfigProof()
		}
	}()
	for range 100 {
		p.trafficMu.Lock()
		batch, err := p.trafficBatchFromCounters(map[string]int64{"user>>>alice>>>traffic>>>uplink": 1}, false)
		p.trafficMu.Unlock()
		if err != nil || batch.batch.ConfigProof == nil || batch.batch.ConfigProof.ConfigStable {
			t.Fatalf("concurrent snapshot lost sticky evidence: %v", err)
		}
	}
	<-done
}

func TestNativeTrafficConfigProofCanonicalNumbersAndArrays(t *testing.T) {
	digest := func(raw string) string {
		t.Helper()
		value, err := canonicalTrafficConfigDigest([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	a := digest(`{"n":9007199254740992,"items":[1,2]}`)
	if a != digest(`{ "items" : [1,2], "n" : 9007199254740992 }`) || a == digest(`{"n":9007199254740993,"items":[1,2]}`) || a == digest(`{"n":9007199254740992,"items":[2,1]}`) {
		t.Fatal("canonical hashing collapsed distinct integer/array intent or preserved object formatting")
	}
}
