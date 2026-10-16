package policy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

// Policy is the top-level structure loaded from a YAML policy file.
type Policy struct {
	Version     string        `mapstructure:"version"`
	Description string        `mapstructure:"description"`
	Path        string        `mapstructure:"-"` // absolute file path or URL, set by Load/LoadDir
	Network     NetworkPolicy `mapstructure:"network"`
	Actions     ActionsPolicy `mapstructure:"actions"`
}

// Validate initializes derived fields and validates the policy.
// Compiles regex patterns, lowercases action names, and checks for errors.
func (p *Policy) Validate() error {
	if !p.Network.IsEnabled() && !p.Actions.IsEnabled() {
		return fmt.Errorf("policy has no rules: expected network or actions configuration")
	}

	// If allow rules exist but no deny rules, insert an implicit deny-all.
	// Network: an empty Rule{} matches everything (all nil regexes = no checks fail).
	// Actions: "*" matches every action name.
	if len(p.Network.DenyList) == 0 && len(p.Network.AllowList) > 0 {
		p.Network.DenyList = []Rule{{}}
	}
	if len(p.Actions.DenyList) == 0 && len(p.Actions.AllowList) > 0 {
		p.Actions.DenyList = []string{"*"}
	}

	if err := compileRules(p.Network.DenyList); err != nil {
		return fmt.Errorf("deny rule: %w", err)
	}
	if err := compileRules(p.Network.AllowList); err != nil {
		return fmt.Errorf("allow rule: %w", err)
	}

	// Normalize action names: trim whitespace and lowercase.
	for i, name := range p.Actions.DenyList {
		p.Actions.DenyList[i] = strings.ToLower(strings.TrimSpace(name))
	}
	for i, name := range p.Actions.AllowList {
		p.Actions.AllowList[i] = strings.ToLower(strings.TrimSpace(name))
	}

	return validateActions(p.Actions)
}

// ActionsPolicy defines which browser actions are allowed or denied.
// Deny rules are checked first; allow rules act as exceptions.
// If allow rules exist but no deny rules, an implicit deny-all is inserted.
type ActionsPolicy struct {
	DenyList  []string `mapstructure:"deny"`
	AllowList []string `mapstructure:"allow"`
}

// knownActions is the set of valid action names for validation: the
// webtool MCP tool's verb inventory (config names are normalized to
// lowercase at load).
var knownActions = map[string]bool{
	"open": true, "close": true, "back": true, "forward": true,
	"reload": true, "click": true, "hover": true, "scroll": true,
	"fill": true, "select": true, "type": true, "upload": true,
	"eval": true, "screenshot": true, "list": true,
}

// IsActionAllowed checks if the given action is allowed by the policy.
// Deny rules are checked first. If a deny matches, allow rules are checked
// as exceptions. "*" in the deny list matches every action.
// Returns true if no policy or no action rules are configured.
// Matching is case-insensitive: rule names are normalized to lowercase
// at load, and the runtime name is lowercased here.
func (p *Policy) IsActionAllowed(action string) bool {
	if p == nil {
		return true
	}
	a := p.Actions
	if len(a.DenyList) == 0 && len(a.AllowList) == 0 {
		return true
	}
	action = strings.ToLower(action)
	denied := slices.Contains(a.DenyList, action) || slices.Contains(a.DenyList, "*")
	if !denied {
		return true
	}
	return slices.Contains(a.AllowList, action)
}

// NetworkPolicy defines network request interception rules.
type NetworkPolicy struct {
	DenyList  []Rule `mapstructure:"deny"`
	AllowList []Rule `mapstructure:"allow"`
}

// IsEnabled returns true if any network rules are configured.
func (p NetworkPolicy) IsEnabled() bool {
	return len(p.DenyList) > 0 || len(p.AllowList) > 0
}

// IsEnabled returns true if any action rules are configured.
func (a ActionsPolicy) IsEnabled() bool {
	return len(a.DenyList) > 0 || len(a.AllowList) > 0
}

// Rule matches network requests by method, URL component patterns, and body regex.
type Rule struct {
	Method string `mapstructure:"method"` // regex pattern, case-insensitive
	Host   string `mapstructure:"host"`   // CDP wildcard pattern matched against parsed URL host
	Path   string `mapstructure:"path"`   // regex pattern matched against parsed URL path
	Query  string `mapstructure:"query"`  // regex pattern matched against parsed URL query
	Header string `mapstructure:"header"` // regex pattern matched against serialized request headers
	Body   string `mapstructure:"body"`   // regex pattern

	// Compiled patterns, set by Load.
	methodRegex *regexp.Regexp
	hostRegex   *regexp.Regexp
	pathRegex   *regexp.Regexp
	queryRegex  *regexp.Regexp
	headerRegex *regexp.Regexp
	bodyRegex   *regexp.Regexp
}

