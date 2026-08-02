package catalog

import (
	"time"

	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

type developerFamily struct {
	id, name  string
	processes []string
	paths     []PathSpec
	minBytes  int64
	guard     Guard
}

func additionalDeveloperTools() []Target {
	families := []developerFamily{
		{id: "go-download-cache", name: "Go download metadata cache", processes: []string{"go", "gopls"}, paths: []PathSpec{Home("go/pkg/mod/cache/download")}, minBytes: 16 * mib},
		{id: "ruby-caches", name: "Ruby tool caches", processes: []string{"ruby", "bundle", "gem"}, paths: []PathSpec{Home(".bundle/cache"), Home(".gem/specs"), Home("Library/Caches/CocoaPods")}, minBytes: 16 * mib},
		{id: "perl-caches", name: "Perl tool caches", processes: []string{"perl", "cpanm"}, paths: []PathSpec{Home(".cpanm/work")}, minBytes: 8 * mib, guard: OlderThan(30 * 24 * time.Hour)},
		{id: "haskell-caches", name: "Haskell tool caches", processes: []string{"cabal", "stack", "ghc"}, paths: []PathSpec{Home(".cabal/packages"), Home(".stack/downloaded")}, minBytes: 16 * mib},
		{id: "elixir-caches", name: "Elixir tool caches", processes: []string{"mix", "beam.smp"}, paths: []PathSpec{Home(".hex/packages"), Home(".hex/cache.ets")}, minBytes: 16 * mib},
		{id: "ocaml-caches", name: "OCaml tool caches", processes: []string{"opam", "ocaml"}, paths: []PathSpec{Home(".opam/download-cache")}, minBytes: 16 * mib},
		{id: "jvm-tool-caches", name: "JVM tool caches", processes: []string{"java", "sbt"}, paths: []PathSpec{Home(".ivy2/cache"), Home(".sbt/boot")}, minBytes: 64 * mib},
		{id: "mise-cache", name: "mise download cache", processes: []string{"mise"}, paths: []PathSpec{Home("Library/Caches/mise"), Home(".cache/mise")}, minBytes: 16 * mib},
		{id: "cicd-caches", name: "local CI tool caches", processes: []string{"act", "pre-commit"}, paths: []PathSpec{Home(".cache/act"), Home(".cache/pre-commit")}, minBytes: 16 * mib},
		{id: "cloud-cli-caches", name: "cloud CLI caches", processes: []string{"gcloud", "az"}, paths: []PathSpec{Home(".cache/gcloud"), Home("Library/Caches/google-cloud-sdk"), Home(".azure/logs")}, minBytes: 8 * mib},
		{id: "api-tool-caches", name: "API client caches", processes: []string{"Postman", "Insomnia"}, paths: []PathSpec{Home("Library/Application Support/Postman/Cache"), Home("Library/Application Support/Postman/Code Cache"), Home("Library/Application Support/Insomnia/Cache")}, minBytes: 8 * mib},
		{id: "database-tool-caches", name: "database tool caches", processes: []string{"TablePlus", "DBeaver"}, paths: []PathSpec{Home("Library/Caches/com.tinyapp.TablePlus"), Home("Library/Caches/org.jkiss.dbeaver.core.product")}, minBytes: 8 * mib},
		{id: "network-tool-caches", name: "network and cluster caches", processes: []string{"helm", "kubectl"}, paths: []PathSpec{Home(".cache/helm"), Home(".kube/cache")}, minBytes: 8 * mib},
		{id: "shell-caches", name: "shell caches", processes: []string{"fish", "zsh"}, paths: []PathSpec{Home(".cache/fish"), Home(".cache/zsh")}, minBytes: 8 * mib},
		{id: "developer-misc-caches", name: "developer utility caches", processes: []string{"gopls", "golangci-lint", "staticcheck"}, paths: []PathSpec{Home("Library/Caches/gopls"), Home("Library/Caches/golangci-lint"), Home("Library/Caches/staticcheck")}, minBytes: 8 * mib},
		{id: "code-editor-caches", name: "code editor caches", processes: []string{"Code", "Cursor", "Zed"}, paths: []PathSpec{
			Home("Library/Application Support/Code/Cache"), Home("Library/Application Support/Code/CachedData"), Home("Library/Application Support/Code/CachedExtensions"), Home("Library/Application Support/Code/Code Cache"),
			Home("Library/Application Support/Cursor/Cache"), Home("Library/Application Support/Cursor/CachedData"), Home("Library/Application Support/Cursor/CachedExtensions"), Home("Library/Caches/Zed"),
		}, minBytes: 16 * mib},
		{id: "corepack-cache", name: "Corepack cache", processes: []string{"corepack", "node"}, paths: []PathSpec{Home(".cache/node/corepack")}, minBytes: 16 * mib},
		{id: "conda-metadata-caches", name: "Conda metadata caches", processes: []string{"conda", "mamba"}, paths: []PathSpec{
			Glob(".conda/pkgs/cache/*"), Glob("anaconda3/pkgs/cache/*"), Glob("miniconda3/pkgs/cache/*"), Glob("miniforge3/pkgs/cache/*"), Glob("mambaforge/pkgs/cache/*"),
		}, minBytes: 8 * mib},
		{id: "xcode-xctest-devices", name: "Xcode XCTest devices", processes: []string{"Xcode", "xcodebuild", "testmanagerd"}, paths: []PathSpec{Home("Library/Developer/XCTestDevices")}, minBytes: 128 * mib},
		{id: "coresimulator-temp-caches", name: "CoreSimulator temporary files", processes: []string{"Simulator", "Xcode", "simdiskimaged"}, paths: []PathSpec{
			Glob("Library/Developer/CoreSimulator/Caches/*"), Glob("Library/Developer/CoreSimulator/Devices/*/data/tmp/*"), Glob("Library/Logs/CoreSimulator/*"),
		}, minBytes: 16 * mib},
	}
	targets := make([]Target, 0, len(families)+1)
	for _, family := range families {
		guards := []Guard{ProcessNotRunning(family.processes...), OwnedByUser()}
		if family.guard.Allow != nil {
			guards = append([]Guard{family.guard}, guards...)
		}
		targets = append(targets, Target{
			ID: family.id, Name: family.name, Group: GroupDeveloper,
			Category: storage.CategoryDeveloper, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
			Detail: "downloaded metadata, generated indexes, logs, or build caches recreated by the owning tool",
			Paths:  family.paths, Guards: guards, MinBytes: family.minBytes,
			Evidence: "every path is a tool-defined cache, log, metadata, temporary, or downloaded-package location with the surrounding configuration and project roots excluded",
			NotTargets: []string{
				"credentials, configuration, installed tool versions, source repositories, virtual environments, databases, and package-manager lock state",
			},
		})
	}
	targets = append(targets, Target{
		ID: "coresimulator-system-caches", Name: "CoreSimulator system caches", Group: GroupDeveloper,
		Category: storage.CategoryDeveloper, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
		Detail: "machine-wide simulator caches rebuilt by CoreSimulator",
		Paths:  []PathSpec{Glob("/Library/Developer/CoreSimulator/Caches/*")},
		Guards: []Guard{NeedsRoot(), ProcessNotRunning("Simulator", "Xcode", "simdiskimaged")}, MinBytes: 16 * mib,
		Evidence:   "direct children of CoreSimulator's machine-wide Caches directory, outside runtime and device data",
		NotTargets: []string{"simulator runtimes, device data, installed applications, and the Caches root"},
	})
	return targets
}
