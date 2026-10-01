package main

// THE GENERATED CLIENT-ACCOUNT PASSWORD FORMAT (migration 0104).
//
// A client account can be given a server-generated password on create and on set-password. What that password
// looks like -- which characters, and how many -- is a per-site operational setting, persisted, bounded,
// defaulted and audited (docs/OPERATIONAL_SETTINGS_POLICY.md). It affects ONLY passwords generated after a
// change: no existing credential is touched, and operator-typed passwords keep their own rules
// (validPassword: 1-128 characters, no control characters).
//
// THE SECURITY FLOOR. Every style must reach at least 40 bits of estimated entropy, so its minimum length is
// ceil(40 / log2(alphabet size)); every style is capped at 32. The same numbers are enforced by the SQL setter
// and by the table CHECK, and a unit test holds the constants below to the formula. They are sent to the UI
// in the GET response, so the browser never carries its own copy.

import (
	"context"
	"crypto/rand"
	"errors"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	pwStyleMixed       = "mixed"
	pwStyleUpperDigits = "upper_digits"
	pwStyleLowerDigits = "lower_digits"
	pwStyleDigits      = "digits"

	// The default is exactly what the hardcoded generator produced before 0104.
	defaultPasswordStyle  = pwStyleMixed
	defaultPasswordLength = 14

	passwordEntropyFloorBits = 40
	maxGeneratedPasswordLen  = 32
)

// Every alphabet leaves out the characters people misread from a slip or a screen: 0/O, 1/I/l and lowercase o.
// Digits-only therefore uses 2-9, so a printed password means the same thing in every style.
const (
	pwUpper  = "ABCDEFGHJKMNPQRSTUVWXYZ"  // 23: no I, L, O
	pwLower  = "abcdefghijkmnpqrstuvwxyz" // 24: no l, o
	pwDigits = "23456789"                 // 8: no 0, 1
)

type passwordStyle struct {
	Key      string
	Label    string
	Alphabet string
	// MinLength is ceil(40 / log2(len(Alphabet))). Held as a constant so the SQL CASE and this table are
	// visibly the same numbers; TestPasswordStyleMinimumsMeetTheEntropyFloor recomputes it.
	MinLength int
}

// passwordStyles, in the order the UI offers them.
var passwordStyles = []passwordStyle{
	{pwStyleMixed, "Upper and lower case letters and digits", pwUpper + pwLower + pwDigits, 7},
	{pwStyleUpperDigits, "Upper case letters and digits", pwUpper + pwDigits, 9},
	{pwStyleLowerDigits, "Lower case letters and digits", pwLower + pwDigits, 8},
	{pwStyleDigits, "Digits only", pwDigits, 14},
}

func lookupPasswordStyle(key string) (passwordStyle, bool) {
	for _, s := range passwordStyles {
		if s.Key == key {
			return s, true
		}
	}
	return passwordStyle{}, false
}

// entropyBits is the estimated entropy of a uniformly random password of this style and length.
func (p passwordStyle) entropyBits(length int) float64 {
	return float64(length) * math.Log2(float64(len(p.Alphabet)))
}

var errPasswordFormat = errors.New("invalid password format")

// validatePasswordFormat mirrors iam_v2.account_password_settings_set and the table CHECK.
func validatePasswordFormat(style string, length int) (passwordStyle, string, bool) {
	p, ok := lookupPasswordStyle(style)
	if !ok {
		return p, "password_style must be mixed, upper_digits, lower_digits or digits", false
	}
	if length < p.MinLength || length > maxGeneratedPasswordLen {
		return p, "a " + p.Key + " password must be between " + strconv.Itoa(p.MinLength) + " and " +
			strconv.Itoa(maxGeneratedPasswordLen) + " characters", false
	}
	return p, "", true
}

