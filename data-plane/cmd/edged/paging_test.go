package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestParsePage_DefaultsAndBounds(t *testing.T) {
	cases := []struct {
		query     string
		legacyMax int
		page      int
		size      int
		bad       bool
	}{
		{"", 0, 1, 50, false},
		{"page=3", 0, 3, 50, false},
		{"page=2&page_size=1", 0, 2, 1, false},
		{"page_size=200", 0, 1, 200, false},
		{"page=0", 0, 0, 0, true},
		{"page=-1", 0, 0, 0, true},
		{"page=x", 0, 0, 0, true},
		{"page_size=0", 0, 0, 0, true},
		{"page_size=201", 0, 0, 0, true},
		{"page_size=ten", 0, 0, 0, true},
		// An older client's ?limit= still sizes the page, capped at what the endpoint always allowed.
		{"limit=500", 500, 1, 500, false},
		{"limit=900", 500, 1, 500, false},
		{"limit=abc", 500, 1, 50, false},
		{"limit=0", 500, 1, 50, false},
		// page_size wins over limit; an endpoint with no legacy limit ignores it.
		{"limit=300&page_size=20", 500, 1, 20, false},
		{"limit=300", 0, 1, 50, false},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.query)
		p, err := parsePage(q, c.legacyMax)
		if c.bad {
			if err == nil {
				t.Errorf("%q: accepted, want a refusal", c.query)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.query, err)
			continue
		}
		if p.Page != c.page || p.Size != c.size {
			t.Errorf("%q: page %d size %d, want %d/%d", c.query, p.Page, p.Size, c.page, c.size)
		}
	}
}

func TestPageReq_OffsetAndFetch(t *testing.T) {
	p := pageReq{Page: 3, Size: 25}
	if p.Offset() != 50 || p.Fetch() != 26 {
		t.Fatalf("offset %d fetch %d, want 50/26", p.Offset(), p.Fetch())
	}
}

