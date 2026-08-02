package catalog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

const obsoleteExtensionsMaxSize = 4 << 20

func obsoleteEditorExtensions() Target {
	roots := func(home string) []string {
		return []string{
			filepath.Join(home, ".vscode", "extensions"),
			filepath.Join(home, ".vscode-insiders", "extensions"),
			filepath.Join(home, ".cursor", "extensions"),
		}
	}
	return Target{
		ID: "obsolete-editor-extensions", Name: "obsolete editor extensions", Group: GroupDeveloper,
		Category: storage.CategoryDeveloper, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
		Detail: "extension versions the editor itself marked obsolete",
		Paths: []PathSpec{Resolver(func(_ context.Context, env Env) []string {
			paths := make([]string, 0, len(roots(env.Home))*4)
			for _, root := range roots(env.Home) {
				paths = append(paths, listedObsoleteExtensions(root)...)
			}
			return paths
		})},
		Guards: []Guard{
			ProcessNotRunning("Code", "Code - Insiders", "Cursor"),
			{Name: "still listed in the editor's .obsolete inventory", Allow: func(_ context.Context, _ Env, path string) (bool, string) {
				for _, listed := range listedObsoleteExtensions(filepath.Dir(path)) {
					if listed == filepath.Clean(path) {
						return true, ""
					}
				}
				return false, "the editor's obsolete inventory changed"
			}},
			OwnedByUser(),
		},
		MinBytes: 1 * mib,
		Evidence: "the extension is a physical direct child named by the editor's own .obsolete JSON inventory, re-read immediately before removal",
		NotTargets: []string{
			"installed extensions not listed in .obsolete, the .obsolete inventory itself, editor settings, profiles, snippets, workspaces, and cached extension downloads",
		},
	}
}

func listedObsoleteExtensions(root string) []string {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	inventory := filepath.Join(root, ".obsolete")
	info, err = os.Lstat(inventory)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > obsoleteExtensionsMaxSize {
		return nil
	}
	contents, err := os.ReadFile(inventory)
	if err != nil {
		return nil
	}
	var entries map[string]any
	if err := json.Unmarshal(contents, &entries); err != nil {
		return nil
	}
	paths := make([]string, 0, len(entries))
	for name := range entries {
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
			continue
		}
		path := filepath.Join(root, name)
		entryInfo, entryErr := os.Lstat(path)
		if entryErr == nil && entryInfo.IsDir() && entryInfo.Mode()&os.ModeSymlink == 0 {
			paths = append(paths, path)
		}
	}
	return storage.UniqueStrings(paths)
}
