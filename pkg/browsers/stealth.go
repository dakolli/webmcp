package browsers

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const (
	// defaultLanguage is the browser locale the launch defaults install.
	defaultLanguage = "en-US"
	// defaultAcceptLanguage is the Accept-Language header the default identity
	// sends. The per-page User-Agent override is given acceptLanguageList of it,
	// because Chromium standardizes that list into this exact header itself.
	defaultAcceptLanguage = "en-US,en;q=0.9"
	// fallbackChromeMajor is the User-Agent major used when the launch-time
	// version probe fails; a launch must never depend on that round trip.
	fallbackChromeMajor = 120
	// versionProbeTimeout bounds the one Browser.getVersion round trip per launch.
	versionProbeTimeout = 3 * time.Second
)

// defaultLanguages is the navigator.languages list the launch defaults produce.
var defaultLanguages = []string{"en-US", "en"}

// browserPlatform is the OS identity shared by the default User-Agent and its
// Sec-CH-UA client hints, so the two can never disagree.
type browserPlatform struct {
	// ua is the platform token inside the User-Agent, e.g. "X11; Linux x86_64".
	ua string
	// navigator is navigator.platform, e.g. "Linux x86_64".
	navigator string
	// chBrand is the Sec-CH-UA platform brand, e.g. "Linux".
	chBrand string
	// chVersion is the Sec-CH-UA platform version, e.g. "10.0.0"; empty is
	// normal on Linux, which does not expose a kernel version.
	chVersion string
	// architecture is the Sec-CH-UA architecture, e.g. "x86".
	architecture string
	// bitness is the Sec-CH-UA bitness, e.g. "64".
	bitness string
}

// StealthProbe is the browser-fingerprint sample ProbeStealth reads from one
// live document. It is JSON-marshalable so a surface can return it verbatim.
type StealthProbe struct {
	// Webdriver reports navigator.webdriver.
	Webdriver bool `json:"webdriver"`
	// Chrome reports whether window.chrome is present.
	Chrome bool `json:"chrome"`
	// Plugins is navigator.plugins.length.
	Plugins int `json:"plugins"`
	// Languages is navigator.languages.
	Languages []string `json:"languages"`
	// Platform is navigator.platform.
	Platform string `json:"platform"`
	// UserAgent is navigator.userAgent.
	UserAgent string `json:"user_agent"`
	// WebGLVendor is the unmasked WebGL vendor.
	WebGLVendor string `json:"webgl_vendor"`
	// WebGLRenderer is the unmasked WebGL renderer.
	WebGLRenderer string `json:"webgl_renderer"`
}

// stealthProbeJS samples the fingerprint in one evaluation.
const stealthProbeJS = `() => {
	const probe = {
		webdriver: navigator.webdriver === true,
		chrome: typeof window.chrome !== "undefined" && window.chrome !== null,
		plugins: navigator.plugins.length,
		languages: Array.from(navigator.languages || []),
		platform: navigator.platform || "",
		user_agent: navigator.userAgent || "",
		webgl_vendor: "",
		webgl_renderer: "",
	};
	try {
		const canvas = document.createElement("canvas");
		const gl = canvas.getContext("webgl") || canvas.getContext("experimental-webgl");
		const ext = gl && gl.getExtension("WEBGL_debug_renderer_info");
		if (ext) {
			probe.webgl_vendor = gl.getParameter(ext.UNMASKED_VENDOR_WEBGL) || "";
			probe.webgl_renderer = gl.getParameter(ext.UNMASKED_RENDERER_WEBGL) || "";
		}
	} catch (err) {
	}
	return probe;
}`

