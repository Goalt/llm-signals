package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Goalt/tg-channel-to-rss/internal/mcp"
)

// runMCP launches the MCP (Model Context Protocol) server over stdio. It
// returns the exit code: 0 on clean EOF, non-zero on transport error.
func runMCP() int {
	srv := mcp.New(os.Stdout, os.Stderr)
	if err := srv.Serve(os.Stdin); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		return 1
	}
	return 0
}
