package pageops

import (
	"fmt"
	"strings"

	"github.com/go-rod/rod/lib/input"
)

// namedKeys maps case-insensitive key names to go-rod input.Key constants.
var namedKeys = map[string]input.Key{
	"escape":       input.Escape,
	"esc":          input.Escape,
	"enter":        input.Enter,
	"return":       input.Enter,
	"tab":          input.Tab,
	"backspace":    input.Backspace,
	"delete":       input.Delete,
	"del":          input.Delete,
	"space":        input.Space,
	"home":         input.Home,
	"end":          input.End,
	"pageup":       input.PageUp,
	"pagedown":     input.PageDown,
	"insert":       input.Insert,
	"arrowleft":    input.ArrowLeft,
	"left":         input.ArrowLeft,
	"arrowright":   input.ArrowRight,
	"right":        input.ArrowRight,
	"arrowup":      input.ArrowUp,
	"up":           input.ArrowUp,
	"arrowdown":    input.ArrowDown,
	"down":         input.ArrowDown,
	"capslock":     input.CapsLock,
	"printscreen":  input.PrintScreen,
	"scrolllock":   input.ScrollLock,
	"pause":        input.Pause,
	"numlock":      input.NumLock,
	"f1":           input.F1,
	"f2":           input.F2,
	"f3":           input.F3,
	"f4":           input.F4,
	"f5":           input.F5,
	"f6":           input.F6,
	"f7":           input.F7,
	"f8":           input.F8,
	"f9":           input.F9,
	"f10":          input.F10,
	"f11":          input.F11,
	"f12":          input.F12,
	"minus":        input.Minus,
	"equal":        input.Equal,
	"bracketleft":  input.BracketLeft,
	"bracketright": input.BracketRight,
	"backslash":    input.Backslash,
	"semicolon":    input.Semicolon,
	"quote":        input.Quote,
	"comma":        input.Comma,
	"period":       input.Period,
	"slash":        input.Slash,
	"backquote":    input.Backquote,
}

// letterKeys maps A-Z letters to input.Key constants.
var letterKeys = map[rune]input.Key{
	'a': input.KeyA, 'b': input.KeyB, 'c': input.KeyC, 'd': input.KeyD,
	'e': input.KeyE, 'f': input.KeyF, 'g': input.KeyG, 'h': input.KeyH,
	'i': input.KeyI, 'j': input.KeyJ, 'k': input.KeyK, 'l': input.KeyL,
	'm': input.KeyM, 'n': input.KeyN, 'o': input.KeyO, 'p': input.KeyP,
	'q': input.KeyQ, 'r': input.KeyR, 's': input.KeyS, 't': input.KeyT,
	'u': input.KeyU, 'v': input.KeyV, 'w': input.KeyW, 'x': input.KeyX,
	'y': input.KeyY, 'z': input.KeyZ,
}

// digitKeys maps 0-9 digits to input.Key constants.
var digitKeys = map[rune]input.Key{
	'0': input.Digit0, '1': input.Digit1, '2': input.Digit2, '3': input.Digit3,
	'4': input.Digit4, '5': input.Digit5, '6': input.Digit6, '7': input.Digit7,
	'8': input.Digit8, '9': input.Digit9,
}

// symbolKeys maps common punctuation single characters to input.Key constants.
var symbolKeys = map[rune]input.Key{
	'-':  input.Minus,
	'=':  input.Equal,
	'[':  input.BracketLeft,
	']':  input.BracketRight,
	'\\': input.Backslash,
	';':  input.Semicolon,
	'\'': input.Quote,
	',':  input.Comma,
	'.':  input.Period,
	'/':  input.Slash,
	'`':  input.Backquote,
}

// parseModifier resolves a modifier name string to its go-rod input.Key representation.
func parseModifier(name string) (input.Key, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "control", "ctrl":
		return input.ControlLeft, true
	case "alt", "option":
		return input.AltLeft, true
	case "shift":
		return input.ShiftLeft, true
	case "meta", "cmd", "command", "super", "windows", "win":
		return input.MetaLeft, true
	default:
		return 0, false
	}
}

// resolveKey resolves a single key token (non-modifier) to an input.Key.
func resolveKey(token string) (input.Key, error) {
	t := strings.TrimSpace(token)
	if t == "" {
		return 0, fmt.Errorf("empty key token")
	}

	// Check named keys map (case-insensitive)
	if k, ok := namedKeys[strings.ToLower(t)]; ok {
		return k, nil
	}

	// Check single character
	runes := []rune(t)
	if len(runes) == 1 {
		r := runes[0]
		lowerR := r
		if r >= 'A' && r <= 'Z' {
			lowerR = r + ('a' - 'A')
		}
		if k, ok := letterKeys[lowerR]; ok {
			return k, nil
		}
		if k, ok := digitKeys[r]; ok {
			return k, nil
		}
		if k, ok := symbolKeys[r]; ok {
			return k, nil
		}
	}

	return 0, fmt.Errorf("unknown key name %q", token)
}

// parseKeyCombo parses a key string into modifier keys and a main key.
// Examples: "Enter", "Escape", "Control+a", "Ctrl+Shift+R", "Meta+c", "Shift+Tab", "Alt+F4".
func parseKeyCombo(keyStr string) (modifiers []input.Key, mainKey input.Key, err error) {
	k := strings.TrimSpace(keyStr)
	if k == "" {
		return nil, 0, fmt.Errorf("key specification cannot be empty")
	}

	// Split by '+'
	parts := strings.Split(k, "+")
	if len(parts) == 1 {
		// Single key without modifiers
		mk, err := resolveKey(parts[0])
		if err != nil {
			return nil, 0, err
		}
		return nil, mk, nil
	}

	// Multi-part combo: leading items are modifiers, last item is the main key
	for i := 0; i < len(parts)-1; i++ {
		mod, ok := parseModifier(parts[i])
		if !ok {
			return nil, 0, fmt.Errorf("unknown key modifier %q in combo %q", parts[i], keyStr)
		}
		modifiers = append(modifiers, mod)
	}

	mk, err := resolveKey(parts[len(parts)-1])
	if err != nil {
		return nil, 0, fmt.Errorf("invalid key in combo %q: %w", keyStr, err)
	}

	return modifiers, mk, nil
}