// generatePasswordWith draws length characters uniformly from the style's alphabet with crypto/rand. It
// refuses a format below the floor rather than producing a weak password: a stored row that somehow escaped
// both the setter and the CHECK must not be honoured.
func generatePasswordWith(style string, length int) (string, error) {
	p, _, ok := validatePasswordFormat(style, length)
	if !ok {
		return "", errPasswordFormat
	}
	b := make([]byte, length)
	max := big.NewInt(int64(len(p.Alphabet)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = p.Alphabet[idx.Int64()]
	}
	return string(b), nil
}

// ---- the stored setting ---------------------------------------------------------------------------------

type accountPasswordFormat struct {
	Style     string     `json:"password_style"`
	Length    int        `json:"password_length"`
	Version   int64      `json:"config_version"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	UpdatedBy *string    `json:"updated_by,omitempty"`
}

// accountPasswordFormat reads the site's format. A site with no row gets the defaults (config_version 0); a
// DATABASE ERROR is returned, never papered over with a default -- the generator then refuses.
func (s *server) accountPasswordFormat(ctx context.Context) (accountPasswordFormat, error) {
	var f accountPasswordFormat
	err := s.db.QueryRow(ctx, `
		SELECT password_style, password_length, config_version, updated_at, updated_by
		  FROM iam_v2.account_password_settings_get($1::uuid, $2::uuid)`,
		s.tenantID, s.siteID).Scan(&f.Style, &f.Length, &f.Version, &f.UpdatedAt, &f.UpdatedBy)
	return f, err
}

// generateAccountPassword answers the "Generate" action of create and set-password. It writes the HTTP error
// itself and reports false when it could not produce a password.
func (s *server) generateAccountPassword(w http.ResponseWriter, ctx context.Context) (string, bool) {
	f, err := s.accountPasswordFormat(ctx)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "password_format_unreadable",
			"the generated-password format could not be read; no password was generated")
		return "", false
	}
	pw, err := generatePasswordWith(f.Style, f.Length)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "generate failed")
		return "", false
	}
	return pw, true
}

// ---- the operator surface -------------------------------------------------------------------------------

type passwordStyleOut struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	Alphabet    string  `json:"alphabet"`
	MinLength   int     `json:"min_length"`
	MaxLength   int     `json:"max_length"`
	BitsPerChar float64 `json:"bits_per_char"`
}

type passwordFormatLimitsOut struct {
	EntropyFloorBits int                `json:"entropy_floor_bits"`
	MaxLength        int                `json:"max_length"`
	DefaultStyle     string             `json:"default_style"`
	DefaultLength    int                `json:"default_length"`
	Styles           []passwordStyleOut `json:"styles"`
}

func passwordFormatLimits() passwordFormatLimitsOut {
	out := passwordFormatLimitsOut{
		EntropyFloorBits: passwordEntropyFloorBits,
		MaxLength:        maxGeneratedPasswordLen,
		DefaultStyle:     defaultPasswordStyle,
		DefaultLength:    defaultPasswordLength,
	}
	for _, p := range passwordStyles {
		out.Styles = append(out.Styles, passwordStyleOut{
			Key: p.Key, Label: p.Label, Alphabet: p.Alphabet,
			MinLength: p.MinLength, MaxLength: maxGeneratedPasswordLen,
			BitsPerChar: math.Round(math.Log2(float64(len(p.Alphabet)))*1000) / 1000,
		})
	}
	return out
}

func (s *server) accountPasswordSettingsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getAccountPasswordSettings)
	r.Put("/", s.setAccountPasswordSettings)
	r.Get("/changes", s.listAccountPasswordSettingChanges)
	return r
}

func (s *server) getAccountPasswordSettings(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		// No site yet: nothing can be generated or saved, but the screen can still show the defaults and bounds.
		writeJSON(w, http.StatusOK, map[string]any{
			"password_style": defaultPasswordStyle, "password_length": defaultPasswordLength, "config_version": 0,
			"awaiting_assignment": true, "limits": passwordFormatLimits(),
		})
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	f, err := s.accountPasswordFormat(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the password format could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"password_style": f.Style, "password_length": f.Length, "config_version": f.Version,
		"updated_at": f.UpdatedAt, "updated_by": f.UpdatedBy,
		"limits": passwordFormatLimits(),
	})
}

func (s *server) setAccountPasswordSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Style  string `json:"password_style"`
		Length int    `json:"password_length"`
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if _, msg, ok := validatePasswordFormat(in.Style, in.Length); !ok {
		code := "invalid_length"
		if _, known := lookupPasswordStyle(in.Style); !known {
			code = "invalid_style"
		}
		jsonErr(w, http.StatusBadRequest, code, msg)
		return
	}
	// Required here (0085 left it optional): changing how strong every future generated password is, is a
	// security decision and must say why.
	reason := strings.TrimSpace(in.Reason)
	if !validReason(reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return
	}
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "no operator session")
		return
	}
	label := sess.Email
	if label == "" {
		label = sess.DisplayName
	}
	if label == "" {
		label = sess.OperatorID
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var version int64
	if err := s.db.QueryRow(ctx,
		`SELECT iam_v2.account_password_settings_set($1::uuid, $2::uuid, $3, $4, $5, $6)`,
		s.tenantID, s.siteID, in.Style, in.Length, label, reason).Scan(&version); err != nil {
		jsonErr(w, http.StatusInternalServerError, "update_failed", "the password format could not be saved")
		return
	}
	s.audit(r, "guest_account.password_format_changed", "account_password_settings", "", map[string]any{
		"password_style": in.Style, "password_length": in.Length, "config_version": version, "reason": reason,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"password_style": in.Style, "password_length": in.Length, "config_version": version,
		"notice": "This applies to the next password you generate. Existing passwords are unchanged.",
	})
}

func (s *server) listAccountPasswordSettingChanges(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "the password format history")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
	    SELECT changed_at, changed_by, change_reason, old_password_style, old_password_length,
	           new_password_style, new_password_length, new_config_version
	      FROM iam_v2.account_password_settings_changes
	     WHERE tenant_id = $1 AND site_id = $2
	     ORDER BY changed_at DESC LIMIT 50`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the format history could not be read")
		return
	}
	defer rows.Close()
	type change struct {
		At        time.Time `json:"changed_at"`
		By        string    `json:"changed_by"`
		Reason    string    `json:"change_reason"`
		OldStyle  *string   `json:"old_password_style"`
		OldLength *int      `json:"old_password_length"`
		NewStyle  string    `json:"new_password_style"`
		NewLength int       `json:"new_password_length"`
		Version   int64     `json:"new_config_version"`
	}
	out := []change{}
	for rows.Next() {
		var c change
		if err := rows.Scan(&c.At, &c.By, &c.Reason, &c.OldStyle, &c.OldLength,
			&c.NewStyle, &c.NewLength, &c.Version); err != nil {
			jsonErr(w, http.StatusInternalServerError, "query_failed", "the format history could not be read")
			return
		}
		out = append(out, c)
	}
	if rows.Err() != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the format history could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": out})
}
