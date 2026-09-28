package commander

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func ValidatePrivateUnixSocket(path string) error {
	if runtime.GOOS == "windows" || !filepath.IsAbs(path) {
		return fmt.Errorf("client policy requires a private Unix socket")
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("client policy requires a private Unix socket in an existing directory with mode 0700")
	}
	return nil
}
