package buildinfo

import (
	"fmt"
	"log/slog"
)

// These values are overridden with -ldflags "-X ..." by the release build.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}

func Log(logger *slog.Logger) {
	logger.Info("build_info", "version", Version, "commit", Commit, "built_at", Date)
}
