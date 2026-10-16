package policy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIsAllowed_AllowExceptionOverridesDeny(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Method: "POST", Host: "*api.example.com"},
		},
		AllowList: []Rule{
			{Method: "POST", Host: "*api.example.com", Path: "/login"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}
	if err := compileRules(np.AllowList); err != nil {
		t.Fatal(err)
	}

	// Login endpoint matches allow exception — allowed.
	req, _ := http.NewRequest("POST", "https://api.example.com/login", nil)
	allowed, rule, err := np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allowed (allow exception), got denied")
	}
	if rule == nil {
		t.Fatal("expected matched allow rule, got nil")
	}

	// Other endpoint matches deny but no allow exception — denied.
	req, _ = http.NewRequest("POST", "https://api.example.com/users/delete", nil)
	allowed, rule, err = np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected denied (no allow exception)")
	}
	if rule == nil {
		t.Fatal("expected matched deny rule, got nil")
	}
}

func TestIsAllowed_DenyWithNoException(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Method: "DELETE", Host: "*api.example.com"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("DELETE", "https://api.example.com/users/1", nil)
	allowed, rule, err := np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected denied, got allowed")
	}
	if rule == nil {
		t.Fatal("expected matched deny rule, got nil")
	}
	if rule.Method != "DELETE" {
		t.Errorf("expected deny rule method DELETE, got %s", rule.Method)
	}
}

func TestIsAllowed_BodyRegex(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Host: "*api.example.com", Path: "/sync", Body: `danger`},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	// Request with matching body — should be denied.
	req, _ := http.NewRequest("POST", "https://api.example.com/sync/data", strings.NewReader("do something danger here"))
	allowed, _, err := np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected denied for body matching regex")
	}

	// Request without matching body — should be allowed.
	req, _ = http.NewRequest("POST", "https://api.example.com/sync/data", strings.NewReader("safe content"))
	allowed, _, err = np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allowed for body without match")
	}
}

func TestIsAllowed_MethodOnly(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Method: "delete"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	// DELETE should be denied (case-insensitive).
	req, _ := http.NewRequest("DELETE", "https://anything.com/whatever", nil)
	allowed, _, err := np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected DELETE denied")
	}

	// GET should be allowed.
	req, _ = http.NewRequest("GET", "https://anything.com/whatever", nil)
	allowed, _, err = np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected GET allowed")
	}
}

func TestIsAllowed_MethodRegex(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Method: "POST|PUT|DELETE"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	// POST, PUT, DELETE should all be denied.
	for _, method := range []string{"POST", "PUT", "DELETE", "post", "Put"} {
		req, _ := http.NewRequest(method, "https://example.com/api", nil)
		allowed, _, err := np.IsAllowed(req)
		if err != nil {
			t.Fatal(err)
		}
		if allowed {
			t.Fatalf("expected %s denied", method)
		}
	}

	// GET and HEAD should be allowed.
	for _, method := range []string{"GET", "HEAD"} {
		req, _ := http.NewRequest(method, "https://example.com/api", nil)
		allowed, _, err := np.IsAllowed(req)
		if err != nil {
			t.Fatal(err)
		}
		if !allowed {
			t.Fatalf("expected %s allowed", method)
		}
	}
}

func TestIsAllowed_AllFieldsMustMatch(t *testing.T) {
	// Rule with all 6 fields. Every field must match for the rule to trigger.
	np := &NetworkPolicy{
		DenyList: []Rule{
			{
				Method: "POST",
				Host:   "*api.example.com",
				Path:   "/sync",
				Query:  "action=delete",
				Header: "Authorization:.*Bearer",
				Body:   "dangerous",
			},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	// Helper to build the fully-matching request.
	makeReq := func() *http.Request {
		req, _ := http.NewRequest("POST", "https://api.example.com/sync/data?action=delete", strings.NewReader("dangerous payload"))
		req.Header.Set("Authorization", "Bearer abc123")
		return req
	}

	// All 6 fields match — denied.
	req := makeReq()
	allowed, _, err := np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected denied when all fields match")
	}

	// Each field mismatch should allow the request.
	tests := []struct {
		name   string
		modify func(r *http.Request) *http.Request
	}{
		{"wrong method", func(r *http.Request) *http.Request {
			req, _ := http.NewRequest("GET", r.URL.String(), strings.NewReader("dangerous payload"))
			req.Header = r.Header
			return req
		}},
		{"wrong host", func(r *http.Request) *http.Request {
			req, _ := http.NewRequest("POST", "https://evil.com/sync/data?action=delete", strings.NewReader("dangerous payload"))
			req.Header.Set("Authorization", "Bearer abc123")
			return req
		}},
		{"wrong path", func(r *http.Request) *http.Request {
			req, _ := http.NewRequest("POST", "https://api.example.com/other?action=delete", strings.NewReader("dangerous payload"))
			req.Header.Set("Authorization", "Bearer abc123")
			return req
		}},
		{"wrong query", func(r *http.Request) *http.Request {
			req, _ := http.NewRequest("POST", "https://api.example.com/sync/data?action=read", strings.NewReader("dangerous payload"))
			req.Header.Set("Authorization", "Bearer abc123")
			return req
		}},
		{"wrong header", func(r *http.Request) *http.Request {
			req, _ := http.NewRequest("POST", "https://api.example.com/sync/data?action=delete", strings.NewReader("dangerous payload"))
			req.Header.Set("Content-Type", "application/json")
			return req
		}},
		{"wrong body", func(r *http.Request) *http.Request {
			req, _ := http.NewRequest("POST", "https://api.example.com/sync/data?action=delete", strings.NewReader("safe content"))
			req.Header.Set("Authorization", "Bearer abc123")
			return req
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.modify(makeReq())
			allowed, _, err := np.IsAllowed(req)
			if err != nil {
				t.Fatal(err)
			}
			if !allowed {
				t.Fatalf("expected allowed when %s", tt.name)
			}
		})
	}
}

func TestIsAllowed_HostPathAND(t *testing.T) {
	// Host + path must both match — the security-critical AND combination.
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Host: "*api.example.com", Path: "/sync"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		url    string
		denied bool
	}{
		{"both match", "https://api.example.com/sync/data", true},
		{"right path wrong host", "https://evil.com/sync/data", false},
		{"right host wrong path", "https://api.example.com/other", false},
		{"neither match", "https://evil.com/other", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", tt.url, nil)
			allowed, _, err := np.IsAllowed(req)
			if err != nil {
				t.Fatal(err)
			}
			if allowed == tt.denied {
				if tt.denied {
					t.Fatal("expected denied")
				}
				t.Fatal("expected allowed")
			}
		})
	}
}

