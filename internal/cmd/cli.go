package cmd

type RunnerControllerCLI struct {
	Run     RunCmd     `cmd:"" default:"withargs" help:"Run the controller and the dashboard."`
	Version VersionCmd `cmd:"" help:"Print the version and exit."`
}
