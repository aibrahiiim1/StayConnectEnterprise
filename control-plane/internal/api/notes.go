package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/onegate/onegate/control-plane/internal/audit"
	"github.com/onegate/onegate/control-plane/internal/auth"
)

const (
	noteMaxLen = 4000
	tagMaxLen  = 40
	tagMaxEach = 20
)

var tagColors = map[string]bool{"neutral": true, "info": true, "ok": true, "warn": true, "err": true, "accent": true}

type subject struct {
	kind string
	id   string
}

func (s subject) column() string { return s.kind + "_id" }

func (s subject) target() string {
	if s.kind == "customer" {
		return "tenant"
	}
	return s.kind
}

func subjectOf(customerID, siteID, applianceID string, allowSite bool) (subject, error) {
	var out []subject
	for _, c := range []subject{{"customer", customerID}, {"site", siteID}, {"appliance", applianceID}} {
		if c.id = strings.ToLower(strings.TrimSpace(c.id)); c.id != "" {
			out = append(out, c)
		}
	}
	if len(out) != 1 {
		return subject{}, errors.New("name exactly one customer_id, site_id or appliance_id")
	}
	if out[0].kind == "site" && !allowSite {
		return subject{}, errors.New("tags go on customers and appliances only")
	}
	if !uuidRe.MatchString(out[0].id) {
		return subject{}, errors.New("that id is not valid")
	}
	return out[0], nil
}

func (b *Base) subjectExists(ctx context.Context, s subject) (bool, string, error) {
	table := map[string]string{"customer": "tenants", "site": "sites", "appliance": "appliances"}[s.kind]
	var customer string
	q := `SELECT COALESCE(tenant_id::text,'') FROM ` + table + ` WHERE id = $1::uuid`
	if s.kind == "customer" {
		q = `SELECT id::text FROM tenants WHERE id = $1::uuid`
	}
	err := b.DB.QueryRow(ctx, q, s.id).Scan(&customer)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	return err == nil, customer, err
}

type NoteRow struct {
	ID          string    `json:"id"`
	Body        string    `json:"body"`
	Pinned      bool      `json:"pinned"`
	AuthorID    *string   `json:"author_id"`
	AuthorName  *string   `json:"author_name"`
	AuthorEmail *string   `json:"author_email"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Edited      bool      `json:"edited"`
	CanEdit     bool      `json:"can_edit"`
	CanDelete   bool      `json:"can_delete"`
}

func canWriteNotes(r *http.Request) bool {
	return auth.FromContext(r.Context()).HasPermission(auth.PermCustomersManage)
}

func (b *Base) queryNotes(ctx context.Context, s subject, noteID string) ([]NoteRow, error) {
	col := "n." + s.column()
	where := col + " = $1::uuid"
	arg := s.id
	if noteID != "" {
		where, arg = "n.id = $1::uuid", noteID
	}
	rows, err := b.DB.Query(ctx, `
        SELECT n.id::text, n.body, n.pinned, n.author_id::text,
               CASE WHEN o.anonymised_at IS NULL THEN NULLIF(o.display_name,'') END,
               CASE WHEN o.anonymised_at IS NULL THEN o.email END,
               n.created_at, n.updated_at
          FROM central_notes n
          LEFT JOIN operators o ON o.id = n.author_id
         WHERE `+where+`
         ORDER BY n.pinned DESC, n.created_at DESC
         LIMIT 500`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NoteRow{}
	for rows.Next() {
		var n NoteRow
		if err := rows.Scan(&n.ID, &n.Body, &n.Pinned, &n.AuthorID, &n.AuthorName, &n.AuthorEmail, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		n.Edited = n.UpdatedAt.Sub(n.CreatedAt) > time.Second
		out = append(out, n)
	}
	return out, rows.Err()
}

func markNoteRights(r *http.Request, notes []NoteRow) {
	me, _ := actorOf(r)
	write := canWriteNotes(r)
	for i := range notes {
		mine := notes[i].AuthorID != nil && *notes[i].AuthorID == me
		notes[i].CanEdit = write && mine
		notes[i].CanDelete = write
	}
}

func (b *Base) listNotes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s, err := subjectOf(q.Get("customer_id"), q.Get("site_id"), q.Get("appliance_id"), true)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	notes, err := b.queryNotes(ctx, s, "")
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The notes could not be loaded.")
		return
	}
	markNoteRights(r, notes)
	WriteJSON(w, http.StatusOK, map[string]any{"items": notes, "can_write": canWriteNotes(r)})
}

func cleanNoteBody(s string) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if s == "" {
		return "", errors.New("Write something first.")
	}
	if len([]rune(s)) > noteMaxLen {
		return "", errors.New("A note can be at most 4000 characters.")
	}
	if strings.ContainsRune(s, 0) {
		return "", errors.New("The note contains characters that cannot be saved.")
	}
	return s, nil
}

func (b *Base) postNote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CustomerID  string `json:"customer_id"`
		SiteID      string `json:"site_id"`
		ApplianceID string `json:"appliance_id"`
		Body        string `json:"body"`
		Pinned      bool   `json:"pinned"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, badBody(err))
		return
	}
	s, err := subjectOf(in.CustomerID, in.SiteID, in.ApplianceID, true)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	body, err := cleanNoteBody(in.Body)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	ok, customer, err := b.subjectExists(ctx, s)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The note could not be saved. Try again.")
		return
	}
	if !ok {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "That "+s.kind+" no longer exists.")
		return
	}
	me, _ := actorOf(r)
	var id string
	if err := b.DB.QueryRow(ctx, `INSERT INTO central_notes (`+s.column()+`, body, pinned, author_id)
                                  VALUES ($1::uuid, $2, $3, NULLIF($4,'')::uuid) RETURNING id::text`,
		s.id, body, in.Pinned, me).Scan(&id); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The note could not be saved. Try again.")
		return
	}
	audit.Op(r.Context(), b.DB, r, "note.created", s.target(), s.id,
		map[string]any{"note_id": id, "length": len([]rune(body)), "pinned": in.Pinned, "_tenant_id": customer})
	b.writeOneNote(w, r, id, http.StatusCreated)
}