func TestIsAllowed_RegexPath(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Host: "*api.example.com", Path: "/api/(user|admin)"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		url    string
		denied bool
	}{
		{"user match", "https://api.example.com/api/user/profile", true},
		{"admin match", "https://api.example.com/api/admin/settings", true},
		{"other path", "https://api.example.com/api/public/docs", false},
		{"different host", "https://other.com/api/user/profile", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", tt.url, nil)
			allowed, _, err := np.IsAllowed(req)
			if err != nil {
				t.Fatal(err)
			}
			if allowed == tt.denied {
				if tt.denied {
					t.Fatal("expected denied")
				}
				t.Fatal("expected allowed")
			}
		})
	}
}

func TestIsAllowed_RegexQuery(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Host: "*api.example.com", Query: "action=(delete|drop)"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		url    string
		denied bool
	}{
		{"delete match", "https://api.example.com/resource?action=delete", true},
		{"drop match", "https://api.example.com/resource?action=drop", true},
		{"safe action", "https://api.example.com/resource?action=read", false},
		{"no query", "https://api.example.com/resource", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", tt.url, nil)
			allowed, _, err := np.IsAllowed(req)
			if err != nil {
				t.Fatal(err)
			}
			if allowed == tt.denied {
				if tt.denied {
					t.Fatal("expected denied")
				}
				t.Fatal("expected allowed")
			}
		})
	}
}

func TestIsAllowed_RegexBody(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Host: "*api.example.com", Body: `"action"\s*:\s*"(delete|archive)"`},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		body   string
		denied bool
	}{
		{"delete match", `{"action": "delete", "id": 1}`, true},
		{"archive match", `{"action":"archive"}`, true},
		{"safe action", `{"action": "read"}`, false},
		{"empty body", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", "https://api.example.com/sync", strings.NewReader(tt.body))
			allowed, _, err := np.IsAllowed(req)
			if err != nil {
				t.Fatal(err)
			}
			if allowed == tt.denied {
				if tt.denied {
					t.Fatal("expected denied")
				}
				t.Fatal("expected allowed")
			}
		})
	}
}

func TestIsAllowed_RegexMethod(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Host: "*api.example.com", Method: "POST|PUT|DELETE|PATCH"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		method string
		denied bool
	}{
		{"POST", "POST", true},
		{"PUT", "PUT", true},
		{"DELETE", "DELETE", true},
		{"PATCH", "PATCH", true},
		{"GET", "GET", false},
		{"HEAD", "HEAD", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(tt.method, "https://api.example.com/data", nil)
			allowed, _, err := np.IsAllowed(req)
			if err != nil {
				t.Fatal(err)
			}
			if allowed == tt.denied {
				if tt.denied {
					t.Fatal("expected denied")
				}
				t.Fatal("expected allowed")
			}
		})
	}
}

func TestIsAllowed_RegexHeader(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Host: "*api.example.com", Header: "X-Custom-Role:\\s*(admin|superuser)"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		header string
		value  string
		denied bool
	}{
		{"admin match", "X-Custom-Role", "admin", true},
		{"superuser match", "X-Custom-Role", "superuser", true},
		{"safe role", "X-Custom-Role", "viewer", false},
		{"no header", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "https://api.example.com/data", nil)
			if tt.header != "" {
				req.Header.Set(tt.header, tt.value)
			}
			allowed, _, err := np.IsAllowed(req)
			if err != nil {
				t.Fatal(err)
			}
			if allowed == tt.denied {
				if tt.denied {
					t.Fatal("expected denied")
				}
				t.Fatal("expected allowed")
			}
		})
	}
}

