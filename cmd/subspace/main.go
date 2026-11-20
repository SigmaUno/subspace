// Command subspace is the Subspace server.
package main

import (
	"fmt"
	"os"
)

func main() {
	// TODO(lead): wire internal/server (config, store, board, mcp, api, sweeper).
	fmt.Fprintln(os.Stderr, "subspace: server wiring not implemented yet")
	os.Exit(1)
}
