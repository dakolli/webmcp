package browsers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/devices"
	"github.com/go-rod/rod/lib/proto"
)

// PageID uniquely identifies a live page (a.k.a. session / browser tab) owned
// by a Browser. It is minted by Browser.NewPage and referenced from every
// page_fetch / page_act / session_close call, e.g. "page-1".
type PageID string

// Page wraps a live *rod.Page (a browser tab) and its own mutex, because rod
// Pages are NOT thread-safe: all access to the underlying page must be
// serialized with the per-page mutex. Tools Lock before touching the page and
// Unlock after the page call completes; the Browser does the same for its own
// lifecycle operations (ClosePage, Close).
type Page struct {
	// ID uniquely identifies this page within its Browser.
	ID PageID
	// URL is the URL the page was created with (set by NewPage).
	URL string
	// Rod is the live go-rod page handle for this tab. Only valid while the
	// page is open.
	Rod *rod.Page
	// lastAccess is the last time this page was used: set at creation (NewPage)
	// and refreshed by touch on every successful Browser.Page lookup. The
	// manager's background sweeper closes pages idle for longer than its TTL.
	lastAccess time.Time
	// docMarkerInstalled reports whether the handle-scope marker script has
	// been installed on this page (see CaptureSnapshot). Guarded by mu.
	docMarkerInstalled bool
	// observer holds the page's network/console capture rings. It is assigned
	// once in the pre-registration window and never replaced, so read methods
	// may use it without taking mu. Engine-internal: surfaces never touch it.
	observer *pageObserver
	// obsCancel disarms the capture subscription. It is set in the same
	// pre-registration window and cleared by stopObservation. Guarded by mu.
	obsCancel context.CancelFunc

	// mu serializes access to the underlying rod.Page (rod is not thread-safe).
	mu sync.Mutex
}

// touch records that the page was just used, resetting its idle clock so the
// manager's sweeper does not close it. It is safe to call while holding the
// Browser mutex: the browser lock is always taken before the page lock.
func (p *Page) touch() {
	p.mu.Lock()
	p.lastAccess = time.Now()
	p.mu.Unlock()
}

// Lock serializes access to the page's underlying rod page. Pair with Unlock.
func (p *Page) Lock() {
	p.mu.Lock()
}

// Unlock releases the per-page lock taken by Lock.
func (p *Page) Unlock() {
	p.mu.Unlock()
}

// Close closes the underlying rod tab, releasing the browser tab. The page
// must not be used afterwards. It is safe to call on an already-closed page
// (nil Rod handle) and is best-effort by design: teardown is attempted even if
// ctx has expired so resources are not leaked. The caller's bounded timeout
// (sweep, shutdown) is enforced at the rod boundary: a wedged CDP connection
// surfaces a ctx-bound error (e.g. context.DeadlineExceeded) instead of
// blocking the caller past its deadline.
func (p *Page) Close(ctx context.Context) error {
	p.Lock()
	defer p.Unlock()
	p.stopObservation()
	if p.Rod == nil {
		return nil
	}
	// rod's Close has no ctx-aware form, but Context(ctx) returns a clone whose
	// chained operations honor ctx. Error-returning form; no Must*.
	return p.Rod.Context(ctx).Close()
}

// PageInfo is a read-only snapshot of a page's metadata, safe to copy and
// marshal. It deliberately excludes the live *rod.Page handle.
type PageInfo struct {
	// ID uniquely identifies the page.
	ID PageID `json:"id"`
	// URL is the URL the page was created with.
	URL string `json:"url"`
}

// Viewport is a page viewport size in CSS pixels. It is the engine's MCP-free
// carrier for a per-page MANUAL viewport override (an explicit user option,
// never a default): the MCP/CLI/IPC layers translate their JSON into this
// struct and the engine applies it via Page.SetViewport
// (Emulation.setDeviceMetricsOverride) before navigating. The override sets
// both the emulated viewport and the matching screen metrics (screen.width
// tracks window.innerWidth). A nil *Viewport means "no override" — the page
// launches PLAIN with rod's own defaults (device presets via Page.Emulate are
// the primary, opt-in emulation mechanism; see Emulate).
type Viewport struct {
	// Width is the viewport width in CSS pixels.
	Width int `json:"width"`
	// Height is the viewport height in CSS pixels.
	Height int `json:"height"`
}