// ProbeStealth samples a live page's automation fingerprint with one
// evaluation, under the page operation lock.
func ProbeStealth(ctx context.Context, page *Page) (StealthProbe, error) {
	if err := page.LockContext(ctx, "probe stealth"); err != nil {
		return StealthProbe{}, err
	}
	defer page.Unlock()
	if page.Rod == nil {
		return StealthProbe{}, fmt.Errorf("browsers: probe stealth: page %q has no live tab", page.ID)
	}
	res, err := page.Rod.Context(ctx).Eval(stealthProbeJS)
	if err != nil {
		return StealthProbe{}, fmt.Errorf("browsers: probe stealth: %w", err)
	}
	var probe StealthProbe
	if err := res.Value.Unmarshal(&probe); err != nil {
		return StealthProbe{}, fmt.Errorf("browsers: decode stealth probe: %w", err)
	}
	return probe, nil
}

// AssertStealthProbe reports whether a probe shows a coherent, non-automated
// browser fingerprint. chromeMajor is the launched Chromium major the
// User-Agent must contain; a non-positive value skips that check.
func AssertStealthProbe(probe StealthProbe, chromeMajor int) error {
	if probe.Webdriver {
		return errors.New("stealth probe: navigator.webdriver is true — the AutomationControlled tell is still exposed")
	}
	if !probe.Chrome {
		return errors.New("stealth probe: window.chrome is missing — the browser is fingerprintable as non-Chrome")
	}
	if probe.Plugins < 1 {
		return errors.New("stealth probe: navigator.plugins is empty")
	}
	if !slices.Equal(probe.Languages, defaultLanguages) {
		return fmt.Errorf("stealth probe: navigator.languages = %v, want %v", probe.Languages, defaultLanguages)
	}
	if chromeMajor > 0 && !strings.Contains(probe.UserAgent, fmt.Sprintf("Chrome/%d.", chromeMajor)) {
		return fmt.Errorf("stealth probe: user agent %q does not contain the launched Chrome major %d", probe.UserAgent, chromeMajor)
	}
	if probe.WebGLRenderer == "" {
		return errors.New("stealth probe: no WebGL renderer reported — a real GPU profile requires a WebGL context")
	}
	renderer := strings.ToLower(probe.WebGLRenderer)
	for _, software := range []string{"swiftshader", "llvmpipe"} {
		if strings.Contains(renderer, software) {
			return fmt.Errorf("stealth probe: WebGL renderer %q is a software rasterizer", probe.WebGLRenderer)
		}
	}
	return nil
}

// hasDevicePreset reports whether device names a real go-rod preset; an empty
// name and "clear" both mean no preset.
func hasDevicePreset(device string) bool {
	device = strings.TrimSpace(device)
	return device != "" && !strings.EqualFold(device, "clear")
}

// defaultPlatform returns the host's browser identity.
func defaultPlatform() browserPlatform {
	return platformFor(runtime.GOOS, runtime.GOARCH)
}

// platformFor maps a GOOS/GOARCH pair to the coherent browser identity table.
func platformFor(goos, goarch string) browserPlatform {
	switch goos {
	case "darwin":
		platform := browserPlatform{
			ua:           "Macintosh; Intel Mac OS X 10_15_7",
			navigator:    "MacIntel",
			chBrand:      "macOS",
			chVersion:    "14.0.0",
			architecture: "x86",
			bitness:      "64",
		}
		if goarch == "arm64" {
			platform.architecture = "arm"
		}
		return platform
	case "windows":
		return browserPlatform{
			ua:           "Windows NT 10.0; Win64; x64",
			navigator:    "Win32",
			chBrand:      "Windows",
			chVersion:    "10.0.0",
			architecture: "x86",
			bitness:      "64",
		}
	}
	if goarch == "arm64" {
		return browserPlatform{
			ua:           "X11; Linux aarch64",
			navigator:    "Linux aarch64",
			chBrand:      "Linux",
			chVersion:    "",
			architecture: "arm",
			bitness:      "64",
		}
	}
	return browserPlatform{
		ua:           "X11; Linux x86_64",
		navigator:    "Linux x86_64",
		chBrand:      "Linux",
		chVersion:    "",
		architecture: "x86",
		bitness:      "64",
	}
}

