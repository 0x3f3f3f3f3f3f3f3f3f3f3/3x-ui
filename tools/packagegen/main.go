// packagegen hashes cross-built distribution resources without running them.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mhsanaei/3x-ui/v3/internal/distribution"
)

func main() {
	root := flag.String("root", "", "package directory")
	osName := flag.String("os", "linux", "target operating system")
	arch := flag.String("arch", "", "target architecture")
	arm := flag.String("arm", "", "ARM variant")
	revision := flag.String("revision", "", "verified full source revision")
	goVersion := flag.String("go-version", "", "actual Go toolchain")
	nodeVersion := flag.String("node-version", "", "actual Node toolchain")
	flag.Parse()
	if *root == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "packagegen requires --root and no positional arguments")
		os.Exit(1)
	}
	_, err := distribution.Generate(*root, distribution.Target{OS: *osName, Arch: *arch, ARM: *arm}, *revision, map[string]string{"go": *goVersion, "node": *nodeVersion})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