// devicePreset is one entry of the go-rod device-preset lookup table: the
// preset's Go identifier (e.g. "IPhoneX"), its human Title (e.g. "iPhone X"),
// and the preset value itself. The table is a hand-typed mirror VERIFIED
// against the vendored rod v0.116.2 lib/devices/list.go (lines 5-690, the
// single `var (` block) — the exported vars there are the source of truth,
// never invent presets (rod v0.116.2 has no IPhone12Pro). A go-rod upgrade
// must diff-review this table against the new list.go.
type devicePreset struct {
	// name is the preset's exported Go identifier in lib/devices/list.go.
	name string
	// title is the preset's Device.Title (e.g. "iPhone X").
	title string
	// dev is the preset value to hand to Page.Emulate.
	dev devices.Device
}

// devicePresets is the full vendor preset list — a hand-typed mirror of every
// exported Device variable in the vendored rod v0.116.2 lib/devices/list.go
// (lines 5-690, the single `var (` block), with its Title, in declaration
// order. The vendor file is the source of truth (never hand-typed, per
// AGENTS.md); a go-rod upgrade trip-wires a diff-review of this table. Both
// spellings resolve case-insensitively, so "iphonex" and "iPhone X" hit the
// same preset.
var devicePresets = []devicePreset{
	{"IPhone4", "iPhone 4", devices.IPhone4},
	{"IPhone5orSE", "iPhone 5/SE", devices.IPhone5orSE},
	{"IPhone6or7or8", "iPhone 6/7/8", devices.IPhone6or7or8},
	{"IPhone6or7or8Plus", "iPhone 6/7/8 Plus", devices.IPhone6or7or8Plus},
	{"IPhoneX", "iPhone X", devices.IPhoneX},
	{"BlackBerryZ30", "BlackBerry Z30", devices.BlackBerryZ30},
	{"Nexus4", "Nexus 4", devices.Nexus4},
	{"Nexus5", "Nexus 5", devices.Nexus5},
	{"Nexus5X", "Nexus 5X", devices.Nexus5X},
	{"Nexus6", "Nexus 6", devices.Nexus6},
	{"Nexus6P", "Nexus 6P", devices.Nexus6P},
	{"Pixel2", "Pixel 2", devices.Pixel2},
	{"Pixel2XL", "Pixel 2 XL", devices.Pixel2XL},
	{"LGOptimusL70", "LG Optimus L70", devices.LGOptimusL70},
	{"NokiaN9", "Nokia N9", devices.NokiaN9},
	{"NokiaLumia520", "Nokia Lumia 520", devices.NokiaLumia520},
	{"MicrosoftLumia550", "Microsoft Lumia 550", devices.MicrosoftLumia550},
	{"MicrosoftLumia950", "Microsoft Lumia 950", devices.MicrosoftLumia950},
	{"GalaxySIII", "Galaxy S III", devices.GalaxySIII},
	{"GalaxyS5", "Galaxy S5", devices.GalaxyS5},
	{"JioPhone2", "JioPhone 2", devices.JioPhone2},
	{"KindleFireHDX", "Kindle Fire HDX", devices.KindleFireHDX},
	{"IPadMini", "iPad Mini", devices.IPadMini},
	{"IPad", "iPad", devices.IPad},
	{"IPadPro", "iPad Pro", devices.IPadPro},
	{"BlackberryPlayBook", "BlackBerry PlayBook", devices.BlackberryPlayBook},
	{"Nexus10", "Nexus 10", devices.Nexus10},
	{"Nexus7", "Nexus 7", devices.Nexus7},
	{"GalaxyNote3", "Galaxy Note 3", devices.GalaxyNote3},
	{"GalaxyNoteII", "Galaxy Note II", devices.GalaxyNoteII},
	{"LaptopWithTouch", "Laptop with touch", devices.LaptopWithTouch.Landscape()},
	{"LaptopWithHiDPIScreen", "Laptop with HiDPI screen", devices.LaptopWithHiDPIScreen.Landscape()},
	{"LaptopWithMDPIScreen", "Laptop with MDPI screen", devices.LaptopWithMDPIScreen.Landscape()},
	{"MotoG4", "Moto G4", devices.MotoG4},
	{"SurfaceDuo", "Surface Duo", devices.SurfaceDuo},
	{"GalaxyFold", "Galaxy Fold", devices.GalaxyFold},
}

