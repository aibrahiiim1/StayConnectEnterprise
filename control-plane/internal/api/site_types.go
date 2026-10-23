package api

import (
	"net/http"
	"strings"

	lic "github.com/stayconnect/enterprise/license"
)

// A site type is descriptive only; it never authorises or gates anything (licence modules do). The database column
// accepts any value of the right shape so a later type needs no migration; the API accepts only the types below.

// SiteTypeUnspecified is the type of an unclassified site. The assignment carries it as empty so an untyped
// site's document signs byte-identically.
const SiteTypeUnspecified = "UNSPECIFIED"

// SiteTypes are the values Central accepts for writes, in display order. Mirrored by cloud-admin lib/site-types.ts.
var SiteTypes = []string{
	"HOTEL", "CAFE", "OFFICE", "CLINIC", "CAMPUS", "VENUE", "COMPOUND", "BEACH_CLUB", "OTHER", SiteTypeUnspecified,
}

// siteTypePresets are suggested modules for a new licence; a site's type never grants one.
var siteTypePresets = map[string][]string{
	"HOTEL": {lic.ModuleHospitality},
}

// normalizeSiteType returns the canonical value ("" means UNSPECIFIED; case and spaces are forgiven), or false if unknown.
func normalizeSiteType(v string) (string, bool) {
	v = strings.ToUpper(strings.TrimSpace(v))
	if v == "" {
		return SiteTypeUnspecified, true
	}
	for _, t := range SiteTypes {
		if t == v {
			return v, true
		}
	}
	return "", false
}

const msgUnknownSiteType = "site_type must be one of HOTEL, CAFE, OFFICE, CLINIC, CAMPUS, VENUE, COMPOUND, BEACH_CLUB, OTHER or UNSPECIFIED"

// assignmentSiteType is the value signed into an assignment: empty for UNSPECIFIED (keeps an untyped site's document byte-identical).
func assignmentSiteType(stored string) string {
	if stored == SiteTypeUnspecified {
		return ""
	}
	return stored
}

type moduleInfo struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Requires []string `json:"requires"`
}

type siteTypeInfo struct {
	ID     string   `json:"id"`
	Preset []string `json:"preset"`
}

type modulesCatalog struct {
	Modules   []moduleInfo   `json:"modules"`
	SiteTypes []siteTypeInfo `json:"site_types"`
}

func buildModulesCatalog() modulesCatalog {
	out := modulesCatalog{Modules: []moduleInfo{}, SiteTypes: []siteTypeInfo{}}
	for _, m := range lic.Registry() {
		req := append([]string{}, m.Requires...)
		out.Modules = append(out.Modules, moduleInfo{ID: m.ID, Label: m.Label, Requires: req})
	}
	for _, t := range SiteTypes {
		out.SiteTypes = append(out.SiteTypes, siteTypeInfo{ID: t, Preset: append([]string{}, siteTypePresets[t]...)})
	}
	return out
}

// listModules: GET /cloud/v1/modules — the module registry and site types, so the console never hard-codes a dependency.
func (b *Base) listModules(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, buildModulesCatalog())
}
