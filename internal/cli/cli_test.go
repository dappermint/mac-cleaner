package cli

import (
	"strings"
	"testing"
)

func TestRootModeIsExplicit(t *testing.T) {
	rootful, args := extractRootFlag([]string{"scan", "--json", "--root"})
	if !rootful || strings.Join(args, " ") != "scan --json" {
		t.Fatalf("root args = %v, %#v", rootful, args)
	}
	if err := validateRootMode(true, 501); err == nil {
		t.Fatal("root mode accepted a non-root uid")
	}
	if err := validateRootMode(true, 0); err != nil {
		t.Fatalf("root mode rejected uid 0: %v", err)
	}
}
