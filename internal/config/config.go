// Package config loads and changes the ghw TOML configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// DefaultRefreshInterval is the watch refresh interval when the config does
// not set one.
const DefaultRefreshInterval = 5 * time.Minute

// Domain holds the settings for one GitHub host.
type Domain struct {
	// ExcludedUsernames lists comment authors to ignore, for example bots
	// that GitHub reports as users.
	ExcludedUsernames []string `toml:"excluded_usernames,omitempty"`
}

// Config is the typed view of the config file.
type Config struct {
	// RefreshInterval is a duration string such as "5m" or "90s".
	RefreshInterval string            `toml:"refresh_interval,omitempty"`
	Domains         map[string]Domain `toml:"domains,omitempty"`
}

// Interval returns the refresh interval for the watch commands.
func (c *Config) Interval() (time.Duration, error) {
	if c.RefreshInterval == "" {
		return DefaultRefreshInterval, nil
	}
	d, err := time.ParseDuration(c.RefreshInterval)
	if err != nil {
		return 0, fmt.Errorf("refresh_interval %q: %w", c.RefreshInterval, err)
	}
	if d < time.Second {
		return 0, fmt.Errorf("refresh_interval %q: must be at least 1s", c.RefreshInterval)
	}
	return d, nil
}

// Excluded returns the excluded usernames for a domain. An unknown domain
// has none.
func (c *Config) Excluded(domain string) []string {
	return c.Domains[domain].ExcludedUsernames
}

// Path returns the config file location: $GHW_CONFIG, or
// $XDG_CONFIG_HOME/ghw/config.toml, with the default
// ~/.config/ghw/config.toml.
func Path() (string, error) {
	if p := os.Getenv("GHW_CONFIG"); p != "" {
		return p, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "ghw", "config.toml"), nil
}

// Load reads the typed config. A missing file gives an empty config.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	cfg := &Config{Domains: map[string]Domain{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Domains == nil {
		cfg.Domains = map[string]Domain{}
	}
	return cfg, nil
}

// File is the raw config document, used for get/set/add/delete operations
// on dotted keys.
type File struct {
	Path string
	Raw  map[string]any
}

// LoadFile reads the raw config document. A missing file gives an empty one.
func LoadFile() (*File, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	f := &File{Path: path, Raw: map[string]any{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(data, &f.Raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return f, nil
}

// Save writes the document to disk and creates the parent directories.
func (f *File) Save() error {
	data, err := toml.Marshal(f.Raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(f.Path, data, 0o644)
}

// ArrayLeaves are the keys whose values are arrays. Every other key holds a
// single value.
var ArrayLeaves = []string{"excluded_usernames"}

var arrayLeaves = func() map[string]bool {
	m := make(map[string]bool, len(ArrayLeaves))
	for _, k := range ArrayLeaves {
		m[k] = true
	}
	return m
}()

func isArrayKey(parts []string) bool {
	return arrayLeaves[parts[len(parts)-1]]
}

// SplitKey splits a dotted key into segments. A segment in double quotes
// can hold dots, so domains."github.com".excluded_usernames addresses the
// github.com table.
func SplitKey(key string) ([]string, error) {
	if key == "" {
		return nil, errors.New("empty key")
	}
	var parts []string
	var cur strings.Builder
	quoted := false
	hasContent := false
	for i := 0; i < len(key); i++ {
		ch := key[i]
		switch {
		case ch == '"':
			quoted = !quoted
			hasContent = true
		case ch == '.' && !quoted:
			if !hasContent {
				return nil, fmt.Errorf("invalid key %q", key)
			}
			parts = append(parts, cur.String())
			cur.Reset()
			hasContent = false
		default:
			cur.WriteByte(ch)
			hasContent = true
		}
	}
	if quoted {
		return nil, fmt.Errorf("invalid key %q: unclosed quote", key)
	}
	if !hasContent {
		return nil, fmt.Errorf("invalid key %q", key)
	}
	parts = append(parts, cur.String())
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("invalid key %q", key)
		}
	}
	return parts, nil
}

// JoinKey builds a dotted key from segments. A segment that holds a dot or
// a space is put in double quotes.
func JoinKey(parts []string) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		if strings.ContainsAny(p, ". ") {
			out[i] = `"` + p + `"`
		} else {
			out[i] = p
		}
	}
	return strings.Join(out, ".")
}

