// Package manifest reads dopt recipes. Schema 2 is current; files without a
// "schema" field are read as schema 1 (the original Go recipes and dopt-bash manifests).
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"github.com/Ominous-Josef/dopt/internal/names"
)

// CurrentSchema is the newest manifest schema this build understands.
const CurrentSchema = 2

// FlexBool accepts JSON true/false and the strings "true"/"false" (dopt-bash writes strings).
type FlexBool bool

func (b *FlexBool) UnmarshalJSON(data []byte) error {
	switch strings.ToLower(strings.Trim(string(bytes.TrimSpace(data)), `"`)) {
	case "true", "yes", "1":
		*b = true
	case "false", "no", "0", "", "null":
		*b = false
	default:
		return fmt.Errorf("expected true or false, got %s", data)
	}
	return nil
}

// Source describes how to find the download URL dynamically.
type Source struct {
	Type              string `json:"type"`                          // "api" or "github"
	Endpoint          string `json:"endpoint,omitempty"`            // api: JSON endpoint
	JqQuery           string `json:"jq_query,omitempty"`            // api: query with $arch, $goarch, $os
	Repository        string `json:"repository,omitempty"`          // github: owner/name
	AssetPattern      string `json:"asset_pattern,omitempty"`       // github: any architecture
	AssetPatternX64   string `json:"asset_pattern_x64,omitempty"`   // github: x86_64 only
	AssetPatternArm64 string `json:"asset_pattern_arm64,omitempty"` // github: aarch64 only
}

// Manifest is one application recipe.
type Manifest struct {
	Schema          int      `json:"schema,omitempty"`
	AppID           string   `json:"app_id"`
	Name            string   `json:"name,omitempty"`
	Comment         string   `json:"comment,omitempty"`
	BinaryPattern   string   `json:"binary_pattern,omitempty"`
	BinaryPath      string   `json:"binary_path,omitempty"`
	IconPath        string   `json:"icon_path,omitempty"`
	CliOnly         FlexBool `json:"cli_only,omitempty"`
	SymlinkAs       string   `json:"symlink_as,omitempty"`
	Categories      string   `json:"categories,omitempty"`
	ExecFlags       string   `json:"exec_flags,omitempty"`
	DefaultURLX64   string   `json:"default_url_x64,omitempty"`
	DefaultURLArm64 string   `json:"default_url_arm64,omitempty"`
	Source          *Source  `json:"source,omitempty"`

	// Schema 1 only. Ignored: apps always install into <opt dir>/<app_id>.
	DefaultInstallDir string `json:"default_install_dir,omitempty"`
}

var knownFields = fieldSet(Manifest{})
var knownSourceFields = fieldSet(Source{})

// Load reads and checks the manifest at path. Warnings are non-fatal problems worth showing.
func Load(path string) (Manifest, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, nil, err
	}
	return Parse(data)
}

// Parse decodes a manifest, fills defaults and validates it.
func Parse(data []byte) (Manifest, []string, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, nil, fmt.Errorf("the manifest isn't valid JSON: %w", err)
	}
	if m.Schema > CurrentSchema {
		return Manifest{}, nil, fmt.Errorf("the manifest uses schema %d, but this dopt only understands up to %d. Update dopt", m.Schema, CurrentSchema)
	}
	if m.Schema < 0 {
		return Manifest{}, nil, fmt.Errorf("invalid schema %d", m.Schema)
	}

	warnings := unknownFields(data)
	if m.DefaultInstallDir != "" {
		warnings = append(warnings, "'default_install_dir' is ignored: apps always install into <opt dir>/<app_id>")
	}

	m.ApplyDefaults()
	if err := m.Validate(); err != nil {
		return Manifest{}, nil, err
	}
	return m, warnings, nil
}

// ApplyDefaults fills optional fields the way dopt-bash does.
func (m *Manifest) ApplyDefaults() {
	if m.Name == "" {
		m.Name = m.AppID
	}
	if m.SymlinkAs == "" {
		m.SymlinkAs = m.AppID
	}
	if m.Categories == "" {
		m.Categories = "Utility;"
	}
}

// Validate checks the required fields and that names are safe path components.
func (m Manifest) Validate() error {
	if m.AppID == "" {
		return errors.New("the manifest is missing the required field 'app_id'")
	}
	if err := names.Validate("app_id", m.AppID); err != nil {
		return err
	}
	if m.SymlinkAs != "" {
		if err := names.Validate("symlink_as", m.SymlinkAs); err != nil {
			return err
		}
	}
	if m.BinaryPath == "" && m.BinaryPattern == "" {
		return errors.New("the manifest must set 'binary_path' or 'binary_pattern'")
	}
	if m.Source != nil {
		switch m.Source.Type {
		case "api":
			if m.Source.Endpoint == "" || m.Source.JqQuery == "" {
				return errors.New("source type 'api' needs 'endpoint' and 'jq_query'")
			}
		case "github":
			if m.Source.Repository == "" {
				return errors.New("source type 'github' needs 'repository'")
			}
		default:
			return fmt.Errorf("unknown source type %q (expected 'api' or 'github')", m.Source.Type)
		}
	}
	return nil
}

// Arch maps a Go architecture to dopt's name for it ("x64" or "arm64").
func Arch(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "x64", nil
	case "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("this processor architecture (%s) isn't supported", goarch)
}

// HostArch is Arch for the running machine.
func HostArch() (string, error) { return Arch(runtime.GOARCH) }

// DefaultURL is the fixed download URL for arch, or "" if the manifest has none.
func (m Manifest) DefaultURL(arch string) string {
	if arch == "arm64" {
		return m.DefaultURLArm64
	}
	return m.DefaultURLX64
}

// HasDownload reports whether -d can find a URL for arch without -u.
func (m Manifest) HasDownload(arch string) bool {
	return m.Source != nil || m.DefaultURL(arch) != ""
}

func unknownFields(data []byte) []string {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	var warnings []string
	for _, k := range sortedKeys(raw) {
		if !knownFields[k] {
			warnings = append(warnings, fmt.Sprintf("unknown field '%s' is ignored", k))
		}
	}
	var src map[string]json.RawMessage
	if s, ok := raw["source"]; ok && json.Unmarshal(s, &src) == nil {
		for _, k := range sortedKeys(src) {
			if !knownSourceFields[k] {
				warnings = append(warnings, fmt.Sprintf("unknown field 'source.%s' is ignored", k))
			}
		}
	}
	return warnings
}

// fieldSet lists the JSON names of a struct's fields.
func fieldSet(v any) map[string]bool {
	set := map[string]bool{}
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			set[name] = true
		}
	}
	return set
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
