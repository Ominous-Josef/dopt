// Package names validates identifiers that dopt uses as path components
// (app IDs, command link names).
package names

import (
	"fmt"
	"regexp"
	"strings"
)

var pattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Rules describes what Valid accepts, for error messages.
const Rules = "allowed: letters, digits, '.', '_', '-'; must not start with a symbol or contain '..'"

// Valid reports whether s is safe to use as a single path component.
func Valid(s string) bool {
	return pattern.MatchString(s) && !strings.Contains(s, "..")
}

// Validate returns an error naming label if s is not Valid.
func Validate(label, s string) error {
	if !Valid(s) {
		return fmt.Errorf("invalid %s %q (%s)", label, s, Rules)
	}
	return nil
}
