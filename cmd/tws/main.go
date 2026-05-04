package main

import (
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal/cli"
)

var version = "dev"

func main() {
	cli.SetVersion(version)
	os.Exit(cli.Execute())
}
