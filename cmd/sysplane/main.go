package main

import (
	"os"

	_ "github.com/jimmicro/version"

	cli "github.com/jimyag/sysplane/internal/sysplane-cli"
)

func main() {
	app := cli.New(os.Stdout, os.Stderr)
	if err := app.Run(os.Args[1:]); err != nil {
		os.Exit(1)
	}
}
