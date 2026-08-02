package safety

import (
	"os"
	"strings"
)

const (
	EnvDryRun = "RATATOUILLE_DRY_RUN"
	EnvNoAuth = "RATATOUILLE_NO_AUTH"
	EnvNoLog  = "RATATOUILLE_NO_OPLOG"
	EnvLog    = "RATATOUILLE_OPLOG_PATH"
)

func enabled(name string) bool {
	value := strings.TrimSpace(os.Getenv(name))
	return value != "" && value != "0" && !strings.EqualFold(value, "false")
}

// DryRunFromEnv lets a wrapper script or a test force dry-run for every
// removal in the process, independent of the command line.
func DryRunFromEnv() bool {
	return enabled(EnvDryRun)
}

// NoAuth refuses anything that could raise an authorization prompt: sudo,
// osascript, launchctl. Test runs set it so a suite cannot block on a dialog.
func NoAuth() bool {
	return enabled(EnvNoAuth)
}

func loggingDisabled() bool {
	return enabled(EnvNoLog)
}
