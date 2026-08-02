package catalog

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

const xcodeDocumentationRoot = "/Library/Developer/Xcode/DocumentationCache"

func xcodeDocumentationTarget() Target {
	return Target{
		ID: "xcode-old-documentation-indexes", Name: "old Xcode documentation indexes", Group: GroupDeveloper,
		Category: storage.CategoryDeveloper, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
		Detail: "superseded documentation search indexes, keeping the newest",
		Paths: []PathSpec{Resolver(func(context.Context, Env) []string {
			return staleXcodeDocumentation(xcodeDocumentationRoot)
		})},
		Guards: []Guard{
			NeedsRoot(), ProcessNotRunning("Xcode", "xcodebuild", "testmanagerd"),
			{Name: "still older than the retained documentation index", Allow: func(_ context.Context, _ Env, path string) (bool, string) {
				for _, stale := range staleXcodeDocumentation(filepath.Dir(path)) {
					if stale == filepath.Clean(path) {
						return true, ""
					}
				}
				return false, "documentation index retention changed"
			}},
		},
		MinBytes: 16 * mib,
		Evidence: "only physical DeveloperDocumentation*.index siblings older than the newest index are selected, and the ordering is rebuilt before removal",
		NotTargets: []string{
			"the newest documentation index, symlinks, Xcode applications, SDKs, toolchains, device support, projects, and documentation cache entries with another name",
		},
	}
}

func staleXcodeDocumentation(root string) []string {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	type entry struct {
		path string
		info os.FileInfo
	}
	var indexes []entry
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, candidate := range entries {
		name := candidate.Name()
		if !strings.HasPrefix(name, "DeveloperDocumentation") || !strings.HasSuffix(name, ".index") || candidate.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, infoErr := candidate.Info()
		if infoErr == nil {
			indexes = append(indexes, entry{path: filepath.Join(root, name), info: info})
		}
	}
	if len(indexes) <= 1 {
		return nil
	}
	sort.SliceStable(indexes, func(left, right int) bool {
		if indexes[left].info.ModTime().Equal(indexes[right].info.ModTime()) {
			return indexes[left].path > indexes[right].path
		}
		return indexes[left].info.ModTime().After(indexes[right].info.ModTime())
	})
	stale := make([]string, 0, len(indexes)-1)
	for _, index := range indexes[1:] {
		stale = append(stale, index.path)
	}
	return stale
}
