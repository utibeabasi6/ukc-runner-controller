package cmd

import (
	"fmt"
	"runtime/debug"
)

// version and commit are set at link time by the release build.
var (
	version = "dev"
	commit  = ""
)

type VersionCmd struct{}

func (VersionCmd) Run() error {
	v, c := buildVersion()
	fmt.Println(v, c)
	return nil
}

// buildVersion falls back to the module version and VCS revision that the Go
// toolchain records, so binaries from go install report a useful version too.
func buildVersion() (string, string) {
	v, c := version, commit
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return v, c
	}
	if v == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		v = info.Main.Version
	}
	if c == "" {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				c = s.Value
			}
		}
	}
	return v, c
}