// String returns a human-readable description of the rule.
func (r Rule) String() string {
	var parts []string
	if r.Method != "" {
		parts = append(parts, "method="+r.Method)
	}
	if r.Host != "" {
		parts = append(parts, "host="+r.Host)
	}
	if r.Path != "" {
		parts = append(parts, "path="+r.Path)
	}
	if r.Query != "" {
		parts = append(parts, "query="+r.Query)
	}
	if r.Header != "" {
		parts = append(parts, "header="+r.Header)
	}
	if r.Body != "" {
		parts = append(parts, "body="+r.Body)
	}
	if len(parts) == 0 {
		return "deny all"
	}
	return strings.Join(parts, " ")
}

// Load reads and validates a policy from a file path or URL.
// If source starts with http:// or https://, it is fetched via HTTP.
// Returns an error if the source cannot be read, parsed, or contains invalid patterns.
// loadFromFile creates a Viper instance and reads a policy from a file path.
func loadFromFile(source string) (*viper.Viper, error) {
	v := viper.New()
	v.SetConfigType("yaml")
	v.SetConfigFile(source)
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading policy file: %w", err)
	}
	return v, nil
}

// loadFromURL creates a Viper instance and reads a policy from an HTTP(S) URL.
func loadFromURL(ctx context.Context, source string) (*viper.Viper, error) {
	v := viper.New()
	v.SetConfigType("yaml")
	body, err := fetchURL(ctx, source)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	if err := v.ReadConfig(body); err != nil {
		return nil, fmt.Errorf("reading policy: %w", err)
	}
	return v, nil
}

// Watch loads a policy and watches for file changes.
// The onChange callback is called with the new policy (or error) when the file changes.
// For URL sources, the policy is loaded once without watching.
func Watch(ctx context.Context, source string, onChange func(*Policy, error)) (*Policy, error) {
	isURL := strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://")

	var v *viper.Viper
	var err error
	if isURL {
		v, err = loadFromURL(ctx, source)
	} else {
		v, err = loadFromFile(source)
	}
	if err != nil {
		return nil, err
	}

	p := &Policy{}
	if err := v.Unmarshal(p); err != nil {
		return nil, fmt.Errorf("parsing policy: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}

	if isURL {
		p.Path = source
		return p, nil
	}

	abs, err := filepath.Abs(source)
	if err != nil {
		return nil, fmt.Errorf("resolving policy path: %w", err)
	}
	p.Path = abs

	v.OnConfigChange(func(_ fsnotify.Event) {
		newP := &Policy{}
		if err := v.ReadInConfig(); err != nil {
			onChange(nil, err)
			return
		}
		if err := v.Unmarshal(newP); err != nil {
			onChange(nil, fmt.Errorf("parsing policy: %w", err))
			return
		}
		if err := newP.Validate(); err != nil {
			onChange(nil, err)
			return
		}
		newP.Path = abs
		onChange(newP, nil)
	})
	v.WatchConfig()

	return p, nil
}

// Load reads and validates a policy from a file path or HTTP(S) URL.
func Load(ctx context.Context, source string) (*Policy, error) {
	isURL := strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://")

	var v *viper.Viper
	var err error
	if isURL {
		v, err = loadFromURL(ctx, source)
	} else {
		v, err = loadFromFile(source)
	}
	if err != nil {
		return nil, err
	}

	p := &Policy{}
	if err := v.Unmarshal(p); err != nil {
		return nil, fmt.Errorf("parsing policy: %w", err)
	}

	if err := p.Validate(); err != nil {
		return nil, err
	}

	if isURL {
		p.Path = source
	} else {
		abs, err := filepath.Abs(source)
		if err != nil {
			return nil, fmt.Errorf("resolving policy path: %w", err)
		}
		p.Path = abs
	}

	return p, nil
}

// LoadDir loads all policy files (*.yml, *.yaml) from a directory.
// Files are loaded in sorted order for deterministic results.
// Returns an error if the directory cannot be read or any file fails to load.
func LoadDir(dir string) ([]*Policy, error) {
	var files []string
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return nil, fmt.Errorf("reading policy directory: %w", err)
		}
		files = append(files, matches...)
	}
	sort.Strings(files)

	if len(files) == 0 {
		return nil, nil
	}

	policies := make([]*Policy, 0, len(files))
	for _, f := range files {
		p, err := Load(context.Background(), f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		policies = append(policies, p)
	}

	return policies, nil
}

// WatchDir watches a policy directory for changes.
// On any file change (create, remove, rename, modify), the directory is re-loaded
// and onChange is called with the new set of policies (or the error).
func WatchDir(dir string, onChange func([]*Policy, error)) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating directory watcher: %w", err)
	}
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return fmt.Errorf("watching policy directory: %w", err)
	}

	go func() {
		for {
			select {
			case _, ok := <-watcher.Events:
				if !ok {
					return
				}
				newPolicies, err := LoadDir(dir)
				onChange(newPolicies, err)
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				onChange(nil, err)
			}
		}
	}()

	return nil
}

