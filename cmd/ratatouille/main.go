package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dappermint/ratatouille/internal/cli"
	"github.com/dappermint/ratatouille/internal/exitcode"
	"github.com/dappermint/ratatouille/internal/i18n"
	"github.com/dappermint/ratatouille/internal/safety"
)

var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Run(ctx, version, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		fmt.Fprint(os.Stderr, i18n.EnglishGB().T("app.error", err))
		if safety.Refused(err) {
			return exitcode.Refused
		}
		return exitcode.Code(err)
	}
	return 0
}
