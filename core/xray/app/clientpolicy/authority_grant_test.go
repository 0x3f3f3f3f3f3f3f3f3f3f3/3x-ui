package clientpolicy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xtls/xray-core/common"
)

func authorityExecutionFixture(t *testing.T, p Policy, capacity uint64, duration time.Duration) (*Engine, ExecutionGrant) {
	t.Helper()
	e := configuredAuthorityEngine(t)
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	binding := AuthorityBinding{AuthorityID: "durable-issuer", Generation: 1, NodeID: "node-a"}
	if err := e.BindAuthority(e.Capabilities().BootID, binding); err != nil {
		t.Fatal(err)
	}
	challenge, err := e.BeginAuthorityChallenge(e.Capabilities().BootID)
	if err != nil {
		t.Fatal(err)
	}
	return e, ExecutionGrant{Authority: binding, InstanceID: e.Capabilities().InstanceID, BootID: e.Capabilities().BootID, ClientID: p.ClientID, WindowID: "window-1", PolicyVersion: p.Version, GrantID: "grant-1", Sequence: 1, ChallengeID: challenge.ChallengeID, Capacity: capacity, Upload: AuthorityShare{Unlimited: true}, Download: AuthorityShare{Unlimited: true}, LeaseDuration: duration}
}

func TestConfiguredGrantAdmissionRequiresCurrentBootAndFiniteCapacity(t *testing.T) {
	e := configuredAuthorityEngine(t)
	p := testPolicy("owner")
	p.Multiplier, p.QuotaBytes = 2000000, 200
	if err := e.Apply(p); err != nil {
		t.Fatal(err)
	}
	if session, err := e.Open(context.Background(), Metadata{ClientID: p.ClientID}, nil); !errors.Is(err, ErrRestricted) {
		if session != nil {
			session.Close()
		}
		t.Fatalf("configured core admitted without authority: %v", err)
	}
	binding := AuthorityBinding{AuthorityID: "issuer", Generation: 1, NodeID: "node-a"}
	if err := e.BindAuthority(e.Capabilities().BootID, binding); err != nil {
		t.Fatal(err)
	}
	challenge, err := e.BeginAuthorityChallenge(e.Capabilities().BootID)
	if err != nil {
		t.Fatal(err)
	}
	g := ExecutionGrant{Authority: binding, InstanceID: e.Capabilities().InstanceID, BootID: e.Capabilities().BootID, ClientID: p.ClientID, WindowID: "window", PolicyVersion: 1, GrantID: "bounded", Sequence: 1, ChallengeID: challenge.ChallengeID, Capacity: 40, Upload: AuthorityShare{Unlimited: true}, Download: AuthorityShare{Unlimited: true}, LeaseDuration: time.Second}
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	if err := s.Admit(Upload, 7); err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(Download, 13); err != nil {
		t.Fatal(err)
	}
	first, err := e.GetAuthorityGrant(p.ClientID)
	if err != nil || first.Usage != (Usage{RawUpload: 7, RawDownload: 13, BilledBytes: 40}) {
		t.Fatalf("grant consumption lost directional/multiplier accounting: %+v/%v", first, err)
	}
	if retry, err := e.InstallAuthorityGrant(g); err != nil || retry.Usage != first.Usage || !retry.Deadline.Equal(first.Deadline) {
		t.Fatalf("grant replay refilled capacity/deadline: %+v/%v", retry, err)
	}
	if err := s.Admit(Upload, 1); err == nil {
		t.Fatal("finite grant admitted beyond40 billed bytes")
	}
	if _, err := e.Open(context.Background(), Metadata{ClientID: p.ClientID}, nil); !errors.Is(err, ErrRestricted) {
		t.Fatalf("reconnection bypassed exhausted grant: %v", err)
	}
	wrong := g
	wrong.BootID = "copied-retired-boot"
	if _, err := e.InstallAuthorityGrant(wrong); !errors.Is(err, ErrAuthority) {
		t.Fatalf("wrong boot installed grant: %v", err)
	}
}

func TestGrantReplayCannotRenewLeaseOrRefillBurst(t *testing.T) {
	p := testPolicy("owner")
	p.UploadRate, p.BurstBytes = 100, 10
	e, g := authorityExecutionFixture(t, p, 100, 50*time.Millisecond)
	g.Upload = AuthorityShare{Rate: 10, Burst: 2}
	first, err := e.InstallAuthorityGrant(g)
	if err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	if err := s.Admit(Upload, 2); err != nil {
		t.Fatal(err)
	}
	if retry, err := e.InstallAuthorityGrant(g); err != nil || !retry.Deadline.Equal(first.Deadline) {
		t.Fatalf("duplicate renewed lease: %+v/%v", retry, err)
	}
	result := make(chan error, 1)
	go func() { result <- s.Admit(Upload, 1) }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("duplicate refilled burst or payload continued after expiry")
		}
	case <-time.After(time.Second):
		t.Fatal("expired grant did not release pending admission")
	}
	snapshot, err := e.Snapshot(p.ClientID)
	if err != nil || snapshot.Usage.RawUpload != 2 || snapshot.ActiveSessions != 0 {
		t.Fatalf("expired grant delivered/retained payload: %+v/%v", snapshot, err)
	}
}

func TestLimitedZeroGrantShareIsNotUnlimited(t *testing.T) {
	e, g := authorityExecutionFixture(t, testPolicy("owner"), 10, time.Second)
	g.Upload = AuthorityShare{}
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, g.ClientID, nil)
	if err := s.Admit(Upload, 1); !errors.Is(err, ErrRestricted) {
		t.Fatalf("limited zero upload acted as unlimited: %v", err)
	}
	if err := s.Admit(Download, 1); err != nil {
		t.Fatalf("zero upload incorrectly blocked granted download: %v", err)
	}
}

