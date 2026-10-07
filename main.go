// dopt - Directory Optional Package Manager: installs and updates standalone
// Linux apps shipped as archives.
package main

import (
	"os"

	"github.com/Ominous-Josef/dopt/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
