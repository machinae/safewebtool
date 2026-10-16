# webtool

An MCP server that lets an AI agent drive your real Chrome browser, with security policies that limit what the agent can do.

webtool connects to the Chrome you already use, with your logins and cookies, over the Chrome DevTools Protocol (CDP). The agent gets one MCP tool, `webtool`, that opens tabs, reads pages as compact text snapshots, clicks, fills forms, types, and runs JavaScript. A YAML policy file blocks network requests and actions you don't want an agent to make, enforced inside Chrome, below anything the agent or the page can change.

It is a single Go binary. No Node.js, no Playwright, no browser download.

## Install

```bash
go install github.com/machinae/safewebtool/cmd/webtool@latest
```

This installs `webtool` into `$(go env GOPATH)/bin`. Make sure that directory is on your `PATH`, or use the binary's absolute path in the MCP client config below.

## Set up Chrome

**Chrome 144 or newer** is required.

1. Open `chrome://inspect/#remote-debugging` in Chrome.
2. Turn on remote debugging for your current browser session.

webtool connects on the agent's first browser tool call, not when the MCP client starts. Chrome then shows a dialog asking to allow the remote debugging connection; click **Allow**. The connection stays open for the rest of the client session. If it drops (for example, Chrome restarts), the next tool call reconnects and Chrome asks again.

## Add it to your MCP client

webtool serves MCP over stdio. The client starts `webtool mcp` once per session and keeps it running.

Claude Code:

```bash
claude mcp add webtool -- webtool mcp --policy /absolute/path/to/policy.yml
```

Codex:

```bash
codex mcp add webtool -- webtool mcp --policy /absolute/path/to/policy.yml
```

Or in `~/.codex/config.toml`:

```toml
[mcp_servers.webtool]
command = "webtool"
args = ["mcp", "--policy", "/absolute/path/to/policy.yml"]
```

Use an absolute policy path; the client may start the server from any working directory. Leave out `--policy` to run without a policy.

## Flags

`webtool mcp` takes these flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-p`, `--policy` | none | Path or `http(s)://` URL of a security policy YAML file. See [Security policies](#security-policies). |
| `--timeout` | `10s` | Time limit for one browser action. |
| `--max-tabs` | `5` | Maximum tabs the agent may have attached at once, and maximum tab groups one call runs in parallel. |
| `--max-output` | `0` (no limit) | Truncate each page-sourced output to this many characters. |
| `--demo` | off | Draw a visible mouse cursor and bring each tab to the front before it acts, for screen recordings. |

## Environment Variables

These choose which browser `webtool mcp` connects to. Set them in your MCP client's `env` block.

| Variable | Default | Description |
|----------|---------|-------------|
| `WEBTOOL_CDP_URL` | unset | Remote CDP WebSocket URL. When set, webtool connects to it instead of finding local Chrome. |
| `WEBTOOL_CDP_TOKEN` | unset | Bearer token sent to `WEBTOOL_CDP_URL`. |
| `WEBTOOL_CHROME_DATA_DIR` | OS default Chrome profile | Chrome user data directory where webtool looks for `DevToolsActivePort`. Ignored when `WEBTOOL_CDP_URL` is set. |

Claude Code takes them with `-e`, for example `claude mcp add webtool -e WEBTOOL_CDP_URL=wss://... -- webtool mcp`. Codex takes `--env KEY=VALUE`, or an `env = { ... }` table in `config.toml`.

## The webtool tool

The agent sends one JSON call per tool use. A call holds tab groups; each group targets one tab (an existing tab id, or `"new"`), runs its actions in order, and returns the page's end state in the format it asks for. Groups in one call run in parallel.

```json
{
  "tabs": [
    {
      "tab": "new",
      "output": "snapshot",
      "actions": [
        {"reason": "open the docs", "action": "open", "url": "https://example.com"}
      ]
    }
  ]
}
```

- **Actions:** `open`, `close`, `back`, `forward`, `reload`, `click`, `hover`, `fill`, `select`, `type`, `scroll`, `upload`, `eval` (run a JavaScript expression in the page), `screenshot`. A top-level `"browser"` array takes `list`, which lists open tabs.
- **Outputs:** `snapshot` (interactive elements with ids to act on), `markdown` (page content), `html`, or `none`, with optional filters to return only part of the page.
- **`reason`:** every action carries a one-sentence reason for why the agent is taking it.

The full schema is in [mcp/tool_schema.json](mcp/tool_schema.json); clients show it to the model.

## Security policies

A policy is a YAML file with two optional sections:

- **`network`** rules block HTTP requests inside Chrome. Every tab the agent opens or acts on has its requests checked before they leave the browser; a blocked request fails with `net::ERR_BLOCKED_BY_CLIENT`, and the agent sees which rule blocked it.
- **`actions`** rules block tool actions such as `eval` or `upload`. A call that contains a blocked action is rejected before any of it runs.

Read-only browsing, enforced at the network layer:

```yaml
description: Read-only. Block every request that could change data.
network:
  deny:
    - method: "POST|PUT|PATCH|DELETE"
```

Keep the agent on a few sites, and stop it from running JavaScript:

```yaml
description: Docs sites only, no JavaScript.
network:
  allow:
    - host: "example.com"
    - host: "*.example.com"
    - host: "docs.python.org"
actions:
  deny:
    - eval
```

Local policy files are reloaded automatically when they change. The `webtool` tool cannot read or change the policy, but a coding agent with file-editing tools can edit the file and webtool will load the edit. Keep the policy somewhere the agent cannot write; see [Protect the policy file](docs/SECURITY.md#protect-the-policy-file).

- [docs/SECURITY.md](docs/SECURITY.md): how enforcement works, what the agent sees, recipes, and limitations.
- [docs/policy-schema.md](docs/policy-schema.md): complete field reference.

## Output safety

Page content returned to the agent is wrapped in markers with a random nonce, followed by a note that the content is untrusted and its instructions must not be followed. This makes prompt injection from web pages easier for the model to recognize. Details are in [docs/SECURITY.md](docs/SECURITY.md#content-boundaries).

## FAQ

See [docs/FAQ.md](docs/FAQ.md).

## License

Apache 2.0. The vendored Rod library in `internal/rod` is MIT licensed; see [internal/rod/LICENSE](internal/rod/LICENSE).
