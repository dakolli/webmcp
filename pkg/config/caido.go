package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const defaultCaidoURL = "http://127.0.0.1:8080/graphql"

// CaidoTemplate is the exact seed text written to webmcp.toml on first use.
const CaidoTemplate = `# web-mcp configuration. Mode 0600. Never commit this file.

[caido]
# Set enabled = true to register the caido_search MCP tool.
enabled = false
# Caido personal access token. Create one at https://dashboard.caido.io/developer
# Tried as the bearer. A rejected PAT is an error, never a guest fallback.
pat = ""
# Direct instance access token, copied from the Caido UI console:
#   JSON.parse(localStorage.CAIDO_AUTHENTICATION).accessToken
# Sent as the bearer with no exchange. It expires after about 7 days.
access_token = ""
# Allow a guest session when neither pat nor access_token is set.
# With a credential present the tool never falls back to guest.
allow_guest = true
# Caido GraphQL endpoint.
url = "http://127.0.0.1:8080/graphql"
`

// Capture modes a launch applies to the instance's passthrough filter.
const (
	CaptureAuto    = ""
	CaptureAll     = "all"
	CaptureInScope = "in-scope"
)

// CaidoFile holds the resolved Caido settings and the file they came from.
// Project, ScopeName, ScopeAllow, ScopeDeny and Capture are applied to the
// instance at launch; an empty value means the instance keeps its own.
type CaidoFile struct {
	Enabled     bool
	PAT         string
	AccessToken string
	AllowGuest  bool
	URL         string
	Project     string
	ScopeName   string
	ScopeAllow  []string
	ScopeDeny   []string
	Capture     string
	Path        string
}

// CaptureMode resolves caido.capture case-insensitively. An empty value or
// "auto" writes nothing, so the instance keeps its stored filter.
func (cf CaidoFile) CaptureMode() (string, error) {
	switch strings.ToLower(strings.TrimSpace(cf.Capture)) {
	case "", "auto":
		return CaptureAuto, nil
	case "all":
		return CaptureAll, nil
	case "in-scope":
		return CaptureInScope, nil
	default:
		return "", fmt.Errorf("caido: capture %q is not a mode — use all or in-scope", cf.Capture)
	}
}

var (
	caidoMu     sync.RWMutex
	activeCaido CaidoFile
)

// SetActiveCaido records the resolved Caido settings for the MCP tool layer.
func SetActiveCaido(cf CaidoFile) {
	caidoMu.Lock()
	activeCaido = cf
	caidoMu.Unlock()
}

// ActiveCaido returns the Caido settings recorded at process startup.
func ActiveCaido() CaidoFile {
	caidoMu.RLock()
	defer caidoMu.RUnlock()
	return activeCaido
}

// CaidoConfigPath returns the absolute path to webmcp.toml in the config dir.
func CaidoConfigPath() string {
	dir, err := ConfigDir()
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return filepath.Join(DirName, "webmcp.toml")
		}
		return filepath.Join(home, ".config", DirName, "webmcp.toml")
	}
	return filepath.Join(dir, "webmcp.toml")
}

// LoadCaido reads webmcp.toml. A missing file yields defaults with no error;
// allow_guest defaults to true and a missing [caido] section leaves Enabled
// false.
func LoadCaido() (CaidoFile, error) {
	path := CaidoConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CaidoFile{AllowGuest: true}, nil
		}
		return CaidoFile{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	doc, err := ParseTOML(data)
	if err != nil {
		return CaidoFile{}, err
	}

	cf := CaidoFile{Path: path, AllowGuest: true}
	if doc["caido"] == nil {
		return cf, nil
	}
	enabled, ok, err := doc.Bool("caido", "enabled")
	if err != nil {
		return CaidoFile{}, fmt.Errorf("config: webmcp.toml caido.enabled: %w", err)
	}
	if ok {
		cf.Enabled = enabled
	}
	if pat, ok := doc.String("caido", "pat"); ok {
		cf.PAT = pat
	}
	if token, ok := doc.String("caido", "access_token"); ok {
		cf.AccessToken = token
	}
	allowGuest, ok, err := doc.Bool("caido", "allow_guest")
	if err != nil {
		return CaidoFile{}, fmt.Errorf("config: webmcp.toml caido.allow_guest: %w", err)
	}
	if ok {
		cf.AllowGuest = allowGuest
	}
	if url, ok := doc.String("caido", "url"); ok {
		cf.URL = url
	}
	if cf.URL == "" {
		cf.URL = defaultCaidoURL
	}
	if project, ok := doc.String("caido", "project"); ok {
		cf.Project = strings.TrimSpace(project)
	}
	if scope, ok := doc.String("caido", "scope_name"); ok {
		cf.ScopeName = strings.TrimSpace(scope)
	}
	allow, _, err := doc.Strings("caido", "scope_allow")
	if err != nil {
		return CaidoFile{}, fmt.Errorf("config: webmcp.toml caido.scope_allow: %w", err)
	}
	if len(allow) > 0 {
		cf.ScopeAllow = allow
	}
	deny, _, err := doc.Strings("caido", "scope_deny")
	if err != nil {
		return CaidoFile{}, fmt.Errorf("config: webmcp.toml caido.scope_deny: %w", err)
	}
	if len(deny) > 0 {
		cf.ScopeDeny = deny
	}
	if capture, ok := doc.String("caido", "capture"); ok {
		cf.Capture = strings.TrimSpace(capture)
	}
	return cf, nil
}

// ResolveCaido loads webmcp.toml and applies token precedence: the explicit
// flags, then CAIDO_PAT and CAIDO_ACCESS_TOKEN, then the file. The flag also
// force-enables the tool. Returned errors never contain a token.
func ResolveCaido(
	flagOn bool,
	flagPAT string,
	flagToken string,
) (CaidoFile, error) {
	cf, err := LoadCaido()
	if err != nil {
		return CaidoFile{}, err
	}
	if flagToken != "" {
		cf.AccessToken = flagToken
	} else if envToken := os.Getenv("CAIDO_ACCESS_TOKEN"); envToken != "" {
		cf.AccessToken = envToken
	}
	if flagPAT != "" {
		cf.PAT = flagPAT
	} else if envPAT := os.Getenv("CAIDO_PAT"); envPAT != "" {
		cf.PAT = envPAT
	}
	cf.Enabled = cf.Enabled || flagOn
	if cf.URL == "" {
		cf.URL = defaultCaidoURL
	}
	return cf, nil
}

// SeedCaido creates webmcp.toml with CaidoTemplate when it does not exist. An
// existing file is never overwritten or read.
func SeedCaido() error {
	dir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: create config dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "webmcp.toml")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("config: seed %s: %w", path, err)
	}
	if _, err := f.WriteString(CaidoTemplate); err != nil {
		f.Close()
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("config: sync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("config: close %s: %w", path, err)
	}
	return nil
}
