package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dappermint/ratatouille/internal/exitcode"
)

func TestRootModeIsExplicit(t *testing.T) {
	rootful, args := extractRootFlag([]string{"scan", "--json", "--root"})
	if !rootful || strings.Join(args, " ") != "scan --json" {
		t.Fatalf("root args = %v, %#v", rootful, args)
	}
	if err := validateRootMode(true, 501); err == nil {
		t.Fatal("root mode accepted a non-root uid")
	} else if exitcode.Code(err) != exitcode.NeedsRoot {
		t.Fatalf("root refusal exit = %d", exitcode.Code(err))
	}
	if err := validateRootMode(true, 0); err != nil {
		t.Fatalf("root mode rejected uid 0: %v", err)
	}
}

func TestDebugFlagIsGlobal(t *testing.T) {
	debug, args := extractDebugFlag([]string{"scan", "--json", "--debug"})
	if !debug || strings.Join(args, " ") != "scan --json" {
		t.Fatalf("debug args = %v, %#v", debug, args)
	}
}

func TestJSONEnvelopeHasStableSchemaAndKind(t *testing.T) {
	var output bytes.Buffer
	if err := writeJSON(&output, "fixture", map[string]int{"count": 2}); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Schema int            `json:"schema"`
		Kind   string         `json:"kind"`
		Data   map[string]int `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Schema != 2 || decoded.Kind != "fixture" || decoded.Data["count"] != 2 {
		t.Fatalf("envelope = %+v", decoded)
	}
}
