package main

import (
	"github.com/alecthomas/kong"

	"github.com/utibeabasi6/ukc-runner-controller/internal/cmd"
)

func main() {
	var cli cmd.RunnerControllerCLI
	ctx := kong.Parse(&cli,
		kong.Name("ukc-runner-controller"),
		kong.Description("Run GitHub Actions jobs on Unikraft Cloud instances."),
		kong.UsageOnError(),
	)
	ctx.FatalIfErrorf(ctx.Run())
}