// walk descends to the map that holds the final key segment. When create is
// true, it creates the missing intermediate tables.
func (f *File) walk(parts []string, create bool) (map[string]any, string, error) {
	cur := f.Raw
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p]
		if !ok {
			if !create {
				return nil, "", fmt.Errorf("key %q not found", JoinKey(parts))
			}
			m := map[string]any{}
			cur[p] = m
			cur = m
			continue
		}
		m, ok := next.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("%q is not a table", p)
		}
		cur = m
	}
	return cur, parts[len(parts)-1], nil
}

// Get returns the value at a dotted key.
func (f *File) Get(key string) (any, error) {
	parts, err := SplitKey(key)
	if err != nil {
		return nil, err
	}
	m, last, err := f.walk(parts, false)
	if err != nil {
		return nil, err
	}
	v, ok := m[last]
	if !ok {
		return nil, fmt.Errorf("key %q not found", key)
	}
	return v, nil
}

// Set assigns a value at a dotted key. An array key takes all the values
// and replaces the old array. A single-value key takes exactly one value.
func (f *File) Set(key string, values ...string) error {
	parts, err := SplitKey(key)
	if err != nil {
		return err
	}
	m, last, err := f.walk(parts, true)
	if err != nil {
		return err
	}
	if _, isTable := m[last].(map[string]any); isTable {
		return fmt.Errorf("%q is a table and cannot be set directly", key)
	}
	if isArrayKey(parts) {
		arr := make([]any, len(values))
		for i, v := range values {
			arr[i] = v
		}
		m[last] = arr
		return nil
	}
	if len(values) != 1 {
		return fmt.Errorf("%q holds a single value, got %d values", key, len(values))
	}
	m[last] = values[0]
	return nil
}

// Add appends a string value to the array at a dotted key. It creates the
// array when it does not exist.
func (f *File) Add(key, value string) error {
	parts, err := SplitKey(key)
	if err != nil {
		return err
	}
	if !isArrayKey(parts) {
		return fmt.Errorf("%q holds a single value; use `ghw config set %s <value>`", key, key)
	}
	m, last, err := f.walk(parts, true)
	if err != nil {
		return err
	}
	cur, ok := m[last]
	if !ok {
		m[last] = []any{value}
		return nil
	}
	arr, ok := cur.([]any)
	if !ok {
		return fmt.Errorf("%q is not an array; use `ghw config set %s <value>`", key, key)
	}
	m[last] = append(arr, value)
	return nil
}

// Delete removes a dotted key. When value is not nil, it removes the first
// matching element from the array at that key instead.
func (f *File) Delete(key string, value *string) error {
	parts, err := SplitKey(key)
	if err != nil {
		return err
	}
	m, last, err := f.walk(parts, false)
	if err != nil {
		return err
	}
	cur, ok := m[last]
	if !ok {
		return fmt.Errorf("key %q not found", key)
	}
	if value == nil {
		delete(m, last)
		return nil
	}
	arr, ok := cur.([]any)
	if !ok {
		return fmt.Errorf("%q is not an array", key)
	}
	for i, v := range arr {
		if s, ok := v.(string); ok && s == *value {
			m[last] = append(arr[:i], arr[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("value %q not found in %q", *value, key)
}

// Keys returns all dotted leaf keys in the document, sorted, for shell
// completion and for the list command.
func (f *File) Keys() []string {
	var out []string
	var visit func(prefix []string, m map[string]any)
	visit = func(prefix []string, m map[string]any) {
		for k, v := range m {
			key := append(append([]string{}, prefix...), k)
			if sub, ok := v.(map[string]any); ok {
				visit(key, sub)
				continue
			}
			out = append(out, JoinKey(key))
		}
	}
	visit(nil, f.Raw)
	sort.Strings(out)
	return out
}