func (b *Base) writeOneNote(w http.ResponseWriter, r *http.Request, id string, status int) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	notes, err := b.queryNotes(ctx, subject{}, id)
	if err != nil || len(notes) == 0 {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The note was saved but could not be read back. Reload the page.")
		return
	}
	markNoteRights(r, notes)
	WriteJSON(w, status, notes[0])
}

type noteOwner struct {
	authorID string
	kind     string
	subject  string
	customer string
}

func (b *Base) noteOwner(ctx context.Context, id string) (noteOwner, error) {
	var o noteOwner
	err := b.DB.QueryRow(ctx, `
        SELECT COALESCE(n.author_id::text,''),
               CASE WHEN n.customer_id IS NOT NULL THEN 'customer' WHEN n.site_id IS NOT NULL THEN 'site' ELSE 'appliance' END,
               COALESCE(n.customer_id, n.site_id, n.appliance_id)::text,
               COALESCE(n.customer_id::text, s.tenant_id::text, a.tenant_id::text, '')
          FROM central_notes n
          LEFT JOIN sites s ON s.id = n.site_id
          LEFT JOIN appliances a ON a.id = n.appliance_id
         WHERE n.id = $1::uuid`, id).Scan(&o.authorID, &o.kind, &o.subject, &o.customer)
	return o, err
}

func (b *Base) patchNote(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !uuidRe.MatchString(id) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "That note does not exist.")
		return
	}
	var in struct {
		Body   *string `json:"body"`
		Pinned *bool   `json:"pinned"`
	}
	if err := DecodeJSON(r, &in); err != nil || (in.Body == nil && in.Pinned == nil) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "body or pinned is required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	o, err := b.noteOwner(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "That note does not exist.")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The note could not be saved. Try again.")
		return
	}
	payload := map[string]any{"note_id": id, "_tenant_id": o.customer}
	if in.Body != nil {
		me, _ := actorOf(r)
		if o.authorID == "" || o.authorID != me {
			Fail(w, r, http.StatusForbidden, CodeForbidden, "Only the person who wrote a note can change its text.")
			return
		}
		body, err := cleanNoteBody(*in.Body)
		if err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
			return
		}
		if _, err := b.DB.Exec(ctx, `UPDATE central_notes SET body = $2, updated_at = now() WHERE id = $1::uuid`, id, body); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "The note could not be saved. Try again.")
			return
		}
		payload["length"] = len([]rune(body))
	}
	if in.Pinned != nil {
		if _, err := b.DB.Exec(ctx, `UPDATE central_notes SET pinned = $2 WHERE id = $1::uuid`, id, *in.Pinned); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "The note could not be saved. Try again.")
			return
		}
		payload["pinned"] = *in.Pinned
	}
	audit.Op(r.Context(), b.DB, r, "note.updated", subject{kind: o.kind}.target(), o.subject, payload)
	b.writeOneNote(w, r, id, http.StatusOK)
}

