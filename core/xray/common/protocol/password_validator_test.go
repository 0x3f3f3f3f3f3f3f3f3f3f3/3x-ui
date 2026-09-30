package protocol_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/proxy/socks"
)

func passwordAccount(user, pass string) protocol.Account {
	return &socks.Account{Username: user, Password: pass}
}

func TestPasswordValidatorRevocationAndReadd(t *testing.T) {
	v, err := protocol.NewPasswordValidator(
		map[string]string{"验证-user": "old-password"},
		map[string]string{"验证-user": "stable-owner"},
		map[string]string{"验证-user": "canonical-email"}, 3, passwordAccount,
	)
	if err != nil {
		t.Fatal(err)
	}
	old := v.Authenticate("验证-user", "old-password")
	if old == nil || old.ClientID != "stable-owner" || old.Email != "canonical-email" || old.Level != 3 || v.Authenticate("stable-owner", "old-password") != nil || v.Authenticate("验证-user", "wrong-password") != nil {
		t.Fatalf("password identity did not come from the configured account: %+v", old)
	}
	var closed atomic.Int64
	untrack, err := old.TrackSession(func() { closed.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer untrack()
	if err := v.Remove("canonical-email"); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 1 || v.Authenticate("验证-user", "old-password") != nil || v.GetUser("canonical-email") != nil {
		t.Fatalf("removal left the authenticated credential active: closed=%d", closed.Load())
	}
	if _, err := old.TrackSession(func() {}); !errors.Is(err, protocol.ErrCredentialRevoked) {
		t.Fatalf("old authenticated pointer regained admission: %v", err)
	}
	fresh := &protocol.MemoryUser{Account: passwordAccount("验证-user", "new-password"), ClientID: "stable-owner", Email: "canonical-email"}
	if err := v.Add("验证-user", "new-password", fresh); err != nil {
		t.Fatal(err)
	}
	if v.Authenticate("验证-user", "new-password") != fresh || v.Authenticate("验证-user", "old-password") != nil || v.GetCount() != 1 {
		t.Fatal("replacement did not retain the stable ID with a fresh credential")
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if err := v.Add("after-close", "password", fresh); err == nil || v.Authenticate("验证-user", "new-password") != nil {
		t.Fatal("closed validator accepted a credential")
	}
}

func TestPasswordValidatorAliasesShareOwnerAndRevokeTogether(t *testing.T) {
	v, err := protocol.NewPasswordValidator(
		map[string]string{"first": "one", "second": "two"},
		map[string]string{"first": "owner", "second": "owner"},
		map[string]string{"first": "canonical", "second": "canonical"}, 0, passwordAccount,
	)
	if err != nil {
		t.Fatal(err)
	}
	var closed atomic.Int64
	for _, credential := range []struct{ user, pass string }{{"first", "one"}, {"second", "two"}} {
		user := v.Authenticate(credential.user, credential.pass)
		if user == nil || user.ClientID != "owner" {
			t.Fatal("alias lost its shared owner")
		}
		untrack, err := user.TrackSession(func() { closed.Add(1) })
		if err != nil {
			t.Fatal(err)
		}
		defer untrack()
	}
	if v.GetCount() != 2 || len(v.GetUsers()) != 2 || v.GetUser("canonical") == nil {
		t.Fatal("credential aliases disappeared from management")
	}
	if err := v.Add("third", "three", &protocol.MemoryUser{Account: passwordAccount("third", "three"), ClientID: "another-owner", Email: "canonical"}); err == nil {
		t.Fatal("one canonical email acquired conflicting stable owners")
	}
	if err := v.Remove("canonical"); err != nil || closed.Load() != 2 || v.GetCount() != 0 {
		t.Fatalf("canonical removal left an alias active: error=%v closed=%d count=%d", err, closed.Load(), v.GetCount())
	}
}

func TestPasswordValidatorKeepsLegacyCaseSensitiveAccounts(t *testing.T) {
	v, err := protocol.NewPasswordValidator(map[string]string{"Case": "upper", "case": "lower"}, nil, nil, 0, passwordAccount)
	if err != nil {
		t.Fatal(err)
	}
	if v.Authenticate("Case", "upper") == nil || v.Authenticate("case", "lower") == nil || v.Authenticate("CASE", "upper") != nil || v.Authenticate("Case", "lower") != nil {
		t.Fatal("legacy username/password matching changed")
	}
	if err := v.Remove("CASE"); err == nil {
		t.Fatal("ambiguous case-folded removal revoked the wrong account")
	}
	if err := v.Remove("Case"); err != nil || v.Authenticate("case", "lower") == nil {
		t.Fatalf("exact-case removal affected a distinct legacy account: %v", err)
	}
}

func TestPasswordValidatorRejectsOrphanMetadata(t *testing.T) {
	for _, tc := range []struct{ ids, emails map[string]string }{
		{map[string]string{"absent": "owner"}, nil},
		{nil, map[string]string{"absent": "label"}},
		{nil, map[string]string{"present": "label"}},
	} {
		if _, err := protocol.NewPasswordValidator(map[string]string{"present": "password"}, tc.ids, tc.emails, 0, passwordAccount); err == nil {
			t.Fatal("orphan identity/label metadata was silently discarded")
		}
	}
}

func TestPasswordValidatorAuthenticationRemovalRace(t *testing.T) {
	v, err := protocol.NewPasswordValidator(map[string]string{"user": "password"}, map[string]string{"user": "owner"}, nil, 0, passwordAccount)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for n := 0; n < 100; n++ {
				if user := v.Authenticate("user", "password"); user != nil {
					untrack, err := user.TrackSession(func() {})
					if err == nil {
						untrack()
					} else if !errors.Is(err, protocol.ErrCredentialRevoked) {
						t.Errorf("unexpected credential admission error: %v", err)
					}
				}
			}
		})
	}
	if err := v.Remove("user"); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if v.Authenticate("user", "password") != nil {
		t.Fatal("removed credential authenticated after racing readers completed")
	}
}
