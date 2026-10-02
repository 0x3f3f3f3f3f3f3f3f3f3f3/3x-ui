package distribution

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestPromotionLockRejectsConcurrentReplacementAndReleasesOwnership(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("paired installation locking requires Linux")
	}
	path := filepath.Join(t.TempDir(), "installed")
	release, err := lockPromotion(path)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := lockPromotion(path); err == nil {
		other()
		release()
		t.Fatal("two replacement processes acquired the same installation")
	}
	release()
	next, err := lockPromotion(path)
	if err != nil {
		t.Fatal("completed replacement retained lock ownership", err)
	}
	next()
}