func (b *Base) deleteNote(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !uuidRe.MatchString(id) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "That note does not exist.")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	o, err := b.noteOwner(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "That note does not exist.")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The note could not be deleted. Try again.")
		return
	}
	if _, err := b.DB.Exec(ctx, `DELETE FROM central_notes WHERE id = $1::uuid`, id); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The note could not be deleted. Try again.")
		return
	}
	me, _ := actorOf(r)
	audit.Op(r.Context(), b.DB, r, "note.deleted", subject{kind: o.kind}.target(), o.subject,
		map[string]any{"note_id": id, "own_note": o.authorID != "" && o.authorID == me, "_tenant_id": o.customer})
	w.WriteHeader(http.StatusNoContent)
}

type TagRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Color      string `json:"color"`
	Customers  int    `json:"customers"`
	Appliances int    `json:"appliances"`
}

func (b *Base) queryTags(ctx context.Context, where string, args ...any) ([]TagRow, error) {
	rows, err := b.DB.Query(ctx, `
        SELECT g.id::text, g.name, g.color,
               (SELECT count(*) FROM central_tag_links l WHERE l.tag_id = g.id AND l.customer_id IS NOT NULL),
               (SELECT count(*) FROM central_tag_links l WHERE l.tag_id = g.id AND l.appliance_id IS NOT NULL)
          FROM central_tags g `+where+`
         ORDER BY lower(g.name)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TagRow{}
	for rows.Next() {
		var t TagRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Customers, &t.Appliances); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (b *Base) listTags(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	tags, err := b.queryTags(ctx, "")
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The tags could not be loaded.")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": tags, "can_write": canWriteNotes(r)})
}

func cleanTag(name, color *string, requireName bool) (string, string, error) {
	n, c := "", ""
	if name != nil {
		n = strings.Join(strings.Fields(*name), " ")
		if n == "" || len([]rune(n)) > tagMaxLen {
			return "", "", errors.New("A tag name is 1 to 40 characters.")
		}
	} else if requireName {
		return "", "", errors.New("A tag needs a name.")
	}
	if color != nil {
		c = strings.TrimSpace(*color)
		if !tagColors[c] {
			return "", "", errors.New("Pick one of the offered colours.")
		}
	}
	return n, c, nil
}

func (b *Base) postTag(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  *string `json:"name"`
		Color *string `json:"color"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, badBody(err))
		return
	}
	name, color, err := cleanTag(in.Name, in.Color, true)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	if color == "" {
		color = "neutral"
	}
	me, _ := actorOf(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	var id string
	err = b.DB.QueryRow(ctx, `INSERT INTO central_tags (name, color, created_by) VALUES ($1, $2, NULLIF($3,'')::uuid)
                               RETURNING id::text`, name, color, me).Scan(&id)
	if isUniqueViolation(err) {
		Fail(w, r, http.StatusConflict, CodeConflict, "A tag called "+name+" already exists.")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The tag could not be created. Try again.")
		return
	}
	audit.Op(r.Context(), b.DB, r, "tag.created", "tag", id, map[string]any{"name": name, "color": color})
	tags, _ := b.queryTags(ctx, "WHERE g.id = $1::uuid", id)
	if len(tags) == 1 {
		WriteJSON(w, http.StatusCreated, tags[0])
		return
	}
	WriteJSON(w, http.StatusCreated, TagRow{ID: id, Name: name, Color: color})
}

