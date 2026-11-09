package profiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreCRUD(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "sub", "p.json"))
	if ps, err := s.List(); err != nil || len(ps) != 0 {
		t.Fatalf("empty: %v %v", ps, err)
	}
	if err := s.Save("", Profile{Name: "prod", Server: "https://a", APIKey: "k1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("", Profile{Name: "PROD", Server: "https://b", APIKey: "k"}); err == nil {
		t.Fatal("duplicate name must fail")
	}
	if err := s.Save("", Profile{Name: "x", Server: "", APIKey: "k"}); err == nil {
		t.Fatal("missing server must fail")
	}
	if err := s.Save("prod", Profile{Name: "prod-eu", Server: "https://a", APIKey: "k2", SkipTLS: true}); err != nil {
		t.Fatal(err)
	}
	p, err := s.Get("PROD-EU")
	if err != nil || p.APIKey != "k2" || !p.SkipTLS {
		t.Fatalf("get: %+v %v", p, err)
	}
	if _, err := s.Get("prod"); err == nil {
		t.Fatal("old name should be gone after rename")
	}
	st, _ := os.Stat(s.Path())
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	if err := s.Delete("prod-eu"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("prod-eu"); err == nil {
		t.Fatal("delete of missing must fail")
	}
}

func TestMask(t *testing.T) {
	if Mask("abcdefghijkl") != "****ijkl" || Mask("abc") != "***" {
		t.Fatal(Mask("abcdefghijkl"), Mask("abc"))
	}
}

func TestDefault(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "p.json"))
	s.Save("", Profile{Name: "a", Server: "s", APIKey: "k"})
	s.Save("", Profile{Name: "b", Server: "s", APIKey: "k"})
	if d, _ := s.Default(); d != "" {
		t.Fatalf("default=%q", d)
	}
	if err := s.SetDefault("B"); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Default(); d != "b" {
		t.Fatalf("default=%q", d)
	}
	if err := s.SetDefault("nope"); err == nil {
		t.Fatal("unknown default must fail")
	}
	// survives other writes and renames
	s.Save("", Profile{Name: "c", Server: "s", APIKey: "k"})
	s.Save("b", Profile{Name: "b2", Server: "s", APIKey: "k"})
	if d, _ := s.Default(); d != "b2" {
		t.Fatalf("after rename default=%q", d)
	}
	s.Delete("b2")
	if d, _ := s.Default(); d != "" {
		t.Fatalf("after delete default=%q", d)
	}
	s.SetDefault("a")
	s.SetDefault("")
	if d, _ := s.Default(); d != "" {
		t.Fatalf("cleared default=%q", d)
	}
}