func TestExpiredChallengeCannotInstallFreshGrant(t *testing.T) {
	e, g := authorityExecutionFixture(t, testPolicy("owner"), 10, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if _, err := e.InstallAuthorityGrant(g); !errors.Is(err, ErrAuthority) {
		t.Fatalf("delayed reply received fresh grant lifetime: %v", err)
	}
	if _, err := e.Open(context.Background(), Metadata{ClientID: g.ClientID}, nil); !errors.Is(err, ErrRestricted) {
		t.Fatalf("expired grant opened admission: %v", err)
	}
}

func TestOlderSealedGrantCannotBeReinstalledAfterReplacement(t *testing.T) {
	e, first := authorityExecutionFixture(t, testPolicy("owner"), 10, time.Second)
	if _, err := e.InstallAuthorityGrant(first); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, first.ClientID, nil)
	if err := s.Admit(Upload, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SealAuthorityGrant(first.ClientID, first.GrantID); err != nil {
		t.Fatal(err)
	}
	challenge, err := e.BeginAuthorityChallenge(e.Capabilities().BootID)
	if err != nil {
		t.Fatal(err)
	}
	next := first
	next.GrantID, next.Sequence, next.ChallengeID = "grant-2", 2, challenge.ChallengeID
	if _, err := e.InstallAuthorityGrant(next); err != nil {
		t.Fatal(err)
	}
	s = openSession(t, e, next.ClientID, nil)
	if err := s.Admit(Upload, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SealAuthorityGrant(next.ClientID, next.GrantID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.InstallAuthorityGrant(first); !errors.Is(err, ErrAuthority) {
		t.Fatalf("older sealed grant replenished capacity after replacement: %v", err)
	}
	if _, err := e.Open(context.Background(), Metadata{ClientID: first.ClientID}, nil); !errors.Is(err, ErrRestricted) {
		t.Fatalf("old grant reopened business admission: %v", err)
	}
}

func TestGrantSealCommitsFractionAndNewBootCannotAcknowledgeOldGrant(t *testing.T) {
	p := testPolicy("owner")
	p.Multiplier = 1500000
	e, g := authorityExecutionFixture(t, p, 50, time.Second)
	store := e.store.(*boltStore)
	path := store.db.Path()
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	if err := s.Admit(Upload, 1); err != nil {
		t.Fatal(err)
	}
	sealed, err := e.SealAuthorityGrant(p.ClientID, g.GrantID)
	if err != nil || !sealed.Sealed || sealed.Usage != (Usage{RawUpload: 1, BilledBytes: 1, Remainder: 500000}) {
		t.Fatalf("seal acknowledged wrong committed boundary: %+v/%v", sealed, err)
	}
	if retry, err := e.SealAuthorityGrant(p.ClientID, g.GrantID); err != nil || retry != sealed {
		t.Fatalf("seal retry changed boundary: %+v/%v", retry, err)
	}
	if err := s.Admit(Download, 1); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("sealed grant retained admission: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	object, err := common.CreateObject(context.Background(), &Config{StateFile: path, InstanceId: g.InstanceID})
	if err != nil {
		t.Fatal(err)
	}
	recovered := object.(*Engine)
	t.Cleanup(func() { _ = recovered.Close() })
	if err := recovered.Start(); err != nil {
		t.Fatal(err)
	}
	if recovered.Capabilities().BootID == g.BootID {
		t.Fatal("recovery copied grant activation boot")
	}
	if _, err := recovered.SealAuthorityGrant(p.ClientID, g.GrantID); !errors.Is(err, ErrAuthority) {
		t.Fatalf("new boot acknowledged stored old grant: %v", err)
	}
	if _, err := recovered.InstallAuthorityGrant(g); !errors.Is(err, ErrAuthority) {
		t.Fatalf("new boot replayed stored old grant: %v", err)
	}
	snapshot, err := recovered.Snapshot(p.ClientID)
	if err != nil || snapshot.Usage != sealed.Usage || snapshot.Reasons&ReasonAuthority == 0 {
		t.Fatalf("recovery discarded fraction or activated stored grant: %+v/%v", snapshot, err)
	}
}

type grantCommitFaultStore struct{ stateStore }

func (s *grantCommitFaultStore) save(storedClient) (uint64, error) {
	return 0, errors.New("injected grant seal commit failure")
}

func TestGrantSealCommitFailureAcknowledgesNoUnusedCapacity(t *testing.T) {
	e, g := authorityExecutionFixture(t, testPolicy("owner"), 50, time.Second)
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, g.ClientID, nil)
	if err := s.Admit(Upload, 1); err != nil {
		t.Fatal(err)
	}
	e.store = &grantCommitFaultStore{stateStore: e.store}
	if sealed, err := e.SealAuthorityGrant(g.ClientID, g.GrantID); !errors.Is(err, ErrStorage) || sealed != (ExecutionGrantState{}) {
		t.Fatalf("failed seal released unused capacity: %+v/%v", sealed, err)
	}
	if err := s.Admit(Upload, 1); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("failed seal left payload admitted: %v", err)
	}
	if _, err := e.GetAuthorityGrant(g.ClientID); !errors.Is(err, ErrStorage) {
		t.Fatalf("failed seal exposed reusable acknowledgement: %v", err)
	}
}