func TestReadPage_AnswersBadRequest(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/x?page_size=1000", nil)
	if _, ok := readPage(w, r, 0); ok {
		t.Fatal("an out-of-bounds page_size was accepted")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}

func TestReadSearch_BoundedAndTrimmed(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("X-Test-Search", "  412  ")
	if v, ok := readSearch(w, r, "X-Test-Search"); !ok || v != "412" {
		t.Fatalf("got %q %v", v, ok)
	}
	// The Admin Console percent-encodes the text, so a name outside ISO-8859-1 can travel in a header.
	r.Header.Set("X-Test-Search", "%D8%B3%D8%A7%D9%85%D9%8A%20")
	if v, ok := readSearch(w, r, "X-Test-Search"); !ok || v != "سامي" {
		t.Fatalf("encoded search: got %q %v", v, ok)
	}
	// Text that is not valid percent-encoding is taken as typed.
	r.Header.Set("X-Test-Search", "50%")
	if v, ok := readSearch(w, r, "X-Test-Search"); !ok || v != "50%" {
		t.Fatalf("plain search: got %q %v", v, ok)
	}
	long := make([]rune, searchMaxLen+1)
	for i := range long {
		long[i] = 'a'
	}
	r.Header.Set("X-Test-Search", string(long))
	if _, ok := readSearch(w, r, "X-Test-Search"); ok || w.Code != http.StatusBadRequest {
		t.Fatalf("an over-long search was accepted (status %d)", w.Code)
	}
}

func TestLikePattern(t *testing.T) {
	if likePattern("") != nil {
		t.Error("no search must be SQL NULL")
	}
	if got := likePattern(`50%_off\`); got != `%50\%\_off\\%` {
		t.Errorf("got %v", got)
	}
}

func TestTrimAndSlicePage(t *testing.T) {
	p := pageReq{Page: 1, Size: 2}
	rows, more := trimPage([]int{1, 2, 3}, p)
	if len(rows) != 2 || !more {
		t.Fatalf("trim: %v %v", rows, more)
	}
	rows, more = trimPage([]int{1, 2}, p)
	if len(rows) != 2 || more {
		t.Fatalf("trim exact: %v %v", rows, more)
	}
	if rows, _ := trimPage[int](nil, p); rows == nil {
		t.Fatal("an empty page must be [] not null")
	}

	all := []int{1, 2, 3, 4, 5}
	seen := []int{}
	for pg := 1; ; pg++ {
		page, more := slicePage(all, pageReq{Page: pg, Size: 2})
		seen = append(seen, page...)
		if !more {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("slicing lost or repeated rows: %v", seen)
	}
	if page, more := slicePage(all, pageReq{Page: 9, Size: 2}); len(page) != 0 || more || page == nil {
		t.Fatalf("past the end: %v %v", page, more)
	}
}

func TestNewPagedList_KeepsTheListShape(t *testing.T) {
	raw, _ := json.Marshal(newPagedList[int](nil, true, pageReq{Page: 2, Size: 10}, nil))
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if d, ok := m["data"].([]any); !ok || len(d) != 0 {
		t.Errorf("data: %v", m["data"])
	}
	if meta, _ := m["meta"].(map[string]any); meta["has_more"] != true {
		t.Errorf("meta: %v", m["meta"])
	}
	if m["page"] != float64(2) || m["page_size"] != float64(10) {
		t.Errorf("page fields: %v", m)
	}
	if _, ok := m["total"]; ok {
		t.Error("an unknown total must be omitted, not 0")
	}
}

func TestPageLeases_SearchesOrdersAndPagesTheWholeList(t *testing.T) {
	body := []byte(`{"leases":[
		{"ip-address":"10.0.0.10","hw-address":"aa:00:00:00:00:10","hostname":"tv","subnet-id":1},
		{"ip-address":"10.0.0.2","hw-address":"aa:00:00:00:00:02","hostname":"Phone-A"},
		{"ip-address":"10.0.0.9","hw-address":"aa:00:00:00:00:09"},
		{"ip-address":"10.0.0.3","hw-address":"bb:00:00:00:00:03","hostname":"laptop"}]}`)
	p1, err := pageLeases(body, "", pageReq{Page: 1, Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	if p1.Total != 4 || !p1.Meta.HasMore || len(p1.Leases) != 3 {
		t.Fatalf("page 1: total %d more %v n %d", p1.Total, p1.Meta.HasMore, len(p1.Leases))
	}
	// Ordered by address numerically, not as text: 10.0.0.2 before 10.0.0.10.
	var first map[string]any
	_ = json.Unmarshal(p1.Leases[0], &first)
	if first["ip-address"] != "10.0.0.2" {
		t.Errorf("first lease %v", first["ip-address"])
	}
	p2, _ := pageLeases(body, "", pageReq{Page: 2, Size: 3})
	var last map[string]any
	_ = json.Unmarshal(p2.Leases[0], &last)
	if p2.Meta.HasMore || len(p2.Leases) != 1 || last["ip-address"] != "10.0.0.10" {
		t.Fatalf("page 2: %v %v", p2.Meta.HasMore, last)
	}
	// A field edged does not read still reaches the client.
	if last["subnet-id"] != float64(1) {
		t.Error("an unread lease field was dropped")
	}
	s, _ := pageLeases(body, "PHONE", pageReq{Page: 1, Size: 50})
	if s.Total != 1 {
		t.Errorf("hostname search: %d", s.Total)
	}
	s, _ = pageLeases(body, "bb:00", pageReq{Page: 1, Size: 50})
	if s.Total != 1 {
		t.Errorf("mac search: %d", s.Total)
	}
	if _, err := pageLeases([]byte("not json"), "", pageReq{Page: 1, Size: 1}); err == nil {
		t.Error("an unreadable lease list was accepted")
	}
}
