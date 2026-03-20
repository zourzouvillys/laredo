// Command laredo-server is the pre-built service binary that wires together
// all laredo modules with HOCON config, gRPC, metrics, and signal handling.
package main

import (
	"fmt"
	"os"

	"github.com/zourzouvillys/laredo"
)

func main() {
	fmt.Fprintf(os.Stderr, "laredo-server %s\n", laredo.Version)
	// TODO: implement config loading, engine setup, gRPC server, signal handling
	fmt.Fprintln(os.Stderr, "not yet implemented")
	os.Exit(1)
}