// deviceByName resolves a device preset name to its devices.Device value. The
// lookup is case-insensitive on BOTH the exported Go identifier ("iphonex")
// and the preset's Device.Title ("iPhone X"); the special name "clear" (any
// case) resolves to devices.Clear, which resets all overrides. The linear scan
// is fine for the 35-entry vendor table and keeps the table self-documenting.
func deviceByName(name string) (devices.Device, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "clear" {
		return devices.Clear, true
	}
	for _, p := range devicePresets {
		if n == strings.ToLower(p.name) || n == strings.ToLower(p.title) {
			return p.dev, true
		}
	}
	return devices.Device{}, false
}

// ValidateDeviceName reports whether name resolves to a go-rod device preset
// (its Go identifier or Title, case-insensitive) or "clear". It is the
// engine's pure, Chromium-free pre-flight check: the MCP/CLI/IPC layers call
// it before any browser work so an unknown device fails fast with the exact
// pinned error, mirroring the WindowState validation in Browser.Focus. The
// error is exact and pinned: unknown device %q (see go-rod lib/devices
// presets, e.g. IPhoneX, IPadPro, LaptopWithHiDPIScreen).
func ValidateDeviceName(name string) error {
	if _, ok := deviceByName(name); !ok {
		return fmt.Errorf("unknown device %q (see go-rod lib/devices presets, e.g. IPhoneX, IPadPro, LaptopWithHiDPIScreen)", name)
	}
	return nil
}

// Emulate applies a go-rod device preset (lib/devices, v0.116.2) to the live
// page: the primary, opt-in screen-size emulation mechanism (user decision
// 2026-08-14 — pages launch PLAIN by default; manual width/height via
// SetViewport is only an explicit option). deviceName resolves
// case-insensitively against the preset's Go identifier AND its Device.Title
// (e.g. "iphonex" or "iPhone X"); the special name "clear" applies
// devices.Clear, which resets the metrics/touch/UA overrides. The apply is the
// error-returning, ctx-bound rod form Page.Emulate (no Must*) under the page
// lock, with the nil-Rod guard; a name that matches no preset returns the
// exact pinned unknown-device error.
func (p *Page) Emulate(ctx context.Context, deviceName string) error {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return fmt.Errorf("page session is closed")
	}
	dev, ok := deviceByName(deviceName)
	if !ok {
		return fmt.Errorf("unknown device %q (see go-rod lib/devices presets, e.g. IPhoneX, IPadPro, LaptopWithHiDPIScreen)", deviceName)
	}
	if err := p.Rod.Context(ctx).Emulate(dev); err != nil {
		return fmt.Errorf("emulate device %q: %w", deviceName, err)
	}
	return nil
}

// SetViewport applies a MANUAL width × height viewport override to the live
// page via Emulation.setDeviceMetricsOverride with a device scale factor of 1
// (desktop, no mobile emulation) and matching screen metrics (screen.width
// tracks window.innerWidth). It is the engine's single seam for the manual
// override — an explicit user option, never a default: pages launch PLAIN and
// the primary emulation mechanism is the opt-in device preset via Page.Emulate
// (user decision 2026-08-14). The apply is the error-returning, ctx-bound rod
// form Page.SetViewport (no Must*) under the page lock, with the nil-Rod
// guard. vp must be non-nil; the MCP/CLI layers enforce their own pinned
// "requires a viewport" errors before calling.
func (p *Page) SetViewport(ctx context.Context, vp *Viewport) error {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return fmt.Errorf("page session is closed")
	}
	sw, sh := vp.Width, vp.Height // proto ScreenWidth/ScreenHeight are *int
	if err := p.Rod.Context(ctx).SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:             vp.Width,
		Height:            vp.Height,
		DeviceScaleFactor: 1,
		Mobile:            false,
		ScreenWidth:       &sw,
		ScreenHeight:      &sh,
	}); err != nil {
		return fmt.Errorf("browsers: set viewport: %w", err)
	}
	return nil
}
