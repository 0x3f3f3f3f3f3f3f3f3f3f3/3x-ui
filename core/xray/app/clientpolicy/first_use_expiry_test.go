package clientpolicy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFirstUseExpiryStartsOnlyAfterAdmittedPayload(t *testing.T) {
	e := NewEngine()
	defer e.Close()
	p := testPolicy("first-use")
	p.ExpiresAt = -150
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	session := openSession(t, e, p.ClientID, func() { close(closed) })
	if err := session.Admit(Upload, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if snapshot, err := e.Snapshot(p.ClientID); err != nil || snapshot.Reasons != 0 || snapshot.ActiveSessions != 1 {
		t.Fatalf("unused account started its expiry clock: %+v, %v", snapshot, err)
	}
	if err := session.Admit(Upload, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("first-use expiry did not close the active session without a panel")
	}
	if _, err := e.Open(context.Background(), Metadata{ClientID: p.ClientID}, nil); !errors.Is(err, ErrRestricted) {
		t.Fatalf("expired account reopened: %v", err)
	}
	if snapshot, err := e.Snapshot(p.ClientID); err != nil || snapshot.Reasons != ReasonExpired || snapshot.Usage.RawUpload != 1 {
		t.Fatalf("first-use expiry lost usage or restriction: %+v, %v", snapshot, err)
	}
}

func TestFirstUseExpirySurvivesRestartAndUnrelatedPolicyChanges(t *testing.T) {
	e, path := persistentEngine(t)
	p := testPolicy("first-use-restart")
	p.ExpiresAt = -200
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	session := openSession(t, e, p.ClientID, nil)
	if err := session.Admit(Upload, 1); err != nil {
		t.Fatal(err)
	}
	p.Version++
	p.UploadRate = 100000
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	restored, err := OpenPersistentEngine(path, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.Open(context.Background(), Metadata{ClientID: p.ClientID}, nil); !errors.Is(err, ErrRestricted) {
		t.Fatalf("restart or policy edit restarted the first-use clock: %v", err)
	}
	if snapshot, err := restored.Snapshot(p.ClientID); err != nil || snapshot.Reasons != ReasonExpired || snapshot.Usage.RawUpload != 1 {
		t.Fatalf("restart lost first-use deadline or ledger: %+v, %v", snapshot, err)
	}
}

func TestFirstUseExpiryPersistsBeforeAbruptExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "first-use-crash.db")
	if err := CreateStore(path, "node-1"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestFirstUseExpiryCrashHelper$")
	cmd.Env = append(os.Environ(), "XRAY_FIRST_USE_CRASH_PATH="+path)
	out, err := cmd.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 23 {
		t.Fatalf("first-use child did not reach abrupt exit: %v %s", err, out)
	}
	time.Sleep(100 * time.Millisecond)
	restored, err := OpenPersistentEngine(path, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	snapshot, err := restored.Snapshot("first-use-crash")
	if err != nil || snapshot.Reasons != ReasonExpired || snapshot.UncertainBytes != reservationRawBytes {
		t.Fatalf("abrupt exit lost the admitted expiry boundary: %+v, %v", snapshot, err)
	}
	if _, err := restored.Open(context.Background(), Metadata{ClientID: "first-use-crash"}, nil); !errors.Is(err, ErrRestricted) {
		t.Fatalf("crash reopened the expired client: %v", err)
	}
}

func TestFirstUseExpiryCrashHelper(t *testing.T) {
	path := os.Getenv("XRAY_FIRST_USE_CRASH_PATH")
	if path == "" {
		return
	}
	e, err := OpenPersistentEngine(path, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	p := testPolicy("first-use-crash")
	p.ExpiresAt = -50
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	if err := openSession(t, e, p.ClientID, nil).Admit(Upload, 1); err != nil {
		t.Fatal(err)
	}
	os.Exit(23)
}
