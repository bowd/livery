package main

import (
	"os"

	"github.com/bowd/livery/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