func TestIsAllowed_MultipleRulesOR(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Method: "DELETE"},
			{Host: "*evil.com"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	// Matches first rule only — denied.
	req, _ := http.NewRequest("DELETE", "https://safe.com/resource", nil)
	allowed, rule, err := np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected denied by first rule")
	}
	if rule.Method != "DELETE" {
		t.Errorf("expected first rule to match, got: %s", rule)
	}

	// Matches second rule only — denied.
	req, _ = http.NewRequest("GET", "https://evil.com/steal", nil)
	allowed, rule, err = np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected denied by second rule")
	}
	if rule.Host != "*evil.com" {
		t.Errorf("expected second rule to match, got: %s", rule)
	}

	// Matches neither — allowed.
	req, _ = http.NewRequest("GET", "https://safe.com/resource", nil)
	allowed, _, err = np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allowed when no rule matches")
	}
}

func TestDenyPatterns_Deduplicates(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Host: "*api.example.com", Path: "/sync", Body: "action1"},
			{Host: "*api.example.com", Path: "/sync", Body: "action2"},
			{Host: "*other.example.com", Path: "/delete"},
		},
	}
	patterns := np.DenyPatterns()
	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d: %v", len(patterns), patterns)
	}
}

func TestCdpPattern(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		pattern string
	}{
		{"host and path", Rule{Host: "*api.example.com", Path: "/sync"}, "*://*api.example.com/*"},
		{"host only", Rule{Host: "*api.example.com"}, "*://*api.example.com/*"},
		{"path only", Rule{Path: "/api/delete"}, "*"},
		{"method only", Rule{Method: "DELETE"}, "*"},
		{"no fields", Rule{}, "*"},
		{"query only", Rule{Query: "action=delete"}, "*"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cdpPattern(&tt.rule)
			if got != tt.pattern {
				t.Errorf("cdpPattern(%v) = %q, want %q", tt.rule, got, tt.pattern)
			}
		})
	}
}

func TestDenyPatterns_CatchAllWhenNoHost(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Host: "*api.example.com"},
			{Method: "DELETE"}, // no host
		},
	}
	patterns := np.DenyPatterns()
	if len(patterns) != 1 || patterns[0] != "*" {
		t.Fatalf("expected [*], got %v", patterns)
	}
}

