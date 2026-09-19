package main

import (
	"os"

	"github.com/motherlodelab/magpie/cli"
)

func main() {
	os.Exit(cli.Execute())
}
