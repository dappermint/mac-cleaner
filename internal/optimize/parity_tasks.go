package optimize

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dappermint/ratatouille/internal/catalog"
	"github.com/dappermint/ratatouille/internal/plist"
	"github.com/dappermint/ratatouille/internal/safety"
)

const (
	spotlightDomain       = "com.apple.spotlight"
	spotlightRulesKey     = "EnabledPreferenceRules"
	sharedFileListMaxSize = 32 << 20
	sharedFileListMaxSeen = 50_000
)

func spotlightPreferenceRules() Task {
	return Task{
		ID:          "spotlight-preference-rules",
		Name:        "Spotlight application rules",
		Description: "remove Spotlight preference rules belonging to apps that are no longer installed",
		Changes:     "rewrites Spotlight's application rule list without entries whose bundle id is provably absent",
		Reverses:    Rebuildable,
		Probe: func(_ context.Context, env Env) (bool, string) {
			_, removed, err := plannedSpotlightRules(env.Home)
			if err != nil {
				return false, "rule list unavailable: " + err.Error()
			}
			if len(removed) == 0 {
				return false, "no stale application rules found"
			}
			return true, fmt.Sprintf("%d stale rules found", len(removed))
		},
		Run: func(ctx context.Context, env Env) (Outcome, string, error) {
			kept, removed, err := plannedSpotlightRules(env.Home)
			if err != nil {
				return OutcomeFailed, "", err
			}
			if len(removed) == 0 {
				return OutcomeUnchanged, "no stale application rules found", nil
			}
			args := make([]string, 0, 4+len(kept))
			args = append(args, "write", spotlightDomain, spotlightRulesKey, "-array")
			args = append(args, kept...)
			if _, err := run(ctx, env, "/usr/bin/defaults", args...); err != nil {
				return OutcomeFailed, "", err
			}
			return OutcomeApplied, fmt.Sprintf("removed %d stale rules: %s", len(removed), strings.Join(removed, ", ")), nil
		},
	}
}

func plannedSpotlightRules(home string) ([]string, []string, error) {
	path := filepath.Join(home, "Library", "Preferences", spotlightDomain+".plist")
	dict, err := plist.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	raw, ok := dict[spotlightRulesKey].([]any)
	if !ok {
		return nil, nil, errors.New("the preference rule list is not an array")
	}
	installed, installedKnown := catalog.ReadInstalledState(home)
	if !installedKnown {
		return nil, nil, errors.New("the installed application index is incomplete")
	}
	return filterSpotlightRules(raw, installed)
}

func filterSpotlightRules(raw []any, installed map[string]bool) ([]string, []string, error) {
	if len(installed) == 0 {
		return nil, nil, errors.New("the installed application index is empty")
	}
	kept := make([]string, 0, len(raw))
	var removed []string
	for _, value := range raw {
		rule, ok := value.(string)
		if !ok {
			return nil, nil, errors.New("the preference rule list contains a non-string value")
		}
		key := strings.ToLower(rule)
		if systemSpotlightRule(rule) || !reverseDNSIdentifier(rule) || installed[key] {
			kept = append(kept, rule)
			continue
		}
		removed = append(removed, rule)
	}
	return kept, removed, nil
}

func systemSpotlightRule(rule string) bool {
	return strings.HasPrefix(rule, "System.") || strings.HasPrefix(rule, "com.apple.")
}

func reverseDNSIdentifier(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) < 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, character := range part {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' && character != '_' {
				return false
			}
		}
	}
	return true
}

func sharedFileLists() Task {
	return Task{
		ID:          "shared-file-lists",
		Name:        "Finder shared file lists",
		Description: "find corrupt sidebar and recent-item records without touching document history",
		Changes:     "moves corrupt .sfl2 and .sfl3 files outside ApplicationRecentDocuments to Trash",
		Reverses:    Rebuildable,
		Probe: func(_ context.Context, env Env) (bool, string) {
			broken, err := brokenSharedFileLists(env.Home)
			if err != nil {
				return false, "shared file lists unavailable: " + err.Error()
			}
			if len(broken) == 0 {
				return false, "every eligible shared file list parses"
			}
			return true, fmt.Sprintf("%d corrupt shared file lists found", len(broken))
		},
		Run: func(ctx context.Context, env Env) (Outcome, string, error) {
			broken, err := brokenSharedFileLists(env.Home)
			if err != nil {
				return OutcomeFailed, "", err
			}
			if len(broken) == 0 {
				return OutcomeUnchanged, "every eligible shared file list parses", nil
			}
			funnel := safety.NewFunnel(env.Home, env.Identity, env.DryRun, nil)
			moved := 0
			for _, path := range broken {
				parses, inspectErr := sharedFileListParses(path)
				if inspectErr != nil {
					return OutcomeFailed, fmt.Sprintf("moved %d files before the failure", moved), inspectErr
				}
				if parses {
					continue
				}
				request := safety.Request{Command: CommandName, Item: "shared-file-lists", Path: path}
				if _, err := funnel.Trash(ctx, request); err != nil {
					return OutcomeFailed, fmt.Sprintf("moved %d files before the failure", moved), err
				}
				moved++
			}
			if moved == 0 {
				return OutcomeUnchanged, "the files changed before removal and now parse", nil
			}
			return OutcomeApplied, fmt.Sprintf("%d corrupt shared file lists moved to Trash", moved), nil
		},
	}
}

func brokenSharedFileLists(home string) ([]string, error) {
	root := filepath.Join(home, "Library", "Application Support", "com.apple.sharedfilelist")
	seen := 0
	var broken []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		seen++
		if seen > sharedFileListMaxSeen {
			return errors.New("shared file list scan exceeded its item limit")
		}
		if strings.Contains(path, "ApplicationRecentDocuments") {
			return nil
		}
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if extension != ".sfl2" && extension != ".sfl3" {
			return nil
		}
		parses, inspectErr := sharedFileListParses(path)
		if inspectErr != nil {
			return inspectErr
		}
		if !parses {
			broken = append(broken, path)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return broken, err
}

func sharedFileListParses(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("shared file list is not a physical regular file")
	}
	if info.Size() > sharedFileListMaxSize {
		return false, fmt.Errorf("shared file list exceeds the %d byte inspection limit", sharedFileListMaxSize)
	}
	if info.Size() == 0 {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	_, err = plist.ParseAny(data)
	return err == nil, nil
}