func (b *Base) patchTag(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !uuidRe.MatchString(id) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "That tag does not exist.")
		return
	}
	var in struct {
		Name  *string `json:"name"`
		Color *string `json:"color"`
	}
	if err := DecodeJSON(r, &in); err != nil || (in.Name == nil && in.Color == nil) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "name or color is required")
		return
	}
	name, color, err := cleanTag(in.Name, in.Color, false)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	tag, err := b.DB.Exec(ctx, `UPDATE central_tags SET name = COALESCE(NULLIF($2,''), name), color = COALESCE(NULLIF($3,''), color)
                                 WHERE id = $1::uuid`, id, name, color)
	if isUniqueViolation(err) {
		Fail(w, r, http.StatusConflict, CodeConflict, "A tag called "+name+" already exists.")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The tag could not be saved. Try again.")
		return
	}
	if tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "That tag does not exist.")
		return
	}
	audit.Op(r.Context(), b.DB, r, "tag.updated", "tag", id, map[string]any{"name": name, "color": color})
	tags, err := b.queryTags(ctx, "WHERE g.id = $1::uuid", id)
	if err != nil || len(tags) != 1 {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The tag was saved but could not be read back. Reload the page.")
		return
	}
	WriteJSON(w, http.StatusOK, tags[0])
}

func (b *Base) deleteTag(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !uuidRe.MatchString(id) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "That tag does not exist.")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var name string
	err := b.DB.QueryRow(ctx, `DELETE FROM central_tags WHERE id = $1::uuid RETURNING name`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "That tag does not exist.")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The tag could not be deleted. Try again.")
		return
	}
	audit.Op(r.Context(), b.DB, r, "tag.deleted", "tag", id, map[string]any{"name": name})
	w.WriteHeader(http.StatusNoContent)
}

func (b *Base) subjectTags(ctx context.Context, s subject) ([]TagRow, error) {
	return b.queryTags(ctx, "WHERE EXISTS (SELECT 1 FROM central_tag_links l WHERE l.tag_id = g.id AND l."+s.column()+" = $1::uuid)", s.id)
}

func (b *Base) getSubjectTags(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s, err := subjectOf(q.Get("customer_id"), "", q.Get("appliance_id"), false)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	tags, err := b.subjectTags(ctx, s)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The tags could not be loaded.")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": tags, "can_write": canWriteNotes(r)})
}

func (b *Base) putSubjectTags(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CustomerID  string   `json:"customer_id"`
		ApplianceID string   `json:"appliance_id"`
		TagIDs      []string `json:"tag_ids"`
	}
	if err := DecodeJSON(r, &in); err != nil || in.TagIDs == nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tag_ids is required")
		return
	}
	s, err := subjectOf(in.CustomerID, "", in.ApplianceID, false)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	if len(in.TagIDs) > tagMaxEach {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "At most 20 tags can go on one record.")
		return
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, t := range in.TagIDs {
		t = strings.ToLower(strings.TrimSpace(t))
		if !uuidRe.MatchString(t) {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "A tag id is not valid.")
			return
		}
		if !seen[t] {
			seen[t] = true
			ids = append(ids, t)
		}
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	ok, customer, err := b.subjectExists(ctx, s)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The tags could not be saved. Try again.")
		return
	}
	if !ok {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "That "+s.kind+" no longer exists.")
		return
	}
	before, err := b.subjectTags(ctx, s)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The tags could not be saved. Try again.")
		return
	}
	me, _ := actorOf(r)
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The tags could not be saved. Try again.")
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM central_tag_links WHERE `+s.column()+` = $1::uuid AND NOT (tag_id::text = ANY($2))`, s.id, ids); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The tags could not be saved. Try again.")
		return
	}
	for _, t := range ids {
		tag, err := tx.Exec(ctx, `INSERT INTO central_tag_links (tag_id, `+s.column()+`, created_by)
                                   SELECT g.id, $2::uuid, NULLIF($3,'')::uuid FROM central_tags g WHERE g.id = $1::uuid
                                   ON CONFLICT DO NOTHING`, t, s.id, me)
		if err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "The tags could not be saved. Try again.")
			return
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			_ = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM central_tags WHERE id = $1::uuid)`, t).Scan(&exists)
			if !exists {
				Fail(w, r, http.StatusConflict, CodeConflict, "A tag was deleted meanwhile. Reload and try again.")
				return
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The tags could not be saved. Try again.")
		return
	}
	after, err := b.subjectTags(ctx, s)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The tags were saved but could not be read back. Reload the page.")
		return
	}
	names := func(ts []TagRow) []string {
		out := []string{}
		for _, t := range ts {
			out = append(out, t.Name)
		}
		return out
	}
	audit.Op(r.Context(), b.DB, r, "tags.updated", s.target(), s.id,
		map[string]any{"from": names(before), "to": names(after), "_tenant_id": customer})
	WriteJSON(w, http.StatusOK, map[string]any{"items": after, "can_write": true})
}