// validateActions checks that the actions policy is valid.
// Returns an error if any action name is not recognized.
// "*" is a valid deny entry meaning "deny all actions".
func validateActions(a ActionsPolicy) error {
	for _, name := range a.DenyList {
		if name != "*" && !knownActions[name] {
			return fmt.Errorf("unknown action %q in actions deny list", name)
		}
	}
	for _, name := range a.AllowList {
		if !knownActions[name] {
			return fmt.Errorf("unknown action %q in actions allow list", name)
		}
	}
	return nil
}

// Merge combines multiple validated policies into a single effective policy.
// Deny and allow rules are concatenated. The merged result is validated.
// Returns an error if no policies are provided or validation fails.
func Merge(policies ...*Policy) (*Policy, error) {
	if len(policies) == 0 {
		return nil, fmt.Errorf("no policies to merge")
	}
	if len(policies) == 1 {
		return policies[0], nil
	}

	merged := &Policy{
		Version: policies[0].Version,
	}
	for _, p := range policies {
		merged.Network.DenyList = append(merged.Network.DenyList, p.Network.DenyList...)
		merged.Network.AllowList = append(merged.Network.AllowList, p.Network.AllowList...)
		merged.Actions.DenyList = append(merged.Actions.DenyList, p.Actions.DenyList...)
		merged.Actions.AllowList = append(merged.Actions.AllowList, p.Actions.AllowList...)
	}

	if err := merged.Validate(); err != nil {
		return nil, fmt.Errorf("merged policy: %w", err)
	}
	return merged, nil
}

// compileRules compiles host, path, query, and body patterns for a slice of rules.
func compileRules(rules []Rule) error {
	for i := range rules {
		r := &rules[i]
		if r.Method != "" {
			re, err := regexp.Compile("(?i)" + r.Method)
			if err != nil {
				return fmt.Errorf("invalid method regex %q: %w", r.Method, err)
			}
			r.methodRegex = re
		}
		if r.Host != "" {
			if err := validateHostPattern(r.Host); err != nil {
				return err
			}
			// Escape the whole pattern, then turn the escaped wildcards
			// back into regex, so "." matches only a literal dot.
			pattern := strings.NewReplacer(`\*`, `.*`, `\?`, `.`).Replace(regexp.QuoteMeta(r.Host))
			re, err := regexp.Compile(`\A` + pattern + `\z`)
			if err != nil {
				return fmt.Errorf("invalid host pattern %q: %w", r.Host, err)
			}
			r.hostRegex = re
		}
		if r.Path != "" {
			re, err := regexp.Compile(r.Path)
			if err != nil {
				return fmt.Errorf("invalid path regex %q: %w", r.Path, err)
			}
			r.pathRegex = re
		}
		if r.Query != "" {
			re, err := regexp.Compile(r.Query)
			if err != nil {
				return fmt.Errorf("invalid query regex %q: %w", r.Query, err)
			}
			r.queryRegex = re
		}
		if r.Header != "" {
			re, err := regexp.Compile(r.Header)
			if err != nil {
				return fmt.Errorf("invalid header regex %q: %w", r.Header, err)
			}
			r.headerRegex = re
		}
		if r.Body != "" {
			re, err := regexp.Compile(r.Body)
			if err != nil {
				return fmt.Errorf("invalid body regex %q: %w", r.Body, err)
			}
			r.bodyRegex = re
		}
	}
	return nil
}

// validateHostPattern checks for regex metacharacters in host patterns.
// Only applied to host — path and query can legitimately contain these characters.
func validateHostPattern(host string) error {
	if strings.ContainsAny(host, `|\^$`) {
		return fmt.Errorf("invalid host pattern in policy: %q. Regular expressions are not supported. Only wildcard characters * and ? are supported", host)
	}
	return nil
}