func TestLoad_ValidPolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yml")
	content := `version: "1"
network:
  deny:
    - method: "DELETE"
      host: "*api.example.com"
    - host: "*api.example.com"
      path: "/sync*"
      body: "delete_action"
  allow:
    - host: "*api.example.com"
      path: "/sync*"
      body: "read_action"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.Version != "1" {
		t.Errorf("version: got %q, want %q", p.Version, "1")
	}
	if p.Path != path {
		t.Errorf("path: got %q, want %q", p.Path, path)
	}
	if len(p.Network.DenyList) != 2 {
		t.Fatalf("deny rules: got %d, want 2", len(p.Network.DenyList))
	}
	if len(p.Network.AllowList) != 1 {
		t.Fatalf("allow rules: got %d, want 1", len(p.Network.AllowList))
	}
	if p.Network.DenyList[0].Method != "DELETE" {
		t.Errorf("deny[0].Method: got %q, want %q", p.Network.DenyList[0].Method, "DELETE")
	}
	if p.Network.DenyList[1].hostRegex == nil {
		t.Error("deny[1].hostRegex not compiled")
	}
	if p.Network.DenyList[1].pathRegex == nil {
		t.Error("deny[1].pathRegex not compiled")
	}
	if p.Network.DenyList[1].bodyRegex == nil {
		t.Error("deny[1].bodyRegex not compiled")
	}
	if p.Network.AllowList[0].bodyRegex == nil {
		t.Error("allow[0].bodyRegex not compiled")
	}
}

func TestLoad_InvalidDenyRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yml")
	content := "network:\n  deny:\n    - body: \"[\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(context.Background(), path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "deny rule:") {
		t.Errorf("expected 'deny rule:' prefix in error, got: %s", err)
	}
}

func TestLoad_InvalidAllowRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yml")
	content := "network:\n  deny:\n    - method: \"GET\"\n  allow:\n    - body: \"[\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(context.Background(), path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "allow rule:") {
		t.Errorf("expected 'allow rule:' prefix in error, got: %s", err)
	}
}

func TestLoad_InvalidHostPattern(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yml")
	content := "network:\n  deny:\n    - host: \"(mail|calendar).google.com\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(context.Background(), path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Regular expressions are not supported") {
		t.Errorf("expected regex warning in error, got: %s", err)
	}
}

func TestCompileRules_InvalidRegex(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		errText string
	}{
		{"invalid path regex", Rule{Path: "["}, "invalid path regex"},
		{"invalid query regex", Rule{Query: "["}, "invalid query regex"},
		{"invalid header regex", Rule{Header: "["}, "invalid header regex"},
		{"invalid body regex", Rule{Body: "["}, "invalid body regex"},
		{"invalid method regex", Rule{Method: "["}, "invalid method regex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := compileRules([]Rule{tt.rule})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.errText) {
				t.Errorf("expected %q in error, got: %s", tt.errText, err)
			}
		})
	}
}

func TestCompileRules_HostPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		match   string
		noMatch string
	}{
		{
			name:    "exact host",
			pattern: "api.example.com",
			match:   "api.example.com",
			noMatch: "evil.com",
		},
		{
			name:    "wildcard subdomain",
			pattern: "*.example.com",
			match:   "api.example.com",
			noMatch: "example.org",
		},
		{
			name:    "wildcard subdomain rejects lookalike domain",
			pattern: "*.example.com",
			match:   "a.b.example.com",
			noMatch: "evilexample.com",
		},
		{
			name:    "dot is literal",
			pattern: "example.com",
			match:   "example.com",
			noMatch: "exampleXcom",
		},
		{
			name:    "wildcard prefix",
			pattern: "*example.com",
			match:   "api.example.com",
			noMatch: "example.org",
		},
		{
			name:    "host with port",
			pattern: "api.example.com:8080",
			match:   "api.example.com:8080",
			noMatch: "api.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := []Rule{{Host: tt.pattern}}
			if err := compileRules(rules); err != nil {
				t.Fatal(err)
			}
			if rules[0].hostRegex == nil {
				t.Fatal("expected compiled host regex")
			}
			if !rules[0].hostRegex.MatchString(tt.match) {
				t.Errorf("expected pattern %q to match %q", tt.pattern, tt.match)
			}
			if rules[0].hostRegex.MatchString(tt.noMatch) {
				t.Errorf("expected pattern %q not to match %q", tt.pattern, tt.noMatch)
			}
		})
	}
}

func TestCompileRules_PathPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		match   string
		noMatch string
	}{
		{
			name:    "exact path",
			pattern: "^/api/users$",
			match:   "/api/users",
			noMatch: "/api/users/1",
		},
		{
			name:    "prefix match",
			pattern: "^/api/",
			match:   "/api/users/1",
			noMatch: "/other/path",
		},
		{
			name:    "suffix match",
			pattern: "/delete$",
			match:   "/api/users/delete",
			noMatch: "/api/users/1",
		},
		{
			name:    "alternation",
			pattern: "/(users|accounts)",
			match:   "/api/users",
			noMatch: "/api/settings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := []Rule{{Path: tt.pattern}}
			if err := compileRules(rules); err != nil {
				t.Fatal(err)
			}
			if rules[0].pathRegex == nil {
				t.Fatal("expected compiled path regex")
			}
			if !rules[0].pathRegex.MatchString(tt.match) {
				t.Errorf("expected pattern %q to match %q", tt.pattern, tt.match)
			}
			if rules[0].pathRegex.MatchString(tt.noMatch) {
				t.Errorf("expected pattern %q not to match %q", tt.pattern, tt.noMatch)
			}
		})
	}
}

func TestCompileRules_QueryPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		match   string
		noMatch string
	}{
		{
			name:    "exact query param",
			pattern: "action=delete",
			match:   "action=delete",
			noMatch: "action=read",
		},
		{
			name:    "alternation",
			pattern: "action=(delete|archive)",
			match:   "action=delete&id=1",
			noMatch: "action=read",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := []Rule{{Query: tt.pattern}}
			if err := compileRules(rules); err != nil {
				t.Fatal(err)
			}
			if rules[0].queryRegex == nil {
				t.Fatal("expected compiled query regex")
			}
			if !rules[0].queryRegex.MatchString(tt.match) {
				t.Errorf("expected pattern %q to match %q", tt.pattern, tt.match)
			}
			if rules[0].queryRegex.MatchString(tt.noMatch) {
				t.Errorf("expected pattern %q not to match %q", tt.pattern, tt.noMatch)
			}
		})
	}
}

func TestIsAllowed_AllowExceptionByBody(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Method: "POST", Host: "*api.example.com", Path: "/sync", Body: "delete|archive"},
		},
		AllowList: []Rule{
			{Method: "POST", Host: "*api.example.com", Path: "/sync", Body: "read"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}
	if err := compileRules(np.AllowList); err != nil {
		t.Fatal(err)
	}

	// Body matches deny but also matches allow exception — allowed.
	req, _ := http.NewRequest("POST", "https://api.example.com/sync/data", strings.NewReader("read delete"))
	allowed, rule, err := np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allowed (allow exception by body)")
	}
	if rule == nil || rule.Body != "read" {
		t.Fatalf("expected allow rule with body=read, got %v", rule)
	}

	// Body matches deny only — denied.
	req, _ = http.NewRequest("POST", "https://api.example.com/sync/data", strings.NewReader("archive stuff"))
	allowed, rule, err = np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected denied (no allow exception)")
	}
	if rule == nil || rule.Body != "delete|archive" {
		t.Fatalf("expected deny rule with body=delete|archive, got %v", rule)
	}
}

func TestIsAllowed_NilBodyWithBodyRules(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Host: "*api.example.com", Body: "dangerous"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	// Nil body — body regex won't match, so request is allowed.
	req, _ := http.NewRequest("GET", "https://api.example.com/data", nil)
	allowed, _, err := np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allowed when body is nil and body regex can't match")
	}
}

func TestIsAllowed_BodyRegexInAllowOnly(t *testing.T) {
	// Body regex only in allow list — needsBody must still read the body.
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Method: "POST"},
		},
		AllowList: []Rule{
			{Method: "POST", Body: "safe"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}
	if err := compileRules(np.AllowList); err != nil {
		t.Fatal(err)
	}

	// Body matches allow exception — allowed.
	req, _ := http.NewRequest("POST", "https://example.com/api", strings.NewReader("safe request"))
	allowed, _, err := np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allowed by body allow exception")
	}

	// Body doesn't match allow — denied.
	req, _ = http.NewRequest("POST", "https://example.com/api", strings.NewReader("dangerous request"))
	allowed, _, err = np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected denied when body doesn't match allow exception")
	}
}

func TestIsAllowed_EmptyBody(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Host: "*api.example.com", Body: "dangerous"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	// Empty string body — body regex won't match, request is allowed.
	req, _ := http.NewRequest("POST", "https://api.example.com/data", strings.NewReader(""))
	allowed, _, err := np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allowed when body is empty and body regex can't match")
	}
}

func TestIsAllowed_HeaderMatching(t *testing.T) {
	np := &NetworkPolicy{
		DenyList: []Rule{
			{Header: "Authorization:.*Bearer"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}

	// Request with matching header — denied.
	req, _ := http.NewRequest("GET", "https://example.com/api", nil)
	req.Header.Set("Authorization", "Bearer abc123")
	allowed, _, err := np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected denied for matching Authorization header")
	}

	// Request without matching header — allowed.
	req, _ = http.NewRequest("GET", "https://example.com/api", nil)
	req.Header.Set("Content-Type", "application/json")
	allowed, _, err = np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allowed when header doesn't match")
	}
}

func TestCompileRules_RejectsRegexInHost(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{"pipe alternation", "(mail|calendar).google.com"},
		{"backslash escape", `mail\.google\.com`},
		{"caret anchor", "^api.example.com"},
		{"dollar anchor", "api.example.com$"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := []Rule{{Host: tt.host}}
			err := compileRules(rules)
			if err == nil {
				t.Fatalf("expected error for host %q, got nil", tt.host)
			}
			if !strings.Contains(err.Error(), "Regular expressions are not supported") {
				t.Errorf("expected regex warning in error, got: %s", err)
			}
		})
	}
}

func TestLoad_EmptyNetworkPolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yml")
	content := "version: \"1\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for policy with no rules")
	}
}

func TestNetworkPolicy_IsEnabled(t *testing.T) {
	if (NetworkPolicy{}).IsEnabled() {
		t.Fatal("empty network policy should not be enabled")
	}
	if !(NetworkPolicy{DenyList: []Rule{{Method: "POST"}}}).IsEnabled() {
		t.Fatal("network policy with deny rules should be enabled")
	}
	if !(NetworkPolicy{AllowList: []Rule{{Host: "*.example.com"}}}).IsEnabled() {
		t.Fatal("network policy with allow rules should be enabled")
	}
}

func TestActionsPolicy_IsEnabled(t *testing.T) {
	if (ActionsPolicy{}).IsEnabled() {
		t.Fatal("empty actions policy should not be enabled")
	}
	if !(ActionsPolicy{DenyList: []string{"eval"}}).IsEnabled() {
		t.Fatal("actions policy with deny rules should be enabled")
	}
	if !(ActionsPolicy{AllowList: []string{"click"}}).IsEnabled() {
		t.Fatal("actions policy with allow rules should be enabled")
	}
}

func TestIsAllowed_AllowOnlyImplicitDenyAll(t *testing.T) {
	// Allow-only policy: no deny list, just allow rules.
	// Should implicitly deny everything except what's allowed.
	np := &NetworkPolicy{
		DenyList: []Rule{{}}, // simulates the implicit catch-all deny
		AllowList: []Rule{
			{Host: "*example.com"},
		},
	}
	if err := compileRules(np.DenyList); err != nil {
		t.Fatal(err)
	}
	if err := compileRules(np.AllowList); err != nil {
		t.Fatal(err)
	}

	// Request to allowed host — allowed.
	req, _ := http.NewRequest("GET", "https://example.com/page", nil)
	allowed, _, err := np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allowed for matching allow rule")
	}

	// Request to non-allowed host — denied.
	req, _ = http.NewRequest("GET", "https://evil.com/steal", nil)
	allowed, _, err = np.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected denied for host not in allow list")
	}
}

func TestIsActionAllowed_DenyList(t *testing.T) {
	p := &Policy{
		Actions: ActionsPolicy{
			DenyList: []string{"eval"},
		},
	}
	if p.IsActionAllowed("eval") {
		t.Fatal("expected eval denied")
	}
	if !p.IsActionAllowed("click") {
		t.Fatal("expected click allowed")
	}
}

func TestIsActionAllowed_AllowList(t *testing.T) {
	p := &Policy{
		Actions: ActionsPolicy{
			DenyList:  []string{"*"},
			AllowList: []string{"screenshot", "list"},
		},
	}
	if !p.IsActionAllowed("screenshot") {
		t.Fatal("expected screenshot allowed")
	}
	if !p.IsActionAllowed("list") {
		t.Fatal("expected list allowed")
	}
	if p.IsActionAllowed("eval") {
		t.Fatal("expected eval denied")
	}
}

// Rule names are lowercased at load, so a mixed-case "Click" rule
// must still match the runtime's "click".
func TestIsActionAllowed_CaseInsensitive(t *testing.T) {
	p := &Policy{
		Actions: ActionsPolicy{DenyList: []string{"Click"}},
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.IsActionAllowed("click") {
		t.Fatal("expected click denied")
	}
}

func TestIsActionAllowed_NoRules(t *testing.T) {
	p := &Policy{}
	if !p.IsActionAllowed("eval") {
		t.Fatal("expected all actions allowed with no rules")
	}
}

func TestIsActionAllowed_NilPolicy(t *testing.T) {
	var p *Policy
	if !p.IsActionAllowed("eval") {
		t.Fatal("expected all actions allowed with nil policy")
	}
}

func TestLoad_ActionsDenyAndAllow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yml")
	content := "actions:\n  deny:\n    - eval\n  allow:\n    - click\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.IsActionAllowed("eval") {
		t.Fatal("expected eval denied")
	}
	// click is not in deny list, so it's allowed regardless of allow list.
	if !p.IsActionAllowed("click") {
		t.Fatal("expected click allowed")
	}
}

func TestLoad_ActionsUnknownAction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yml")
	content := "actions:\n  deny:\n    - fakecmd\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(context.Background(), path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `unknown action "fakecmd"`) {
		t.Errorf("expected unknown action error, got: %s", err)
	}
}

func TestLoad_ActionsLowercase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yml")
	content := "actions:\n  deny:\n    - EVAL\n    - Click\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Actions.DenyList[0] != "eval" || p.Actions.DenyList[1] != "click" {
		t.Errorf("expected lowercased actions, got: %v", p.Actions.DenyList)
	}
}

func TestLoad_AllowOnlyPolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yml")
	content := `network:
  allow:
    - host: "*.example.com"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have synthesized a catch-all deny rule.
	if len(p.Network.DenyList) != 1 {
		t.Fatalf("expected 1 implicit deny rule, got %d", len(p.Network.DenyList))
	}
	// The implicit deny rule should have all empty fields.
	r := p.Network.DenyList[0]
	if r.Method != "" || r.Host != "" || r.Path != "" || r.Query != "" || r.Header != "" || r.Body != "" {
		t.Errorf("expected empty catch-all deny rule, got: %s", r)
	}

	if len(p.Network.AllowList) != 1 {
		t.Fatalf("expected 1 allow rule, got %d", len(p.Network.AllowList))
	}

	// Verify it works end-to-end: allowed host passes, other host denied.
	req, _ := http.NewRequest("GET", "https://api.example.com/data", nil)
	allowed, _, err := p.Network.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allowed for *.example.com")
	}

	req, _ = http.NewRequest("GET", "https://evil.com/steal", nil)
	allowed, _, err = p.Network.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected denied for non-allowed host")
	}
}

