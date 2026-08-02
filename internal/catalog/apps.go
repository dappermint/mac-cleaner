package catalog

import (
	"github.com/dappermint/ratatouille/internal/safety"
	"github.com/dappermint/ratatouille/internal/storage"
)

type appFamily struct {
	id        string
	name      string
	processes []string
	paths     []string
}

func appsAndUtilities() []Target {
	families := []appFamily{
		{id: "communication-app-caches", name: "communication app caches", processes: []string{"Discord", "Slack", "zoom.us", "Microsoft Teams", "WhatsApp"}, paths: []string{
			"Library/Application Support/discord/Cache", "Library/Application Support/discord/Code Cache", "Library/Application Support/Slack/Cache", "Library/Application Support/Slack/Code Cache",
			"Library/Caches/us.zoom.xos", "Library/Caches/com.microsoft.teams2", "Library/Caches/net.whatsapp.WhatsApp", "Library/Caches/com.skype.skype",
		}},
		{id: "media-player-caches", name: "media player caches", processes: []string{"Spotify", "Music", "VLC"}, paths: []string{
			"Library/Caches/com.spotify.client", "Library/Caches/com.apple.Music", "Library/Caches/org.videolan.vlc",
		}},
		{id: "video-tool-caches", name: "video tool caches", processes: []string{"Final Cut Pro", "DaVinci Resolve", "HandBrake"}, paths: []string{
			"Library/Caches/com.apple.FinalCut", "Library/Caches/com.blackmagic-design.DaVinciResolve", "Library/Caches/fr.handbrake.HandBrake",
		}},
		{id: "design-tool-caches", name: "design tool caches", processes: []string{"Figma", "Sketch", "Adobe Photoshop 2026"}, paths: []string{
			"Library/Application Support/Figma/Cache", "Library/Application Support/Figma/Code Cache", "Library/Caches/com.figma.Desktop", "Library/Caches/com.bohemiancoding.sketch3", "Library/Caches/Adobe",
		}},
		{id: "note-app-caches", name: "note app caches", processes: []string{"Notion", "Obsidian", "Craft"}, paths: []string{
			"Library/Application Support/Notion/Cache", "Library/Application Support/Notion/Code Cache", "Library/Caches/md.obsidian", "Library/Caches/com.lukilabs.lukiapp",
		}},
		{id: "task-app-caches", name: "task app caches", processes: []string{"Things3", "Todoist", "Linear"}, paths: []string{
			"Library/Caches/com.culturedcode.ThingsMac", "Library/Application Support/Todoist/Cache", "Library/Application Support/Linear/Cache",
		}},
		{id: "productivity-app-caches", name: "productivity app caches", processes: []string{"Raycast", "Alfred", "Rectangle"}, paths: []string{
			"Library/Caches/com.raycast.macos", "Library/Caches/com.runningwithcrayons.Alfred", "Library/Caches/com.knollsoft.Rectangle",
		}},
		{id: "gaming-platform-caches", name: "gaming platform caches", processes: []string{"steam_osx", "EpicGamesLauncher", "Battle.net"}, paths: []string{
			"Library/Caches/com.valvesoftware.steam", "Library/Application Support/Steam/appcache/httpcache", "Library/Caches/com.epicgames.EpicGamesLauncher", "Library/Caches/net.battle.app",
		}},
		{id: "launcher-app-caches", name: "launcher app caches", processes: []string{"Setapp", "CleanMyMac"}, paths: []string{
			"Library/Caches/com.setapp.DesktopClient", "Library/Caches/com.macpaw.CleanMyMac5",
		}},
		{id: "screenshot-tool-caches", name: "screenshot tool caches", processes: []string{"CleanShot X", "Shottr"}, paths: []string{
			"Library/Caches/pl.maketheweb.cleanshotx", "Library/Caches/cc.ffitch.shottr",
		}},
		{id: "download-manager-caches", name: "download manager caches", processes: []string{"Folx", "Neat Download Manager"}, paths: []string{
			"Library/Caches/com.eltima.Folx", "Library/Caches/com.neatdownloadmanager.mac",
		}},
		{id: "email-client-caches", name: "email client caches", processes: []string{"Mail", "Spark", "Microsoft Outlook"}, paths: []string{
			"Library/Caches/com.apple.mail", "Library/Caches/com.readdle.smartemail-Mac", "Library/Caches/com.microsoft.Outlook",
		}},
		{id: "translation-app-caches", name: "translation app caches", processes: []string{"DeepL", "Mate Translate"}, paths: []string{
			"Library/Application Support/DeepL/Cache", "Library/Caches/com.deepl.DeepL", "Library/Caches/com.gikken.MateTranslate-Setapp",
		}},
		{id: "remote-desktop-caches", name: "remote desktop caches", processes: []string{"AnyDesk", "TeamViewer", "RustDesk"}, paths: []string{
			"Library/Caches/com.philandro.anydesk", "Library/Caches/com.teamviewer.TeamViewer", "Library/Caches/com.carriez.RustDesk",
		}},
		{id: "system-utility-caches", name: "system utility caches", processes: []string{"OrcaSlicer", "Pearcleaner"}, paths: []string{
			"Library/Caches/com.orcaslicer.OrcaSlicer", "Library/Caches/com.alienator88.Pearcleaner",
		}},
		{id: "shell-utility-caches", name: "shell utility caches", processes: []string{"Warp", "iTerm2"}, paths: []string{
			"Library/Caches/dev.warp.Warp-Stable", "Library/Caches/dev.warp.Warp-Preview", "Library/Caches/com.googlecode.iterm2",
		}},
		{id: "ai-app-caches", name: "AI app caches", processes: []string{"Claude", "Codex", "ChatGPT", "LM Studio"}, paths: []string{
			"Library/Application Support/Claude/Cache", "Library/Application Support/Claude/Code Cache", "Library/Caches/com.openai.codex", "Library/Caches/com.openai.chat", "Library/Caches/ai.lmstudio.app",
		}},
	}
	targets := make([]Target, 0, len(families))
	for _, family := range families {
		paths := make([]PathSpec, 0, len(family.paths))
		for _, path := range family.paths {
			paths = append(paths, Home(path))
		}
		targets = append(targets, Target{
			ID: family.id, Name: family.name, Group: GroupApps,
			Category: storage.CategorySystemData, Risk: RiskSafe, Recovery: safety.RecoveryTrash,
			Detail:   "named cache directories the owning apps rebuild on next launch",
			Paths:    paths,
			Guards:   []Guard{ProcessNotRunning(family.processes...), NotDataProtected(), OwnedByUser()},
			MinBytes: 8 * mib,
			Evidence: "each path is an app-specific Cache, Code Cache, or vendor cache directory, never the surrounding application support root",
			NotTargets: []string{
				"application support roots, documents, profiles, accounts, credentials, messages, projects, and downloaded media",
			},
		})
	}
	return targets
}