// buildUserAgent returns the desktop Chrome User-Agent for a launched Chromium
// major version and platform.
func buildUserAgent(major int, platform browserPlatform) string {
	if major <= 0 {
		major = fallbackChromeMajor
	}
	return fmt.Sprintf(
		"Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36",
		platform.ua,
		major,
	)
}

// buildUserAgentMetadata returns the Sec-CH-UA client hints matching
// buildUserAgent's version and platform.
func buildUserAgentMetadata(major int, platform browserPlatform) *proto.EmulationUserAgentMetadata {
	if major <= 0 {
		major = fallbackChromeMajor
	}
	version := fmt.Sprintf("%d.0.0.0", major)
	brandVersion := strconv.Itoa(major)
	return &proto.EmulationUserAgentMetadata{
		Brands: []*proto.EmulationUserAgentBrandVersion{
			{Brand: "Not_A Brand", Version: "8"},
			{Brand: "Chromium", Version: brandVersion},
			{Brand: "Google Chrome", Version: brandVersion},
		},
		FullVersionList: []*proto.EmulationUserAgentBrandVersion{
			{Brand: "Not_A Brand", Version: "8.0.0.0"},
			{Brand: "Chromium", Version: version},
			{Brand: "Google Chrome", Version: version},
		},
		Platform:        platform.chBrand,
		PlatformVersion: platform.chVersion,
		Architecture:    platform.architecture,
		Bitness:         platform.bitness,
	}
}

// acceptLanguageList strips the quality values from an Accept-Language header,
// leaving the bare language list. Network.setUserAgentOverride re-standardizes
// that list into the header and appends its own q ladder, so a q value handed
// back to it is duplicated into the fingerprintable "en-US,en;q=0.9;q=0.9"
// (crbug.com/40853228).
func acceptLanguageList(header string) string {
	ranges := strings.Split(header, ",")
	list := make([]string, 0, len(ranges))
	for _, r := range ranges {
		lang, _, _ := strings.Cut(strings.TrimSpace(r), ";")
		list = append(list, lang)
	}
	return strings.Join(list, ",")
}

// applyDefaultUserAgent installs the coherent default User-Agent, language and
// client hints on a page before it navigates.
func applyDefaultUserAgent(
	ctx context.Context,
	page *rod.Page,
	major int,
) error {
	platform := defaultPlatform()
	req := &proto.NetworkSetUserAgentOverride{
		UserAgent:         buildUserAgent(major, platform),
		AcceptLanguage:    acceptLanguageList(defaultAcceptLanguage),
		Platform:          platform.navigator,
		UserAgentMetadata: buildUserAgentMetadata(major, platform),
	}
	if err := page.Context(ctx).SetUserAgent(req); err != nil {
		return fmt.Errorf("browsers: set default user agent: %w", err)
	}
	return nil
}

// parseChromeToken extracts the major version from the first Chrome/<major>.
// token in s, returning 0 when s carries no parseable version.
func parseChromeToken(s string) int {
	idx := strings.Index(s, "Chrome/")
	if idx < 0 {
		return 0
	}
	major, _, _ := strings.Cut(s[idx+len("Chrome/"):], ".")
	n, err := strconv.Atoi(major)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// probeChromeMajor reads the launched Chromium's major version once at launch,
// degrading to fallbackChromeMajor when the round trip or parse fails so a
// launch never depends on the probe.
func probeChromeMajor(ctx context.Context, rb *rod.Browser) int {
	probeCtx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()
	res, err := proto.BrowserGetVersion{}.Call(rb.Context(probeCtx))
	if err != nil {
		return fallbackChromeMajor
	}
	if major := parseChromeToken(res.Product); major > 0 {
		return major
	}
	if major := parseChromeToken(res.UserAgent); major > 0 {
		return major
	}
	return fallbackChromeMajor
}
