package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const defaultCaidoURL = "http://127.0.0.1:8080/graphql"

// CaidoTemplate is the exact seed text written to webmcp.toml on first use.
const CaidoTemplate = `# web-mcp configuration. Mode 0600. Never commit this file.

[caido]
# Set enabled = true to register the caido_search MCP tool.
enabled = false
# Caido personal access token. Create one at https://dashboard.caido.io/developer
# Leave empty to use guest access when the instance allows it.
pat = ""
# Caido GraphQL endpoint.
url = "http://127.0.0.1:8080/graphql"
`

// CaidoFile holds the resolved Caido settings and the file they came from.
type CaidoFile struct {
	Enabled bool
	PAT     string
	URL     string
	Path    string
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

// LoadCaido reads webmcp.toml. A missing file yields the zero value with no
// error; a missing [caido] section leaves Enabled false.
func LoadCaido() (CaidoFile, error) {
	path := CaidoConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CaidoFile{}, nil
		}
		return CaidoFile{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	doc, err := ParseTOML(data)
	if err != nil {
		return CaidoFile{}, err
	}

	cf := CaidoFile{Path: path}
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
	if url, ok := doc.String("caido", "url"); ok {
		cf.URL = url
	}
	if cf.URL == "" {
		cf.URL = defaultCaidoURL
	}
	return cf, nil
}

// ResolveCaido loads webmcp.toml and applies token precedence: the explicit
// flag, then CAIDO_PAT, then the file. The flag also force-enables the tool.
// Returned errors never contain the token.
func ResolveCaido(flagOn bool, flagPAT string) (CaidoFile, error) {
	cf, err := LoadCaido()
	if err != nil {
		return CaidoFile{}, err
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
