package distribution

import (
	"bufio"
	"os/exec"
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

func TestLifecycleLockRemainsOwnedByInstallerChildAfterParentDescriptorCloses(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kernel lock inheritance requires Linux")
	}
	path := filepath.Join(t.TempDir(), "installed.lifecycle")
	file, err := lockPromotionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/bin/sh", "-c", "printf 'ready\\n'; read finish")
	child.ExtraFiles = append(child.ExtraFiles, file)
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	if _, err := bufio.NewReader(output).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if other, err := lockPromotion(path); err == nil {
		other()
		t.Fatal("parent exit released a still-running installer child's lock")
	}
	_, _ = input.Write([]byte("finish\n"))
	_ = input.Close()
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	next, err := lockPromotion(path)
	if err != nil {
		t.Fatal("child completion retained lock", err)
	}
	next()
}
