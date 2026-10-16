package cmd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/machinae/safewebtool/browser"
	"github.com/machinae/safewebtool/config"
	"github.com/machinae/safewebtool/mcp"
	"github.com/machinae/safewebtool/policy"
	"github.com/spf13/cobra"
)

var mcpPolicyFlag string

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start an MCP server over stdio.",
	Long: `Start an MCP server over stdio. Connects to Chrome on the first tool call, and again after the connection drops. Serves the webtool tool until stdin closes.

Environment variables:
  WEBTOOL_CDP_URL          remote CDP WebSocket URL; skips local Chrome discovery
  WEBTOOL_CDP_TOKEN        Bearer token sent to WEBTOOL_CDP_URL
  WEBTOOL_CHROME_DATA_DIR  Chrome user data directory to find DevToolsActivePort in (default: OS default)`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c := config.AppConfig
		c.CDPUrl = os.Getenv("WEBTOOL_CDP_URL")
		c.CDPToken = os.Getenv("WEBTOOL_CDP_TOKEN")
		if dir := os.Getenv("WEBTOOL_CHROME_DATA_DIR"); dir != "" {
			c.ChromeDataDir = dir
		}
		if err := c.Validate(); err != nil {
			return err
		}

		var b *browser.Browser
		if c.CDPUrl != "" {
			h := http.Header{}
			if c.CDPToken != "" {
				h.Set("Authorization", "Bearer "+c.CDPToken)
			}
			b = browser.New().
				WithURL(c.CDPUrl).
				WithHeaders(h)
		} else {
			b = browser.New().WithChromeDataDir(c.ChromeDataDir)
		}
		b.WithMaxTabs(c.MaxWorkers)

		if mcpPolicyFlag != "" {
			if err := watchPolicy(cmd.Context(), b, mcpPolicyFlag); err != nil {
				return err
			}
		}

		defer b.Close()

		return mcp.NewServer(b).ServeStdio()
	},
}

// watchPolicy loads a security policy, installs it, then watches for file
// changes and wires reload to the browser. Errors during reload are logged.
func watchPolicy(ctx context.Context, b *browser.Browser, source string) error {
	p, err := policy.Load(ctx, source)
	if err != nil {
		return fmt.Errorf("error in policy file: %w", err)
	}
	b.WithPolicy(p)

	// The watcher starts only after the initial policy is installed, so a
	// reload callback can never be overwritten by the older startup policy.
	_, err = policy.Watch(ctx, source, func(newP *policy.Policy, err error) {
		if err != nil {
			log.Printf("policy reload error: %v", err)
			return
		}
		if err := b.ReloadPolicy(newP); err != nil {
			log.Printf("policy reload error: %v", err)
		}
	})
	if err != nil {
		return fmt.Errorf("error in policy file: %w", err)
	}
	return nil
}

func init() {
	c := config.AppConfig
	mcpCmd.Flags().StringVarP(&mcpPolicyFlag, "policy", "p", "", "path or URL to security policy YAML file")
	mcpCmd.Flags().DurationVar(&c.Timeout, "timeout", c.Timeout, "time limit for one browser command")
	mcpCmd.Flags().IntVar(&c.MaxWorkers, "max-tabs", c.MaxWorkers, "max tabs attached at once and tab groups run concurrently")
	mcpCmd.Flags().IntVar(&c.MaxOutput, "max-output", c.MaxOutput, "truncate page output to this many characters (0 = no limit)")
	mcpCmd.Flags().BoolVar(&c.DemoMode, "demo", c.DemoMode, "draw a visible mouse cursor and bring each tab to the front for screen recordings")
	rootCmd.AddCommand(mcpCmd)
}
