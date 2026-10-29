package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestCaidoTemplateParses(t *testing.T) {
	doc, err := ParseTOML([]byte(CaidoTemplate))
	if err != nil {
		t.Fatalf("ParseTOML(CaidoTemplate): %v", err)
	}

	enabled, ok, err := doc.Bool("caido", "enabled")
	if err != nil || !ok {
		t.Fatalf("caido.enabled: ok=%v err=%v", ok, err)
	}
	if enabled {
		t.Error("caido.enabled = true, want false")
	}

	pat, ok := doc.String("caido", "pat")
	if !ok || pat != "" {
		t.Errorf("caido.pat = %q, want empty", pat)
	}

	token, ok := doc.String("caido", "access_token")
	if !ok || token != "" {
		t.Errorf("caido.access_token = %q, want empty", token)
	}

	allowGuest, ok, err := doc.Bool("caido", "allow_guest")
	if err != nil || !ok {
		t.Fatalf("caido.allow_guest: ok=%v err=%v", ok, err)
	}
	if !allowGuest {
		t.Error("caido.allow_guest = false, want true")
	}

	url, ok := doc.String("caido", "url")
	if !ok || url != defaultCaidoURL {
		t.Errorf("caido.url = %q, want %q", url, defaultCaidoURL)
	}
}

func TestLoadCaidoAllowGuestDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cf, err := LoadCaido()
	if err != nil {
		t.Fatalf("LoadCaido(missing file): %v", err)
	}
	if !cf.AllowGuest {
		t.Error("missing file: AllowGuest = false, want true")
	}

	path := CaidoConfigPath()
	if err := os.WriteFile(path, []byte("[caido]\nenabled = true\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cf, err = LoadCaido()
	if err != nil {
		t.Fatalf("LoadCaido: %v", err)
	}
	if !cf.AllowGuest {
		t.Error("AllowGuest = false, want the absent key to default true")
	}

	if err := os.WriteFile(path, []byte("[caido]\nallow_guest = false\n"), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	cf, err = LoadCaido()
	if err != nil {
		t.Fatalf("LoadCaido: %v", err)
	}
	if cf.AllowGuest {
		t.Error("AllowGuest = true, want the explicit false value")
	}
}

func TestParseTOMLErrorsCarryLine(t *testing.T) {
	tests := []struct {
		name string
		data string
		line int
		want string
	}{
		{"bad section", "[caido\nenabled = false\n", 1, "malformed section"},
		{"duplicate key", "[caido]\npat = \"a\"\npat = \"b\"\n", 3, "duplicate key"},
		{"missing value", "[caido]\npat =\n", 2, "unsupported value"},
		{"bare text", "[caido]\npat = nope\n", 2, "unsupported value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseTOML([]byte(tt.data))
			if err == nil {
				t.Fatal("expected parse error")
			}
			var te *TOMLError
			if !errors.As(err, &te) {
				t.Fatalf("error %v is not a *TOMLError", err)
			}
			if te.Line != tt.line {
				t.Errorf("line = %d, want %d", te.Line, tt.line)
			}
			if !strings.Contains(err.Error(), "line "+strconv.Itoa(tt.line)) {
				t.Errorf("error %q does not name line %d", err, tt.line)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestResolveCaidoPrecedence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := CaidoConfigPath()
	body := "[caido]\nenabled = false\npat = \"file-token\"\n" +
		"access_token = \"file-access\"\nurl = \"http://caido.local/graphql\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("CAIDO_PAT", "env-token")
	t.Setenv("CAIDO_ACCESS_TOKEN", "env-access")
	cf, err := ResolveCaido(false, "", "")
	if err != nil {
		t.Fatalf("ResolveCaido(no flag): %v", err)
	}
	if cf.PAT != "env-token" {
		t.Errorf("PAT = %q, want the env token", cf.PAT)
	}
	if cf.AccessToken != "env-access" {
		t.Errorf("AccessToken = %q, want the env token", cf.AccessToken)
	}
	if cf.Enabled {
		t.Error("Enabled = true, want file value false")
	}
	if cf.URL != "http://caido.local/graphql" {
		t.Errorf("URL = %q, want the file URL", cf.URL)
	}

	cf, err = ResolveCaido(true, "flag-token", "flag-access")
	if err != nil {
		t.Fatalf("ResolveCaido(flag): %v", err)
	}
	if cf.PAT != "flag-token" {
		t.Errorf("PAT = %q, want the flag token", cf.PAT)
	}
	if cf.AccessToken != "flag-access" {
		t.Errorf("AccessToken = %q, want the flag token", cf.AccessToken)
	}
	if !cf.Enabled {
		t.Error("Enabled = false, want flag to force it on")
	}
}

func TestSeedCaidoCreatesOnceAndNeverOverwrites(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := CaidoConfigPath()

	if err := SeedCaido(); err != nil {
		t.Fatalf("SeedCaido: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat seeded file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seeded file: %v", err)
	}
	if string(data) != CaidoTemplate {
		t.Error("seeded bytes differ from CaidoTemplate")
	}

	const sentinel = "keep me\n"
	if err := os.WriteFile(path, []byte(sentinel), 0o600); err != nil {
		t.Fatalf("rewrite seed: %v", err)
	}
	if err := SeedCaido(); err != nil {
		t.Fatalf("SeedCaido on existing file: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read existing file: %v", err)
	}
	if string(data) != sentinel {
		t.Error("SeedCaido overwrote an existing file")
	}
}

func TestCaidoErrorsOmitToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := CaidoConfigPath()
	if err := os.WriteFile(path, []byte("[caido\npat = \"file_secret\"\n"), 0o600); err != nil {
		t.Fatalf("write malformed config: %v", err)
	}
	t.Setenv("CAIDO_PAT", "caido_env_secret")

	_, err := ResolveCaido(true, "caido_flag_secret", "caido_token_secret")
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if strings.Contains(err.Error(), "caido_") {
		t.Error("error message contains a token")
	}
	if strings.Contains(err.Error(), "file_secret") {
		t.Error("error message contains a token")
	}
}
