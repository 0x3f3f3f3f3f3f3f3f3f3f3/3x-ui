package clientpolicy

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xtls/xray-core/common"
)

func configuredAuthorityEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	if err := CreateStore(path, "authority-node"); err != nil {
		t.Fatal(err)
	}
	object, err := common.CreateObject(context.Background(), &Config{StateFile: path, InstanceId: "authority-node"})
	if err != nil {
		t.Fatal(err)
	}
	e := object.(*Engine)
	t.Cleanup(func() { _ = e.Close() })
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestConfiguredBootNonceCannotBeCopiedWithExecutionEpoch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "original.db")
	if err := CreateStore(path, "same-source"); err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, name := range []string{"copy-a", "copy-b", "copy-c"} {
		copyPath := filepath.Join(dir, name+".db")
		if err := os.WriteFile(copyPath, image, 0600); err != nil {
			t.Fatal(err)
		}
		object, err := common.CreateObject(context.Background(), &Config{StateFile: copyPath, InstanceId: "same-source"})
		if err != nil {
			t.Fatal(err)
		}
		e := object.(*Engine)
		t.Cleanup(func() { _ = e.Close() })
		caps := e.Capabilities()
		decoded, err := hex.DecodeString(caps.BootID)
		if err != nil || len(decoded) != 16 || seen[caps.BootID] || caps.Epoch != 1 || caps.InstanceID != "same-source" {
			t.Fatalf("copied state reused/omitted fresh boot nonce: %+v/%v", caps, err)
		}
		seen[caps.BootID] = true
	}
}

func TestAuthorityChallengeDeadlinePrecedesDelayedGrantReply(t *testing.T) {
	e := configuredAuthorityEngine(t)
	caps := e.Capabilities()
	if _, err := e.BeginAuthorityChallenge("retired-boot"); !errors.Is(err, ErrAuthority) {
		t.Fatalf("wrong boot created challenge: %v", err)
	}
	before := time.Now()
	challenge, err := e.BeginAuthorityChallenge(caps.BootID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.authorityDeadline(challenge.ChallengeID, time.Second)
	if err != nil || first.Before(before.Add(time.Second)) || first.After(time.Now().Add(time.Second)) {
		t.Fatalf("deadline was not born before grant reply: %v/%v", first, err)
	}
	time.Sleep(20 * time.Millisecond)
	second, err := e.authorityDeadline(challenge.ChallengeID, time.Second)
	if err != nil || !second.Equal(first) {
		t.Fatalf("replayed challenge restarted lease lifetime: %v/%v", second, err)
	}
	if _, err := e.authorityDeadline(challenge.ChallengeID, time.Millisecond); !errors.Is(err, ErrAuthority) {
		t.Fatalf("late reply received a fresh lifetime: %v", err)
	}
	if _, err := e.authorityDeadline(challenge.ChallengeID, MaxAuthorityLeaseDuration+time.Nanosecond); !errors.Is(err, ErrAuthority) {
		t.Fatalf("excessive lease accepted: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.BeginAuthorityChallenge(caps.BootID); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("closed core created challenge: %v", err)
	}
}

func TestAuthorityChallengeBacklogIsBoundedAndUnbilled(t *testing.T) {
	e := configuredAuthorityEngine(t)
	if err := e.Apply(testPolicy("owner")); err != nil {
		t.Fatal(err)
	}
	before, err := e.Snapshot("owner")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < maxAuthorityChallenges; i++ {
		challenge, err := e.BeginAuthorityChallenge(e.Capabilities().BootID)
		if err != nil || seen[challenge.ChallengeID] {
			t.Fatalf("challenge collision/failure: %+v/%v", challenge, err)
		}
		seen[challenge.ChallengeID] = true
	}
	if _, err := e.BeginAuthorityChallenge(e.Capabilities().BootID); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("unbounded challenge backlog: %v", err)
	}
	after, err := e.Snapshot("owner")
	if err != nil || after.Usage != before.Usage || after.Sequence != before.Sequence {
		t.Fatalf("control challenge changed committed traffic: %+v/%v", after, err)
	}
}
