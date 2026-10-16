package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "webtool",
	Short: "MCP server that drives a Chrome browser via CDP.",
	Long: `webtool runs an MCP server that lets an LLM drive Chrome via the Chrome DevTools Protocol.

Subcommand 'mcp' serves MCP over stdio. Add it to an MCP client such as Claude Code or Codex.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.SetOut(os.Stdout)
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
