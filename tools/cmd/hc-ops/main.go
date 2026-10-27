// hc-ops is the source-pinned deployment data tool. Bash retains orchestration.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/androiddev"
	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/androidpublish"
	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/androidrelease"
	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/backup"
	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/deployguard"
	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/fontcheck"
	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/relayguard"
	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/seosmoke"
	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/stagepush"
	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/stageyookassa"
)

var sourceSHA = "unknown" // Set by the exact-source CI build, never environment input.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Expected hc-ops command")
		return 1
	}
	switch args[0] {
	case "version":
		if len(args) != 1 {
			return 1
		}
		fmt.Fprintln(stdout, sourceSHA)
		return 0
	case "android-release":
		return androidrelease.Run(ctx, args[1:], stdout, stderr)
	case "android-manifest":
		return androidrelease.RunBuild(ctx, args[1:], stdout, stderr)
	case "android-huawei":
		return androidrelease.RunHuawei(ctx, args[1:], stdout, stderr)
	case "stage-push":
		return stagepush.Run(ctx, args[1:], stdout, stderr)
	case "stage-yookassa":
		return stageyookassa.Run(ctx, args[1:], stdout, stderr)
	case "backup":
		return backup.Run(ctx, args[1:], stdout, stderr)
	case "android-publish":
		return androidpublish.Run(ctx, args[1:], stdout, stderr)
	case "android-dev-manifest":
		return androiddev.Build(ctx, args[1:], stdout, stderr)
	case "android-dev-publish":
		return androiddev.RunPublish(ctx, args[1:], stdout, stderr)
	case "seo-smoke":
		return seosmoke.Run(ctx, args[1:], stdout, stderr)
	case "font-check":
		return fontcheck.Run(ctx, args[1:], stdout, stderr)
	case "deploy-json":
		return deployguard.Run(ctx, args[1:], stdout, stderr)
	case "compose-field", "compose-check", "compose-s3-env":
		return deployguard.Run(ctx, args, stdout, stderr)
	case "relay-response":
		return relayguard.Run(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "Unknown hc-ops command")
		return 1
	}
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
