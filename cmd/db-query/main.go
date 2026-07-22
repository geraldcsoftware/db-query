package main

import (
	"os"

	"github.com/geraldcsoftware/db-query/internal/cli"
)

// Build metadata, injected at release time via
// -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
// The defaults describe a local (non-release) build.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cli.SetBuildInfo(cli.BuildInfo{Version: version, Commit: commit, Date: date})
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
