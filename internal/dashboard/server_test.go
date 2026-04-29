package dashboard

import (
	"html/template"
	"testing"
)

// TestTemplatesParse fails loud if the embedded templates contain a
// syntax error. NewServer also fails on bad templates, but exercising
// it here without a pool keeps the unit suite fast and dependency-free.
func TestTemplatesParse(t *testing.T) {
	tmpl, err := template.New("").Funcs(funcMap()).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	expected := []string{"layout", "index.html", "detail.html"}
	for _, name := range expected {
		if tmpl.Lookup(name) == nil {
			t.Errorf("template %q missing", name)
		}
	}
}

func TestNewServer_RejectsNilPool(t *testing.T) {
	if _, err := NewServer(nil); err == nil {
		t.Fatal("expected error for nil pool")
	}
}

func TestFuncMap_ShortUUIDHandlesShort(t *testing.T) {
	fm := funcMap()
	short := fm["shortUUID"].(func(string) string)
	if short("abc") != "abc" {
		t.Errorf("short input should pass through")
	}
	if got := short("abcdef0123456789"); got != "abcdef01" {
		t.Errorf("got %q want abcdef01", got)
	}
}

func TestFuncMap_DerefHandlesNil(t *testing.T) {
	fm := funcMap()
	deref := fm["deref"].(func(*string) string)
	if deref(nil) != "" {
		t.Errorf("nil should produce empty string")
	}
	s := "hi"
	if deref(&s) != "hi" {
		t.Errorf("non-nil should pass through")
	}
}
