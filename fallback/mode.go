package fallback

import "fmt"

const (
	ModeWriteThrough = "write_through"
	ModeReadFallback = "read_fallback"
	ModeBoth         = "both"
)

// NormalizeMode returns the effective fallback mode, defaulting to both.
func NormalizeMode(mode string) string {
	if mode == "" {
		return ModeBoth
	}
	return mode
}

// WriteEnabled reports whether fallback writes (write-through) are enabled.
func WriteEnabled(mode string) bool {
	switch NormalizeMode(mode) {
	case ModeReadFallback:
		return false
	case ModeWriteThrough, ModeBoth:
		return true
	default:
		return true
	}
}

// ValidateMode checks that mode is one of the supported values.
func ValidateMode(mode string) error {
	if mode == "" {
		return nil
	}
	switch mode {
	case ModeWriteThrough, ModeReadFallback, ModeBoth:
		return nil
	default:
		return fmt.Errorf("fallback.mode must be %q, %q, or %q", ModeWriteThrough, ModeReadFallback, ModeBoth)
	}
}
