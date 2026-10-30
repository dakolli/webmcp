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

func TestLoadCaidoProjectScope(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := CaidoConfigPath()
	body := "[caido]\nproject = \" webmcp \"\nscope_name = \"target\"\n" +
		"scope_allow = [\"daft.ie\", \"*.daft.ie\"]\nscope_deny = [\"ads.daft.ie\"]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cf, err := LoadCaido()
	if err != nil {
		t.Fatalf("LoadCaido: %v", err)
	}
	if cf.Project != "webmcp" {
		t.Errorf("Project = %q, want the trimmed name", cf.Project)
	}
	if cf.ScopeName != "target" {
		t.Errorf("ScopeName = %q, want target", cf.ScopeName)
	}
	if len(cf.ScopeAllow) != 2 || cf.ScopeAllow[0] != "daft.ie" || cf.ScopeAllow[1] != "*.daft.ie" {
		t.Errorf("ScopeAllow = %v, want the parsed array", cf.ScopeAllow)
	}
	if len(cf.ScopeDeny) != 1 || cf.ScopeDeny[0] != "ads.daft.ie" {
		t.Errorf("ScopeDeny = %v, want the parsed array", cf.ScopeDeny)
	}

	empty := "[caido]\nproject = \"\"\nscope_allow = []\nscope_deny = []\n"
	if err := os.WriteFile(path, []byte(empty), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	cf, err = LoadCaido()
	if err != nil {
		t.Fatalf("LoadCaido(empty): %v", err)
	}
	if cf.Project != "" || cf.ScopeName != "" || cf.ScopeAllow != nil || cf.ScopeDeny != nil {
		t.Errorf("empty keys = %q/%q/%v/%v, want unset",
			cf.Project, cf.ScopeName, cf.ScopeAllow, cf.ScopeDeny)
	}
}

func TestCaptureMode(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty keeps the instance filter", raw: "", want: CaptureAuto},
		{name: "auto keeps the instance filter", raw: "auto", want: CaptureAuto},
		{name: "all resets the filter", raw: "all", want: CaptureAll},
		{name: "in-scope captures the allowlist", raw: "in-scope", want: CaptureInScope},
		{name: "case insensitive", raw: " ALL ", want: CaptureAll},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CaidoFile{Capture: tc.raw}.CaptureMode()
			if err != nil {
				t.Fatalf("CaptureMode(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("CaptureMode(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}

	_, err := (CaidoFile{Capture: "none"}).CaptureMode()
	want := `caido: capture "none" is not a mode — use all or in-scope`
	if err == nil || err.Error() != want {
		t.Fatalf("CaptureMode(bogus) = %v, want %q", err, want)
	}
}
