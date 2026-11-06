package api

import (
	"context"
	"net/http"
	"strings"
)

const (
	searchMinLen  = 2
	searchMaxLen  = 100
	searchPerKind = 8
)

type SearchHit struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Href     string `json:"href"`
	Matched  string `json:"matched,omitempty"`
}

type SearchResult struct {
	Query      string      `json:"query"`
	Customers  []SearchHit `json:"customers"`
	Sites      []SearchHit `json:"sites"`
	Appliances []SearchHit `json:"appliances"`
	Licenses   []SearchHit `json:"licenses"`
	Notes      []SearchHit `json:"notes"`
}

func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

func prefixPattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(strings.ToLower(q)) + "%"
}

func snippet(body, q string, width int) string {
	b := strings.Join(strings.Fields(body), " ")
	i := strings.Index(strings.ToLower(b), strings.ToLower(q))
	if i < 0 {
		i = 0
	}
	start := max(i-width/3, 0)
	r := []rune(b)
	rs := len([]rune(b[:start]))
	end := min(rs+width, len(r))
	out := string(r[rs:end])
	if rs > 0 {
		out = "..." + out
	}
	if end < len(r) {
		out += "..."
	}
	return out
}

func (b *Base) search(w http.ResponseWriter, r *http.Request) {
	q := strings.Join(strings.Fields(r.URL.Query().Get("q")), " ")
	if n := len([]rune(q)); n < searchMinLen || n > searchMaxLen {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "Type between 2 and 100 characters to search.")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	res, err := b.runSearch(ctx, q)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The search could not run. Try again.")
		return
	}
	WriteJSON(w, http.StatusOK, res)
}

