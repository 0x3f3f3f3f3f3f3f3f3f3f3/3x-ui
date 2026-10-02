package main

import (
	"runtime/debug"
	"testing"
)

func TestLocalReplacementDevelVersionIsManagedCorrespondingSource(t *testing.T) {
	for _, version := range []string{"", "(devel)"} {
		m := &debug.Module{Path: "github.com/amnezia-vpn/amneziawg-go/v3", Version: "v3.1.20260828", Replace: &debug.Module{Path: "./core/deps/amneziawg-go", Version: version}}
		d := moduleDependency(m)
		if d.Replacement == nil || !d.Replacement.ManagedSource || d.Replacement.Archive != "" {
			t.Fatalf("local compiled replacement treated as downloadable: %+v", d.Replacement)
		}
	}
	d := moduleDependency(&debug.Module{Path: "github.com/apernet/quic-go", Version: "v0.61.1", Sum: "h1:compiled-checksum"})
	if d.ManagedSource {
		t.Fatal("external module wrongly marked as managed source")
	}
}
