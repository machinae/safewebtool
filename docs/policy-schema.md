# Policy Schema Reference

Complete field reference for the webtool security policy YAML. For how policies are enforced, what the agent sees, and limitations, see [SECURITY.md](SECURITY.md).

## Top-Level Structure

```yaml
version: "1"         # optional
description: "..."   # optional, what this policy is for
network:             # optional, HTTP request rules
  deny: [...]        # optional, rules that block requests
  allow: [...]       # optional, exceptions to deny rules
actions:             # optional, tool action rules
  deny: [...]        # optional, actions to block
  allow: [...]       # optional, exceptions to deny
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `version` | string | No | Policy schema version. Currently `"1"`. |
| `description` | string | No | Human-readable description of the policy. |
| `network` | object | No | HTTP request rules. |
| `actions` | object | No | Tool action rules. |

At least one of `network` or `actions` must contain a rule. A policy with only metadata is rejected at load time.

## Network

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `deny` | list of rules | No | Requests matching any rule are blocked, unless an `allow` rule also matches. |
| `allow` | list of rules | No | Exceptions. A request matching a deny rule and an allow rule continues. |

If `allow` is present and `deny` is empty, a deny rule that matches every request is inserted, so only requests matching an allow rule continue.

### Evaluation

1. No deny rule matches: **allowed**.
2. A deny rule matches and an allow rule matches: **allowed**.
3. A deny rule matches and no allow rule matches: **blocked** with `net::ERR_BLOCKED_BY_CLIENT`.

## Rule

Each entry in `network.deny` or `network.allow`. Every field set in a rule must match (AND). Any rule in a list matching is enough (OR). An omitted field matches anything. A rule with no fields matches every request.

| Field | Syntax | Matched against | Example |
|-------|--------|-----------------|---------|
| `method` | Regex, case-insensitive | HTTP method | `"POST\|PUT\|DELETE\|PATCH"` |
| `host` | Wildcard (`*`, `?`) | Parsed URL host, including the port if present | `"*.example.com"` |
| `path` | Regex | Parsed URL path | `"^/api/"`, `"/(users\|accounts)"` |
| `query` | Regex | Raw query string, without `?` | `"action=(delete\|archive)"` |
| `header` | Regex | Request headers, one `Name: Value\r\n` per header, canonical names (`Content-Type`, `Authorization`) | `"Authorization: Bearer "` |
| `body` | Regex | Request body (POST data) | `"delete\|archive"` |

### Host wildcards

`host` uses CDP wildcard syntax, not regex. The pattern must match the whole host.

| Pattern | Meaning |
|---------|---------|
| `*` | Any run of characters, including none |
| `?` | Exactly one character |
| Anything else | That literal character |

`|`, `\`, `^`, and `$` are rejected in `host`.

| Pattern | Matches | Does not match |
|---------|---------|----------------|
| `example.com` | `example.com` | `a.example.com`, `example.com:8443` |
| `*.example.com` | `a.example.com`, `a.b.example.com` | `example.com`, `evilexample.com` |
| `*example.com` | `example.com`, `a.example.com`, `evilexample.com` | `example.com.evil.net` |
| `example.com:*` | `example.com:8443` | `example.com` |

To cover a domain and its subdomains, list `example.com` and `*.example.com`. Avoid `*example.com`, which also matches other domains that end in the same letters.

### Regex fields

`method`, `path`, `query`, `header`, and `body` use Go [`regexp`](https://pkg.go.dev/regexp/syntax) syntax. `method` is compiled with `(?i)`; the others are case-sensitive unless the pattern starts with `(?i)`.

Regexes match anywhere in the value. Anchor them for exact matches:

| Pattern | Matches |
|---------|---------|
| `/sync` | `/sync`, `/sync/data`, `/old/sync` |
| `^/sync` | `/sync`, `/sync/data` |
| `^/sync$` | `/sync` only |

YAML treats `\` specially inside double quotes. Use single quotes for regexes with backslashes: `path: '^/items/\d+$'`.

### Component matching

The request URL is parsed with Go's `url.Parse`, and each field is matched against its own component: `host` against `URL.Host`, `path` against `URL.Path`, `query` against `URL.RawQuery`. A domain that appears in a path or query string never matches a `host` rule.

## Actions

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `deny` | list of action names | No | Actions to block. `"*"` matches every action. |
| `allow` | list of action names | No | Exceptions. An action in both lists is allowed. |

If `allow` is present and `deny` is empty, `deny: ["*"]` is inserted.

Valid names: `open`, `close`, `back`, `forward`, `reload`, `click`, `hover`, `fill`, `select`, `type`, `scroll`, `upload`, `eval`, `screenshot`, `list`.

- Names are case-insensitive and trimmed of whitespace.
- An unknown name is rejected at load time. `"*"` is valid only in `deny`.
- `list` and `close` are always allowed, whatever the policy says.
- Group outputs (`snapshot`, `markdown`, `html`) are not actions and are never blocked.
- A call containing a blocked action is rejected before any of its actions run.

## Examples

### Block all writes

```yaml
network:
  deny:
    - method: "POST|PUT|DELETE|PATCH"
```

### Block a domain and its subdomains

```yaml
network:
  deny:
    - host: "example.com"
    - host: "*.example.com"
```

### Allow only specific domains

```yaml
network:
  allow:
    - host: "example.com"
    - host: "*.example.com"
    - host: "docs.google.com"
```

### Block API writes, with a login exception

```yaml
network:
  deny:
    - method: "POST"
      host: "api.example.com"
  allow:
    - method: "POST"
      host: "api.example.com"
      path: "^/login$"
```

### Block destructive calls by request body

```yaml
network:
  deny:
    - host: "api.example.com"
      path: "^/sync"
      body: "delete|archive"
  allow:
    - host: "api.example.com"
      path: "^/sync"
      body: "read"
```

### Block DELETE on specific paths

```yaml
network:
  deny:
    - method: "DELETE"
      host: "api.example.com"
      path: "^/api/(users|accounts)"
```

### Block a query parameter

```yaml
network:
  deny:
    - host: "api.example.com"
      query: "action=(delete|archive)"
```

### Block requests with a bearer token

```yaml
network:
  deny:
    - header: "Authorization: Bearer "
```

### Block JavaScript execution

```yaml
actions:
  deny:
    - eval
```

### Read and navigate only

```yaml
actions:
  allow:
    - open
    - back
    - forward
    - reload
    - scroll
```

### Combined network and action rules

```yaml
description: Docs sites only, no JavaScript or uploads.
network:
  allow:
    - host: "*.example.com"
actions:
  deny:
    - eval
    - upload
```

## Error Messages

A blocked request fails the action that triggered it:

```
request blocked by policy: POST https://api.example.com/sync (rule: host=api.example.com path=^/sync body=delete|archive)
```

A rule that came from an allow-only policy's implicit deny-all is reported as `(rule: deny all)`.

A blocked action rejects the call:

```
action "eval" is blocked by policy
```

Load-time errors stop the server from starting:

```
error in policy file: policy has no rules: expected network or actions configuration
error in policy file: unknown action "extract" in actions deny list
error in policy file: deny rule: invalid path regex "(": ...
error in policy file: deny rule: invalid host pattern in policy: "^example.com". Regular expressions are not supported. Only wildcard characters * and ? are supported
```