func TestLoad_FromURL(t *testing.T) {
	body := `
network:
  deny:
    - method: "POST"
      host: "*api.example.com"
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	p, err := Load(context.Background(), srv.URL+"/policy.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Network.DenyList) != 1 {
		t.Fatalf("expected 1 deny rule, got %d", len(p.Network.DenyList))
	}
	if p.Network.DenyList[0].Host != "*api.example.com" {
		t.Errorf("expected host *api.example.com, got %s", p.Network.DenyList[0].Host)
	}
	expectedURL := srv.URL + "/policy.yml"
	if p.Path != expectedURL {
		t.Errorf("path: got %q, want %q", p.Path, expectedURL)
	}
}

func TestLoad_FromURL_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := Load(context.Background(), srv.URL+"/policy.yml")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestWatch_InitialLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yml")
	content := "actions:\n  deny:\n    - eval\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Watch(context.Background(), path, func(_ *Policy, _ error) {})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.IsActionAllowed("eval") {
		t.Fatal("expected eval denied")
	}
	if !p.IsActionAllowed("click") {
		t.Fatal("expected click allowed")
	}
	if p.Path != path {
		t.Errorf("path: got %q, want %q", p.Path, path)
	}
}

func TestWatch_InitialLoadError(t *testing.T) {
	_, err := Watch(context.Background(), "/nonexistent/policy.yml", func(_ *Policy, _ error) {})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestWatch_ReloadsOnFileChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yml")
	content := "actions:\n  deny:\n    - eval\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var reloaded *Policy
	var reloadErr error
	done := make(chan struct{}, 1)

	p, err := Watch(context.Background(), path, func(newP *Policy, err error) {
		mu.Lock()
		defer mu.Unlock()
		reloaded = newP
		reloadErr = err
		select {
		case done <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.IsActionAllowed("eval") {
		t.Fatal("expected eval denied initially")
	}

	// Update the policy file.
	newContent := "actions:\n  deny:\n    - click\n"
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for policy reload")
	}

	mu.Lock()
	defer mu.Unlock()
	if reloadErr != nil {
		t.Fatalf("unexpected reload error: %v", reloadErr)
	}
	if reloaded.IsActionAllowed("click") {
		t.Fatal("expected click denied after reload")
	}
	if !reloaded.IsActionAllowed("eval") {
		t.Fatal("expected eval allowed after reload")
	}
}

func TestWatch_InvalidReloadCallsOnChangeWithError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yml")
	content := "actions:\n  deny:\n    - eval\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var reloadErr error
	done := make(chan struct{}, 1)

	_, err := Watch(context.Background(), path, func(newP *Policy, err error) {
		mu.Lock()
		defer mu.Unlock()
		reloadErr = err
		select {
		case done <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Write an invalid policy (unknown action).
	invalidContent := "actions:\n  deny:\n    - fakecmd\n"
	if err := os.WriteFile(path, []byte(invalidContent), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for policy reload")
	}

	mu.Lock()
	defer mu.Unlock()
	if reloadErr == nil {
		t.Fatal("expected error for invalid policy reload")
	}
	if !strings.Contains(reloadErr.Error(), "unknown action") {
		t.Errorf("expected unknown action error, got: %s", reloadErr)
	}
}

func TestWatchDir_ReloadsOnFileAdd(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "a.yml"), []byte("actions:\n  deny:\n    - eval\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var reloaded []*Policy
	var reloadErr error
	done := make(chan struct{}, 1)

	if err := WatchDir(dir, func(newPolicies []*Policy, err error) {
		mu.Lock()
		defer mu.Unlock()
		reloaded = newPolicies
		reloadErr = err
		select {
		case done <- struct{}{}:
		default:
		}
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Add a second policy file.
	if err := os.WriteFile(filepath.Join(dir, "b.yml"), []byte("actions:\n  deny:\n    - upload\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reload")
	}

	mu.Lock()
	defer mu.Unlock()
	if reloadErr != nil {
		t.Fatalf("unexpected reload error: %v", reloadErr)
	}
	if len(reloaded) != 2 {
		t.Fatalf("expected 2 policies after adding file, got %d", len(reloaded))
	}
}

func TestWatchDir_ReloadsOnFileRemove(t *testing.T) {
	dir := t.TempDir()

	pathA := filepath.Join(dir, "a.yml")
	pathB := filepath.Join(dir, "b.yml")
	if err := os.WriteFile(pathA, []byte("actions:\n  deny:\n    - eval\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, []byte("actions:\n  deny:\n    - upload\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var reloaded []*Policy
	var reloadErr error
	done := make(chan struct{}, 1)

	if err := WatchDir(dir, func(newPolicies []*Policy, err error) {
		mu.Lock()
		defer mu.Unlock()
		reloaded = newPolicies
		reloadErr = err
		select {
		case done <- struct{}{}:
		default:
		}
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Remove a file to trigger reload.
	if err := os.Remove(pathB); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reload")
	}

	mu.Lock()
	defer mu.Unlock()
	if reloadErr != nil {
		t.Fatalf("unexpected reload error: %v", reloadErr)
	}
	if len(reloaded) != 1 {
		t.Fatalf("expected 1 policy after removing file, got %d", len(reloaded))
	}
}

func TestMerge_Empty(t *testing.T) {
	_, err := Merge()
	if err == nil {
		t.Fatal("expected error for empty merge")
	}
}

func TestMerge_Single(t *testing.T) {
	p := &Policy{
		Actions: ActionsPolicy{DenyList: []string{"eval"}},
	}
	// Single policy needs network or actions to pass validation,
	// and Merge with one policy returns it directly.
	got, err := Merge(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != p {
		t.Fatal("expected same pointer back for single policy")
	}
}

func TestMerge_NetworkDenyRules(t *testing.T) {
	a := &Policy{
		Network: NetworkPolicy{
			DenyList: []Rule{{Host: "*evil.com"}},
		},
	}
	b := &Policy{
		Network: NetworkPolicy{
			DenyList: []Rule{{Method: "DELETE"}},
		},
	}
	merged, err := Merge(a, b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(merged.Network.DenyList) != 2 {
		t.Fatalf("expected 2 deny rules, got %d", len(merged.Network.DenyList))
	}

	// evil.com should be denied.
	req := httptest.NewRequest("GET", "https://evil.com/page", nil)
	allowed, _, err := merged.Network.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected evil.com denied")
	}

	// DELETE should be denied.
	req = httptest.NewRequest("DELETE", "https://safe.com/resource", nil)
	allowed, _, err = merged.Network.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected DELETE denied")
	}

	// GET to safe.com should be allowed.
	req = httptest.NewRequest("GET", "https://safe.com/page", nil)
	allowed, _, err = merged.Network.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected GET to safe.com allowed")
	}
}

func TestMerge_AllowOnlyPolicies(t *testing.T) {
	a := &Policy{
		Network: NetworkPolicy{
			AllowList: []Rule{{Host: "*.example.com"}},
		},
	}
	b := &Policy{
		Network: NetworkPolicy{
			AllowList: []Rule{{Host: "docs.google.com"}},
		},
	}
	merged, err := Merge(a, b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// example.com allowed.
	req := httptest.NewRequest("GET", "https://app.example.com/", nil)
	allowed, _, err := merged.Network.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected example.com allowed")
	}

	// google docs allowed.
	req = httptest.NewRequest("GET", "https://docs.google.com/", nil)
	allowed, _, err = merged.Network.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected docs.google.com allowed")
	}

	// random site denied.
	req = httptest.NewRequest("GET", "https://random.com/", nil)
	allowed, _, err = merged.Network.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected random.com denied")
	}
}

func TestMerge_MixedDenyAndAllowOnly(t *testing.T) {
	// Policy A: deny-list mode (block evil.com, allow everything else).
	a := &Policy{
		Network: NetworkPolicy{
			DenyList: []Rule{{Host: "*evil.com"}},
		},
	}
	// Policy B: allow-only mode (only allow example.com).
	// Validate() will insert implicit deny-all.
	b := &Policy{
		Network: NetworkPolicy{
			AllowList: []Rule{{Host: "*.example.com"}},
		},
	}
	// Validate B first so implicit deny-all is expanded.
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}

	merged, err := Merge(a, b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// example.com: allowed (matches allow exception).
	req := httptest.NewRequest("GET", "https://app.example.com/", nil)
	allowed, _, err := merged.Network.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected example.com allowed")
	}

	// evil.com: denied (matches deny from both policies).
	req = httptest.NewRequest("GET", "https://evil.com/", nil)
	allowed, _, err = merged.Network.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected evil.com denied")
	}

	// google.com: denied (catch-all deny from B, no allow exception).
	req = httptest.NewRequest("GET", "https://google.com/", nil)
	allowed, _, err = merged.Network.IsAllowed(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected google.com denied by catch-all")
	}
}

func TestMerge_ActionsDenyLists(t *testing.T) {
	a := &Policy{
		Actions: ActionsPolicy{DenyList: []string{"eval"}},
	}
	b := &Policy{
		Actions: ActionsPolicy{DenyList: []string{"upload"}},
	}
	merged, err := Merge(a, b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if merged.IsActionAllowed("eval") {
		t.Fatal("expected eval denied")
	}
	if merged.IsActionAllowed("upload") {
		t.Fatal("expected upload denied")
	}
	if !merged.IsActionAllowed("click") {
		t.Fatal("expected click allowed")
	}
}

func TestLoadDir_LoadsFiles(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "a-block-evil.yml"), []byte(`
network:
  deny:
    - host: "*evil.com"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b-read-only.yaml"), []byte(`
network:
  deny:
    - method: "DELETE"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	policies, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(policies) != 2 {
		t.Fatalf("expected 2 policies, got %d", len(policies))
	}
	if policies[0].Path != filepath.Join(dir, "a-block-evil.yml") {
		t.Errorf("policies[0].Path: got %q, want %q", policies[0].Path, filepath.Join(dir, "a-block-evil.yml"))
	}
	if policies[1].Path != filepath.Join(dir, "b-read-only.yaml") {
		t.Errorf("policies[1].Path: got %q, want %q", policies[1].Path, filepath.Join(dir, "b-read-only.yaml"))
	}
}

func TestLoadDir_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	policies, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if policies != nil {
		t.Fatal("expected nil for empty directory")
	}
}

func TestLoadDir_InvalidFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.yml"), []byte(`
network:
  deny:
    - host: "*evil|.com"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadDir(dir)
	if err == nil {
		t.Fatal("expected error for invalid policy file")
	}
	if !strings.Contains(err.Error(), "bad.yml") {
		t.Errorf("expected filename in error, got: %s", err)
	}
}

func TestMerge_ActionsAllowOnly(t *testing.T) {
	a := &Policy{
		Actions: ActionsPolicy{AllowList: []string{"screenshot"}},
	}
	b := &Policy{
		Actions: ActionsPolicy{AllowList: []string{"list"}},
	}
	// Validate to expand implicit deny-all.
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}

	merged, err := Merge(a, b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !merged.IsActionAllowed("screenshot") {
		t.Fatal("expected screenshot allowed")
	}
	if !merged.IsActionAllowed("list") {
		t.Fatal("expected list allowed")
	}
	if merged.IsActionAllowed("eval") {
		t.Fatal("expected eval denied")
	}
}
