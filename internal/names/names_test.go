package names

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	good := []string{"go", "com.example.app", "jetbrains-toolbox", "a_b", "App2", "x"}
	bad := []string{"", ".hidden", "-flag", "_x", "a/b", "../x", "a..b", "with space", "new\nline", "tab\t", "a$b", "ü"}
	for _, s := range good {
		if !Valid(s) {
			t.Errorf("Valid(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if Valid(s) {
			t.Errorf("Valid(%q) = true, want false", s)
		}
	}
}

func TestValidateMentionsLabel(t *testing.T) {
	err := Validate("App ID", "../etc")
	if err == nil || !strings.Contains(err.Error(), "App ID") {
		t.Fatalf("Validate error = %v, want it to mention the label", err)
	}
}
