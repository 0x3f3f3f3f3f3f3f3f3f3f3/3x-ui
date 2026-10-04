package clientpolicy

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestPaidPayloadCompletionPreservesControlledAuthorityHandoff(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		name := "before-policy-advance"
		if advanced {
			name = "after-policy-advance"
		}
		t.Run(name, func(t *testing.T) {
			p := testPolicy("paid-handoff")
			e, grant := authorityExecutionFixture(t, p, 100, 2*time.Second)
			if _, err := e.InstallAuthorityGrant(grant); err != nil {
				t.Fatal(err)
			}
			var closed atomic.Bool
			s := openSession(t, e, p.ClientID, func() { closed.Store(true) })
			finish, err := s.AdmitPayload(Upload, 1)
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := e.PauseAuthorityGrant(p.ClientID, grant.GrantID)
			if err != nil || !sealed.Sealed || sealed.Usage != (Usage{RawUpload: 1, BilledBytes: 1}) {
				t.Fatalf("original paid boundary: %+v/%v", sealed, err)
			}
			p.Version, p.Multiplier = 2, 2000000
			if advanced {
				if err := e.Apply(p); err != nil {
					t.Fatal(err)
				}
			}
			finish()
			finish()
			got, err := e.Snapshot(p.ClientID)
			if err != nil || closed.Load() || got.ActiveSessions != 1 || got.Reasons != ReasonAuthority || got.Usage != (Usage{RawUpload: 1, BilledBytes: 1}) {
				t.Fatalf("paid completion terminated controlled handoff: %+v/%v closed=%v", got, err, closed.Load())
			}
			if err := e.Apply(p); err != nil {
				t.Fatal(err)
			}
			challenge, err := e.BeginAuthorityChallenge(grant.BootID)
			if err != nil {
				t.Fatal(err)
			}
			grant.GrantID, grant.Sequence, grant.PolicyVersion, grant.ChallengeID = "grant-2", 2, 2, challenge.ChallengeID
			if _, err := e.InstallAuthorityGrant(grant); err != nil {
				t.Fatal(err)
			}
			if err := s.Admit(Download, 1); err != nil {
				t.Fatal("replacement did not resume the original session", err)
			}
			got, err = e.Snapshot(p.ClientID)
			if err != nil || closed.Load() || got.ActiveSessions != 1 || got.Reasons != 0 || got.Usage != (Usage{RawUpload: 1, RawDownload: 1, BilledBytes: 3}) {
				t.Fatalf("replacement changed paid history: %+v/%v", got, err)
			}
		})
	}
}
