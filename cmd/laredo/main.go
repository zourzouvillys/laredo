// Command laredo is the CLI tool for interacting with a laredo-server instance.
package main

import (
	"fmt"
	"os"

	"github.com/zourzouvillys/laredo"
)

func main() {
	fmt.Fprintf(os.Stderr, "laredo %s\n", laredo.Version)
	// TODO: implement CLI subcommands
	fmt.Fprintln(os.Stderr, "not yet implemented")
	os.Exit(1)
}
