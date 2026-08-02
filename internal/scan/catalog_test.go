package scan

import (
	"testing"
	"time"

	"github.com/dappermint/ratatouille/internal/catalog"
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

func TestCatalogItemCarriesStructuredEvidence(t *testing.T) {
	target := catalog.Target{
		ID: "fixture", Name: "fixture", Group: catalog.GroupDeveloper,
		Category: storage.CategoryDeveloper, Risk: catalog.RiskSafe, Recovery: safety.RecoveryTrash,
		Evidence: "the fixture proves it", Qualification: catalog.EvidenceObserved,
		Observations: []catalog.Observation{{Product: "fixture", Version: "1", MacOS: "27", Bytes: 4096, ObservedAt: time.Now()}},
		NotTargets:   []string{"state"},
	}
	item := (Scanner{Home: "/Users/example"}).catalogItem(target, []catalog.Measurement{{Path: "/Users/example/cache", Bytes: 4096}})
	if item.Evidence == nil || item.Evidence.Status != catalog.EvidenceObserved || item.Evidence.Claim != target.Evidence {
		t.Fatalf("evidence = %+v", item.Evidence)
	}
	if len(item.Evidence.Observations) != 1 || len(item.Evidence.LeavesAlone) != 1 {
		t.Fatalf("structured evidence = %+v", item.Evidence)
	}
}
