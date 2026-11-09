// Package profiles stores named Bindplane connections (server + API key) in a
// local file so one tool can work across several organizations.
package profiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Profile struct {
	Name    string `json:"name"`
	Server  string `json:"server"`
	APIKey  string `json:"apiKey"`
	SkipTLS bool   `json:"skipTLSVerify,omitempty"`
}

type Store struct {
	path string
}

// DefaultPath is $BPLABELER_CONFIG, or <user config dir>/bplabeler/profiles.json.
func DefaultPath() (string, error) {
	if p := os.Getenv("BPLABELER_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bplabeler", "profiles.json"), nil
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) Path() string { return s.path }

type file struct {
	Default  string    `json:"default,omitempty"`
	Profiles []Profile `json:"profiles"`
}

func (s *Store) load() (file, error) {
	var f file
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("%s: %w", s.path, err)
	}
	sort.Slice(f.Profiles, func(i, j int) bool {
		return strings.ToLower(f.Profiles[i].Name) < strings.ToLower(f.Profiles[j].Name)
	})
	return f, nil
}

// List returns the profiles sorted by name. A missing file is an empty list.
func (s *Store) List() ([]Profile, error) {
	f, err := s.load()
	return f.Profiles, err
}

// Default returns the name of the default profile, or "" when none is set.
func (s *Store) Default() (string, error) {
	f, err := s.load()
	return f.Default, err
}

// SetDefault marks name as the profile used when none is chosen. An empty
// name clears the default.
func (s *Store) SetDefault(name string) error {
	f, err := s.load()
	if err != nil {
		return err
	}
	f.Default = ""
	if name != "" {
		found := false
		for _, p := range f.Profiles {
			if strings.EqualFold(p.Name, name) {
				f.Default, found = p.Name, true
			}
		}
		if !found {
			return fmt.Errorf("no profile named %q", name)
		}
	}
	return s.write(f)
}

func (s *Store) Get(name string) (Profile, error) {
	ps, err := s.List()
	if err != nil {
		return Profile{}, err
	}
	for _, p := range ps {
		if strings.EqualFold(p.Name, name) {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("no profile named %q", name)
}

func validate(p Profile) error {
	switch {
	case strings.TrimSpace(p.Name) == "":
		return errors.New("profile name is required")
	case strings.TrimSpace(p.Server) == "":
		return errors.New("server URL is required")
	case strings.TrimSpace(p.APIKey) == "":
		return errors.New("API key is required")
	}
	return nil
}

// Save adds a profile, or replaces the one called oldName (use "" to add).
// Renaming is allowed; names must stay unique, ignoring case.
func (s *Store) Save(oldName string, p Profile) error {
	p.Name, p.Server, p.APIKey = strings.TrimSpace(p.Name), strings.TrimSpace(p.Server), strings.TrimSpace(p.APIKey)
	if err := validate(p); err != nil {
		return err
	}
	f, err := s.load()
	if err != nil {
		return err
	}
	ps := f.Profiles
	var out []Profile
	replaced := false
	for _, e := range ps {
		switch {
		case oldName != "" && strings.EqualFold(e.Name, oldName):
			out = append(out, p)
			replaced = true
		case strings.EqualFold(e.Name, p.Name):
			return fmt.Errorf("a profile named %q already exists", e.Name)
		default:
			out = append(out, e)
		}
	}
	if oldName != "" && !replaced {
		return fmt.Errorf("no profile named %q", oldName)
	}
	if oldName == "" {
		out = append(out, p)
	}
	if oldName != "" && strings.EqualFold(f.Default, oldName) {
		f.Default = p.Name // keep the default through a rename
	}
	f.Profiles = out
	return s.write(f)
}

func (s *Store) Delete(name string) error {
	f, err := s.load()
	if err != nil {
		return err
	}
	var out []Profile
	for _, e := range f.Profiles {
		if !strings.EqualFold(e.Name, name) {
			out = append(out, e)
		}
	}
	if len(out) == len(f.Profiles) {
		return fmt.Errorf("no profile named %q", name)
	}
	if strings.EqualFold(f.Default, name) {
		f.Default = ""
	}
	f.Profiles = out
	return s.write(f)
}

// write replaces the file atomically; it holds API keys so it is 0600.
func (s *Store) write(f file) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".profiles-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// Mask shows only the tail of an API key.
func Mask(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return "****" + key[len(key)-4:]
}
