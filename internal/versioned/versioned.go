// Package versioned plans cleanup for directories that retain multiple tool
// versions. The same plan is rebuilt at deletion time so a newly active
// version can never be removed from a stale preview.
package versioned

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type Spec struct {
	Root         string
	ActiveLink   string
	Active       string
	Installed    string
	KeepPrevious int
}

type Plan struct {
	Root   string
	Active string
	Keep   []string
	Stale  []string
}

func Resolve(spec Spec) (Plan, error) {
	root := filepath.Clean(spec.Root)
	info, err := os.Lstat(root)
	if err != nil {
		return Plan{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Plan{}, errors.New("versions root is not a physical directory")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Plan{}, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return Plan{}, err
	}
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == filepath.Base(spec.ActiveLink) || entry.Type()&os.ModeSymlink != 0 || !looksVersioned(entry.Name()) {
			continue
		}
		entryInfo, entryErr := entry.Info()
		if entryErr != nil || (!entryInfo.IsDir() && !entryInfo.Mode().IsRegular()) {
			continue
		}
		versions = append(versions, entry.Name())
	}
	if len(versions) == 0 {
		return Plan{Root: root}, nil
	}
	sort.SliceStable(versions, func(left, right int) bool { return compare(versions[left], versions[right]) > 0 })
	installed := strings.TrimSpace(spec.Installed)
	if installed != "" {
		if !looksVersioned(installed) {
			return Plan{}, errors.New("installed version is invalid")
		}
		plan := Plan{Root: root, Active: installed}
		for _, version := range versions {
			path := filepath.Join(root, version)
			if compare(version, installed) < 0 {
				plan.Stale = append(plan.Stale, path)
			} else {
				plan.Keep = append(plan.Keep, path)
			}
		}
		return plan, nil
	}

	active := strings.TrimSpace(spec.Active)
	if active == "" && spec.ActiveLink != "" {
		link := spec.ActiveLink
		if !filepath.IsAbs(link) {
			link = filepath.Join(root, link)
		}
		linkInfo, linkErr := os.Lstat(link)
		if linkErr != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
			return Plan{}, errors.New("active version link is unavailable")
		}
		target, linkErr := filepath.EvalSymlinks(link)
		if linkErr != nil || filepath.Dir(target) != resolvedRoot {
			return Plan{}, errors.New("active version link leaves the versions root")
		}
		active = filepath.Base(target)
	}
	if active == "" {
		active = versions[0]
	}
	activeFound := false
	for _, version := range versions {
		if version == active {
			activeFound = true
			break
		}
	}
	if !activeFound {
		return Plan{}, errors.New("active version is not a physical child")
	}

	keepCount := spec.KeepPrevious + 1
	if keepCount < 1 {
		keepCount = 1
	}
	keepSet := map[string]bool{active: true}
	for _, version := range versions {
		if len(keepSet) >= keepCount {
			break
		}
		keepSet[version] = true
	}
	plan := Plan{Root: root, Active: active}
	for _, version := range versions {
		path := filepath.Join(root, version)
		if keepSet[version] {
			plan.Keep = append(plan.Keep, path)
		} else {
			plan.Stale = append(plan.Stale, path)
		}
	}
	return plan, nil
}

func StillStale(spec Spec, path string) (bool, string) {
	plan, err := Resolve(spec)
	if err != nil {
		return false, "version inventory is unavailable"
	}
	cleaned := filepath.Clean(path)
	for _, stale := range plan.Stale {
		if cleaned == stale {
			return true, ""
		}
	}
	return false, "active version or retention set changed"
}

func looksVersioned(value string) bool {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\\`) {
		return false
	}
	hasDigit := false
	for _, character := range value {
		if unicode.IsDigit(character) {
			hasDigit = true
			continue
		}
		if !unicode.IsLetter(character) && character != '.' && character != '-' && character != '_' && character != '+' {
			return false
		}
	}
	return hasDigit
}

func compare(left, right string) int {
	leftParts := parts(left)
	rightParts := parts(right)
	for index := 0; index < len(leftParts) || index < len(rightParts); index++ {
		if index >= len(leftParts) {
			return -1
		}
		if index >= len(rightParts) {
			return 1
		}
		leftNumber, leftErr := strconv.ParseUint(leftParts[index], 10, 64)
		rightNumber, rightErr := strconv.ParseUint(rightParts[index], 10, 64)
		switch {
		case leftErr == nil && rightErr == nil && leftNumber != rightNumber:
			if leftNumber > rightNumber {
				return 1
			}
			return -1
		case leftParts[index] != rightParts[index]:
			if leftParts[index] > rightParts[index] {
				return 1
			}
			return -1
		}
	}
	return 0
}

func parts(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(character rune) bool {
		return character == '.' || character == '-' || character == '_' || character == '+'
	})
}
