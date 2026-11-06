package api

import (
	"strings"
	"testing"
)

func TestSearchPatternsEscapeWildcards(t *testing.T) {
	if got := likePattern(`50%_off\x`); got != `%50\%\_off\\x%` {
		t.Fatalf("like: %q", got)
	}
	if got := prefixPattern("AB_c"); got != `ab\_c%` {
		t.Fatalf("prefix: %q", got)
	}
}

func TestSnippetCentresTheMatch(t *testing.T) {
	body := strings.Repeat("filler ", 40) + "the WAN cable was replaced " + strings.Repeat("more ", 40)
	s := snippet(body, "wan cable", 60)
	if !strings.Contains(s, "WAN cable") || !strings.HasPrefix(s, "...") || !strings.HasSuffix(s, "...") {
		t.Fatalf("snippet %q", s)
	}
	if s := snippet("short note", "zzz", 60); s != "short note" {
		t.Fatalf("no match: %q", s)
	}
	if s := snippet("café été ok", "ok", 4); !strings.Contains(s, "ok") {
		t.Fatalf("unicode: %q", s)
	}
}

func TestSubjectOf(t *testing.T) {
	id := "11111111-2222-3333-4444-555555555555"
	if s, err := subjectOf("", "", strings.ToUpper(id), true); err != nil || s.kind != "appliance" || s.id != id || s.column() != "appliance_id" {
		t.Fatalf("appliance: %+v %v", s, err)
	}
	if s, _ := subjectOf(id, "", "", true); s.target() != "tenant" {
		t.Fatalf("a customer audits as tenant, got %q", s.target())
	}
	for name, c := range map[string][3]string{
		"none": {"", "", ""}, "two": {id, "", id}, "bad id": {"x'; drop", "", ""},
	} {
		if _, err := subjectOf(c[0], c[1], c[2], true); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := subjectOf("", id, "", false); err == nil {
		t.Error("a site was accepted where only customers and appliances can be tagged")
	}
}

func TestCleanNoteBody(t *testing.T) {
	if b, err := cleanNoteBody("  line one\r\nline two  "); err != nil || b != "line one\nline two" {
		t.Fatalf("%q %v", b, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", noteMaxLen+1), "a\x00b"} {
		if _, err := cleanNoteBody(bad); err == nil {
			t.Errorf("%q accepted", bad[:min(len(bad), 10)])
		}
	}
	if _, err := cleanNoteBody(strings.Repeat("é", noteMaxLen)); err != nil {
		t.Fatalf("4000 accented characters refused: %v", err)
	}
}

func TestCleanTag(t *testing.T) {
	n, c, err := cleanTag(ptr("  Pilot   site "), ptr("info"), true)
	if err != nil || n != "Pilot site" || c != "info" {
		t.Fatalf("%q %q %v", n, c, err)
	}
	if _, _, err := cleanTag(nil, nil, true); err == nil {
		t.Error("a nameless tag was accepted")
	}
	if _, _, err := cleanTag(ptr(strings.Repeat("x", tagMaxLen+1)), nil, true); err == nil {
		t.Error("a long tag was accepted")
	}
	if _, _, err := cleanTag(ptr("ok"), ptr("purple"), true); err == nil {
		t.Error("an unknown colour was accepted")
	}
}

func ptr(s string) *string { return &s }
