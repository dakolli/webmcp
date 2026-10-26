package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProxyCredential is the username/password pair for one upstream proxy.
type ProxyCredential struct {
	// Username is the proxy account name.
	Username string `json:"username"`
	// Password is the proxy account password.
	Password string `json:"password"`
}

// proxyFile is the on-disk shape of proxy.json: credentials keyed by proxy
// host:port.
type proxyFile struct {
	// Proxies maps a lowercase host:port to its credentials.
	Proxies map[string]ProxyCredential `json:"proxies,omitempty"`
}

// ProxyConfigPath returns the absolute path of proxy.json under the
// browser-mcp config directory.
func ProxyConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "proxy.json"), nil
}

// LoadProxyCredentials reads proxy.json and returns its credentials keyed by
// lowercase proxy host:port. A missing file yields an empty map; an existing
// file must be mode 0600 and contain valid JSON.
func LoadProxyCredentials() (map[string]ProxyCredential, error) {
	path, err := ProxyConfigPath()
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]ProxyCredential{}, nil
		}
		return nil, fmt.Errorf("config: stat %s: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		return nil, fmt.Errorf(
			"config: proxy credentials %s must have mode 0600 (got %04o); run: chmod 600 %s",
			path,
			perm,
			path,
		)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var pf proxyFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	creds := make(map[string]ProxyCredential, len(pf.Proxies))
	for host, cred := range pf.Proxies {
		creds[strings.ToLower(strings.TrimSpace(host))] = cred
	}
	return creds, nil
}
