package clientpolicy

import (
	"errors"
	"testing"
	"time"
)

func TestGrantRenewalPreservesLiveSessionAllowanceAndLimiter(t *testing.T) {
	p := testPolicy("owner")
	p.UploadRate, p.BurstBytes = 100, 10
	e, g := authorityExecutionFixture(t, p, 100, 200*time.Millisecond)
	g.Upload = AuthorityShare{Rate: 10, Burst: 2}
	initial, err := e.InstallAuthorityGrant(g)
	if err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	if err := s.Admit(Upload, 2); err != nil {
		t.Fatal(err)
	}
	challenge, err := e.BeginAuthorityChallenge(g.BootID)
	if err != nil {
		t.Fatal(err)
	}
	r := AuthorityGrantRenewal{BootID: g.BootID, ClientID: g.ClientID, GrantID: g.GrantID, ChallengeID: challenge.ChallengeID, Sequence: 1, LeaseDuration: time.Second}
	deadline, err := e.RenewAuthorityGrant(r)
	if err != nil || !deadline.After(initial.Deadline) {
		t.Fatalf("renewal: %v/%v", deadline, err)
	}
	if retry, err := e.RenewAuthorityGrant(r); err != nil || !retry.Equal(deadline) {
		t.Fatalf("retry extended deadline: %v/%v", retry, err)
	}
	result := make(chan error, 1)
	go func() { result <- s.Admit(Upload, 1) }()
	select {
	case err := <-result:
		t.Fatalf("renewal refilled rate burst: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("renewed stream stopped admitting")
	}
	time.Sleep(130 * time.Millisecond)
	if err := s.Admit(Upload, 1); err != nil {
		t.Fatalf("existing session did not survive original grant deadline: %v", err)
	}
	state, err := e.GetAuthorityGrant(p.ClientID)
	if err != nil || state.Grant != g || state.Usage.RawUpload != 4 || state.Usage.BilledBytes != 4 {
		t.Fatalf("renewal changed grant or billing: %+v/%v", state, err)
	}
	conflict := r
	conflict.LeaseDuration = 900 * time.Millisecond
	if _, err := e.RenewAuthorityGrant(conflict); !errors.Is(err, ErrAuthority) {
		t.Fatalf("conflicting renewal retry: %v", err)
	}
}

func TestExpiredGrantCannotBeReactivatedByRenewal(t *testing.T) {
	e, g := authorityExecutionFixture(t, testPolicy("owner"), 10, 10*time.Millisecond)
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, g.ClientID, nil)
	time.Sleep(25 * time.Millisecond)
	challenge, err := e.BeginAuthorityChallenge(g.BootID)
	if err != nil {
		t.Fatal(err)
	}
	r := AuthorityGrantRenewal{BootID: g.BootID, ClientID: g.ClientID, GrantID: g.GrantID, ChallengeID: challenge.ChallengeID, Sequence: 1, LeaseDuration: time.Second}
	if _, err := e.RenewAuthorityGrant(r); !errors.Is(err, ErrAuthority) {
		t.Fatalf("expired grant renewed: %v", err)
	}
	if err := s.Admit(Upload, 1); err == nil {
		t.Fatal("renewal reactivated expired session")
	}
}

func TestGrantInstallRetryReturnsCommittedCumulativeReceipt(t *testing.T) {
	p := testPolicy("owner")
	p.Multiplier = 1500000
	e, g := authorityExecutionFixture(t, p, 100, time.Second)
	if _, err := e.InstallAuthorityGrant(g); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, p.ClientID, nil)
	if err := s.Admit(Upload, 3); err != nil {
		t.Fatal(err)
	}
	retry, err := e.InstallAuthorityGrant(g)
	if err != nil {
		t.Fatal(err)
	}
	records, err := e.ReadLedger(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	last := records[len(records)-1]
	if retry.Sequence != last.Sequence || retry.Usage != last.Usage {
		t.Fatalf("retry mixed uncommitted usage with old receipt: %+v/%+v", retry, last)
	}
}
