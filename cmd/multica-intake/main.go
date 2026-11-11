// Command multica-intake is an MCP server that lets a chat bot hand work to a
// Multica team as suggestions only. See `multica-intake --help`.
package main

import (
	"context"
	"os"

	"github.com/vantt/multica-intake/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
