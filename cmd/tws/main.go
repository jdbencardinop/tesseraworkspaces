package main

import (
	"os"
	"runtime/debug"

	"github.com/jdbencardinop/tesseraworkspaces/internal/cli"
)

var version = "dev"

func main() {
	cli.SetVersion(resolveVersion(version, readBuildInfo))
	os.Exit(cli.Execute())
}

func resolveVersion(injected string, readInfo func() (*debug.BuildInfo, bool)) string {
	if injected != "dev" {
		return injected
	}

	info, ok := readInfo()
	if ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	return injected
}

func readBuildInfo() (*debug.BuildInfo, bool) {
	return debug.ReadBuildInfo()
}