// checkRequest validates that the request has the fields needed for policy matching.
func checkRequest(r *http.Request) error {
	if r == nil {
		return fmt.Errorf("nil request")
	}
	if r.URL == nil {
		return fmt.Errorf("request has nil URL")
	}
	return nil
}

// IsAllowed checks if the request is allowed by the network policy.
// Deny rules are checked first. If a deny matches, allow rules are checked
// as exceptions. If no allow exception is found, the request is denied.
// Returns (false, matched deny rule, nil) if denied.
// Returns (true, nil, nil) if allowed (no deny match).
// Returns (true, matched allow rule, nil) if allowed by exception.
func (p *NetworkPolicy) IsAllowed(r *http.Request) (bool, *Rule, error) {
	if err := checkRequest(r); err != nil {
		return false, nil, err
	}

	// Read body once upfront if any rule needs it.
	var body string
	if p.needsBody() {
		var err error
		body, err = readBody(r)
		if err != nil {
			return false, nil, err
		}
	}

	denied, denyRule := p.matchRules(p.DenyList, r, body)
	if !denied {
		return true, nil, nil
	}

	excepted, allowRule := p.matchRules(p.AllowList, r, body)
	if excepted {
		return true, allowRule, nil
	}

	return false, denyRule, nil
}

// needsBody returns true if any rule has a body pattern.
func (p *NetworkPolicy) needsBody() bool {
	for i := range p.DenyList {
		if p.DenyList[i].bodyRegex != nil {
			return true
		}
	}
	for i := range p.AllowList {
		if p.AllowList[i].bodyRegex != nil {
			return true
		}
	}
	return false
}

// matchRules checks if any rule in the list matches the request.
// URL components are extracted from the parsed request URL.
// Header serialization is deferred until a rule actually needs it.
func (p *NetworkPolicy) matchRules(rules []Rule, r *http.Request, body string) (bool, *Rule) {
	// Serialize headers lazily — only if a rule has a header regex.
	var header string
	var headerReady bool

	for i := range rules {
		rule := &rules[i]

		if rule.methodRegex != nil && !rule.methodRegex.MatchString(r.Method) {
			continue
		}
		if rule.hostRegex != nil && !rule.hostRegex.MatchString(r.URL.Host) {
			continue
		}
		if rule.pathRegex != nil && !rule.pathRegex.MatchString(r.URL.Path) {
			continue
		}
		if rule.queryRegex != nil && !rule.queryRegex.MatchString(r.URL.RawQuery) {
			continue
		}
		if rule.headerRegex != nil {
			if !headerReady {
				header = serializeHeader(r.Header)
				headerReady = true
			}
			if !rule.headerRegex.MatchString(header) {
				continue
			}
		}
		if rule.bodyRegex != nil && !rule.bodyRegex.MatchString(body) {
			continue
		}

		return true, rule
	}

	return false, nil
}

// DenyPatterns returns deduplicated CDP URL patterns from deny rules for registration.
// Constructs coarse patterns from host/path fields. If any rule has no host, returns ["*"].
func (p *NetworkPolicy) DenyPatterns() []string {
	seen := make(map[string]bool)
	for _, r := range p.DenyList {
		pattern := cdpPattern(&r)
		if pattern == "*" {
			return []string{"*"}
		}
		seen[pattern] = true
	}
	patterns := make([]string, 0, len(seen))
	for u := range seen {
		patterns = append(patterns, u)
	}
	sort.Strings(patterns)
	return patterns
}

// cdpPattern constructs a coarse CDP Fetch URL pattern from a rule's host.
// Only the host field is used because it's the only field that uses CDP wildcard
// syntax. All other fields (path, query, method, etc.) are regexes and cannot
// be safely embedded in a CDP pattern.
func cdpPattern(r *Rule) string {
	if r.Host == "" {
		return "*"
	}
	return "*://" + r.Host + "/*"
}

// serializeHeader formats request headers in wire format ("Name: Value\r\n").
// Uses http.Header.Write which outputs canonical header names.
func serializeHeader(h http.Header) string {
	var b strings.Builder
	h.Write(&b)
	return b.String()
}

// fetchURL fetches a policy from a URL and returns the response body.
func fetchURL(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching policy: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching policy: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("fetching policy: %s", resp.Status)
	}
	return resp.Body, nil
}

// readBody reads the request body and returns it as a string.
// The body is reset so the request can still be forwarded.
// Returns empty string if the body is nil.
func readBody(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", nil
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return "", fmt.Errorf("reading request body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return string(data), nil
}
