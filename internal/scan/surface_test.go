package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/dappermint/ratatouille/internal/storage"
)

func walkTemporaryTree(t *testing.T, root string) (*surfaceWalker, *SurfaceNode) {
	t.Helper()
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("no stat information for the temporary root")
	}
	walker := newSurfaceWalker(context.Background(), storage.DeviceID(stat), root)
	return walker, walker.Walk(root)
}

func assertChildrenSumToParent(t *testing.T, node *SurfaceNode) {
	t.Helper()
	if len(node.Children) == 0 {
		return
	}
	var total int64
	for _, child := range node.Children {
		total += child.Total()
	}
	if total != node.Total() {
		t.Fatalf("%s: children sum to %d, parent holds %d", node.Name, total, node.Total())
	}
	for _, child := range node.Children {
		assertChildrenSumToParent(t, child)
	}
}

func TestSurfaceWalkAccountsEveryByte(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 40; index++ {
		directory := filepath.Join(root, fmt.Sprintf("project-%02d", index))
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		payload := make([]byte, (index+1)*4096)
		if err := os.WriteFile(filepath.Join(directory, "blob.bin"), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "loose.bin"), make([]byte, 8192), 0o600); err != nil {
		t.Fatal(err)
	}

	_, tree := walkTemporaryTree(t, root)
	assertChildrenSumToParent(t, tree)

	measured, err := storage.PathUsage(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Total() != measured.Bytes {
		t.Fatalf("surface walk counted %d, block walk counted %d", tree.Total(), measured.Bytes)
	}
}

func TestSurfaceWalkFoldsTailIntoRemainder(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 60; index++ {
		directory := filepath.Join(root, fmt.Sprintf("small-%02d", index))
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "blob.bin"), make([]byte, 4096), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, tree := walkTemporaryTree(t, root)
	if len(tree.Children) > surfaceKeepPerNode+1 {
		t.Fatalf("kept %d children, expected at most %d", len(tree.Children), surfaceKeepPerNode+1)
	}
	remainder := 0
	for _, child := range tree.Children {
		if child.Kind == NodeRemainder {
			remainder++
		}
	}
	if remainder != 1 {
		t.Fatalf("expected exactly one remainder row, found %d", remainder)
	}
	assertChildrenSumToParent(t, tree)
}

func TestSurfaceWalkCountsHardLinksOnce(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original.bin")
	if err := os.WriteFile(original, make([]byte, 128*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, filepath.Join(root, "linked.bin")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	_, tree := walkTemporaryTree(t, root)
	if tree.Total() >= 256*1024 {
		t.Fatalf("hard linked bytes counted twice: %d", tree.Total())
	}
}

func TestSurfaceWalkRecordsUnreadableTrees(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "blob.bin"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	walker, tree := walkTemporaryTree(t, root)
	if walker.denied.Load() == 0 {
		t.Fatal("an unreadable directory was not counted")
	}
	assertChildrenSumToParent(t, tree)
}

func TestAdoptWalkedTreeNamesTheGap(t *testing.T) {
	tree := &SurfaceNode{Name: "data", Kind: NodeDirectory, Bytes: 100}
	children := adoptWalkedTree(tree, 160, 3)
	var unwalked, unreadable *SurfaceNode
	for _, child := range children {
		switch child.Kind {
		case NodeUnwalked:
			unwalked = child
		case NodeUnreadable:
			unreadable = child
		}
	}
	if unwalked == nil || unwalked.Bytes != 60 {
		t.Fatalf("expected a 60 byte unaccounted row, got %+v", unwalked)
	}
	if unreadable == nil || unreadable.Entries != 3 || unreadable.Total() != 0 {
		t.Fatalf("expected an unreadable row that adds no bytes, got %+v", unreadable)
	}
}

func TestTrimTreePreservesParentTotals(t *testing.T) {
	root := &SurfaceNode{Name: "root", Kind: NodeDirectory}
	for index := 0; index < 30; index++ {
		child := &SurfaceNode{Name: fmt.Sprintf("child-%02d", index), Kind: NodeDirectory, Bytes: int64(index + 1)}
		child.Children = []*SurfaceNode{{Name: "leaf", Kind: NodeDirectory, Bytes: child.Bytes}}
		root.Children = append(root.Children, child)
		root.Bytes += child.Bytes
	}
	trimTree(root, 10)
	assertChildrenSumToParent(t, root)
	if len(root.Children) > 11 {
		t.Fatalf("trim kept %d children for a budget of 10", len(root.Children))
	}
}

func TestContainerAccountingReconciles(t *testing.T) {
	container := storage.Container{
		Ceiling: 1000,
		Free:    100,
		Volumes: []storage.Volume{{InUse: 600}, {InUse: 300}},
	}
	if container.VolumesInUse() != 900 {
		t.Fatalf("volume total is %d", container.VolumesInUse())
	}
	if container.Unattributed() != 0 {
		t.Fatalf("expected a reconciled container, gap is %d", container.Unattributed())
	}
}

func TestHealthFlagsMediaErrorsAndReadOnlyData(t *testing.T) {
	surface := Surface{
		Devices: []storage.StorageDevice{{
			Device:  "disk0",
			Media:   "APPLE SSD",
			Status:  "Verified",
			Metrics: map[string]int64{"MEDIA_ERRORS_0": 4, "AVAILABLE_SPARE": 100, "AVAILABLE_SPARE_THRESHOLD": 10},
		}},
		Containers: []storage.Container{{
			Reference: "disk3",
			Ceiling:   1000,
			Free:      500,
			Volumes:   []storage.Volume{{Device: "disk3s5", Name: "Data", Roles: []string{"Data"}, InUse: 500, MountedAt: "/System/Volumes/Data", ReadOnly: true}},
		}},
	}
	health := EvaluateHealth(surface, "/System/Volumes/Data")
	if health.Level != HealthAlarm {
		t.Fatalf("expected an alarm, got %s", health.Level)
	}
	found := map[string]HealthLevel{}
	for _, signal := range health.Signals {
		found[signal.ID] = signal.Level
	}
	if found["media-errors-disk0"] != HealthAlarm {
		t.Fatalf("media errors were not raised: %v", found)
	}
	if found["read-only-disk3s5"] != HealthAlarm {
		t.Fatalf("a read-only data volume was not raised: %v", found)
	}
}

func TestHealthStaysQuietOnAHealthyMachine(t *testing.T) {
	surface := Surface{
		Devices: []storage.StorageDevice{{
			Device:  "disk0",
			Status:  "Verified",
			Metrics: map[string]int64{"MEDIA_ERRORS_0": 0, "PERCENTAGE_USED": 3, "AVAILABLE_SPARE": 100, "AVAILABLE_SPARE_THRESHOLD": 99},
		}},
		Containers: []storage.Container{{Reference: "disk3", Ceiling: 1000, Free: 500, Volumes: []storage.Volume{{InUse: 500, Roles: []string{"Data"}, MountedAt: "/System/Volumes/Data"}}}},
		Mounts:     []storage.Mount{{Path: "/System/Volumes/Data", Total: 1000, Available: 500}},
		Claimed:    500,
		Walked:     500,
	}
	health := EvaluateHealth(surface, "/System/Volumes/Data")
	if health.Level != HealthOK {
		for _, signal := range health.Signals {
			if signal.Level != HealthOK {
				t.Logf("%s = %s (%s)", signal.ID, signal.Level, signal.Value)
			}
		}
		t.Fatalf("a healthy machine reported %s", health.Level)
	}
}

func TestPathUsageStopsAtMountBoundaries(t *testing.T) {
	root := "/Library/Developer/CoreSimulator/Volumes"
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) == 0 {
		t.Skip("no simulator runtime volume is mounted on this machine")
	}
	measured, err := storage.PathUsage(context.Background(), root)
	if err != nil {
		t.Fatalf("bounded walk failed: %v", err)
	}
	if measured.Crossed == 0 {
		t.Skip("the runtime directory holds no mount point")
	}
	if measured.Bytes > 64*1024*1024 {
		t.Fatalf("a mounted runtime volume leaked into its parent: %d bytes", measured.Bytes)
	}
}