func (b *Base) runSearch(ctx context.Context, q string) (SearchResult, error) {
	like, prefix := likePattern(q), prefixPattern(q)
	res := SearchResult{Query: q, Customers: []SearchHit{}, Sites: []SearchHit{}, Appliances: []SearchHit{},
		Licenses: []SearchHit{}, Notes: []SearchHit{}}
	collect := func(dst *[]SearchHit, sql string, args ...any) error {
		rows, err := b.DB.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h SearchHit
			if err := rows.Scan(&h.ID, &h.Title, &h.Subtitle, &h.Href, &h.Matched); err != nil {
				return err
			}
			*dst = append(*dst, h)
		}
		return rows.Err()
	}
	if err := collect(&res.Customers, `
        SELECT t.id::text, t.name,
               CASE WHEN t.status = 'archived' THEN 'Archived customer' ELSE 'Customer' END,
               '/customers/' || t.id::text,
               COALESCE((SELECT 'tag ' || g.name FROM central_tag_links l JOIN central_tags g ON g.id = l.tag_id
                          WHERE l.customer_id = t.id AND g.name ILIKE $1 ESCAPE '\' LIMIT 1), '')
          FROM tenants t
         WHERE t.name ILIKE $1 ESCAPE '\' OR t.slug ILIKE $1 ESCAPE '\'
            OR EXISTS (SELECT 1 FROM central_tag_links l JOIN central_tags g ON g.id = l.tag_id
                        WHERE l.customer_id = t.id AND g.name ILIKE $1 ESCAPE '\')
         ORDER BY (t.status = 'archived'), (lower(t.name) LIKE $2 ESCAPE '\') DESC, lower(t.name)
         LIMIT $3`, like, prefix, searchPerKind); err != nil {
		return res, err
	}
	if err := collect(&res.Sites, `
        SELECT s.id::text, s.name, 'Site of ' || t.name, '/customers/' || t.id::text || '?tab=sites', ''
          FROM sites s JOIN tenants t ON t.id = s.tenant_id
         WHERE s.name ILIKE $1 ESCAPE '\' OR COALESCE(s.code,'') ILIKE $1 ESCAPE '\'
         ORDER BY (s.status = 'archived'), (lower(s.name) LIKE $2 ESCAPE '\') DESC, lower(s.name)
         LIMIT $3`, like, prefix, searchPerKind); err != nil {
		return res, err
	}
	if err := collect(&res.Appliances, `
        SELECT a.id::text, a.serial,
               concat_ws(' . ', NULLIF(a.hostname,''), NULLIF(t.name,''), NULLIF(s.name,'')),
               '/appliances/' || a.id::text,
               CASE WHEN a.serial ILIKE $1 ESCAPE '\' OR COALESCE(a.hostname,'') ILIKE $1 ESCAPE '\' THEN ''
                    WHEN COALESCE(a.last_public_ip,'') ILIKE $1 ESCAPE '\' THEN 'public address ' || a.last_public_ip
                    WHEN a.id::text LIKE $2 ESCAPE '\' THEN 'appliance id'
                    ELSE COALESCE((SELECT 'tag ' || g.name FROM central_tag_links l JOIN central_tags g ON g.id = l.tag_id
                                    WHERE l.appliance_id = a.id AND g.name ILIKE $1 ESCAPE '\' LIMIT 1), '')
               END
          FROM appliances a
          LEFT JOIN tenants t ON t.id = a.tenant_id
          LEFT JOIN sites s ON s.id = a.site_id
         WHERE a.serial ILIKE $1 ESCAPE '\' OR COALESCE(a.hostname,'') ILIKE $1 ESCAPE '\'
            OR COALESCE(a.last_public_ip,'') ILIKE $1 ESCAPE '\' OR a.id::text LIKE $2 ESCAPE '\'
            OR EXISTS (SELECT 1 FROM central_tag_links l JOIN central_tags g ON g.id = l.tag_id
                        WHERE l.appliance_id = a.id AND g.name ILIKE $1 ESCAPE '\')
         ORDER BY (lower(a.serial) LIKE $2 ESCAPE '\') DESC, lower(a.serial)
         LIMIT $3`, like, prefix, searchPerKind); err != nil {
		return res, err
	}
	if err := collect(&res.Licenses, `
        SELECT l.id::text, 'Licence ' || left(l.id::text, 8),
               concat_ws(' . ', NULLIF(t.name,''), NULLIF(a.serial,''), initcap(l.status)),
               '/licenses?q=' || left(l.id::text, 8), ''
          FROM licenses l
          JOIN tenants t ON t.id = l.tenant_id
          LEFT JOIN appliances a ON a.id = l.appliance_ids[1]
         WHERE l.id::text LIKE $1 ESCAPE '\' AND length($2) >= 5
         ORDER BY l.issued_at DESC
         LIMIT $3`, prefix, strings.TrimSuffix(prefix, "%"), searchPerKind); err != nil {
		return res, err
	}
	rows, err := b.DB.Query(ctx, `
        SELECT n.id::text, n.body,
               CASE WHEN n.customer_id IS NOT NULL THEN 'Note on ' || t.name
                    WHEN n.site_id IS NOT NULL THEN 'Note on site ' || s.name || ' (' || st.name || ')'
                    ELSE 'Note on appliance ' || a.serial END,
               CASE WHEN n.customer_id IS NOT NULL THEN '/customers/' || n.customer_id::text
                    WHEN n.site_id IS NOT NULL THEN '/customers/' || s.tenant_id::text || '?tab=sites'
                    ELSE '/appliances/' || n.appliance_id::text END
          FROM central_notes n
          LEFT JOIN tenants t ON t.id = n.customer_id
          LEFT JOIN sites s ON s.id = n.site_id
          LEFT JOIN tenants st ON st.id = s.tenant_id
          LEFT JOIN appliances a ON a.id = n.appliance_id
         WHERE n.body ILIKE $1 ESCAPE '\'
         ORDER BY n.pinned DESC, n.updated_at DESC
         LIMIT $2`, like, searchPerKind)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, body, where, href string
		if err := rows.Scan(&id, &body, &where, &href); err != nil {
			return res, err
		}
		res.Notes = append(res.Notes, SearchHit{ID: id, Title: where, Subtitle: snippet(body, q, 90), Href: href})
	}
	return res, rows.Err()
}
