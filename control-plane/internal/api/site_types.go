package api

import (
	"net/http"
	"strings"

	lic "github.com/stayconnect/enterprise/license"
)

// SITE TYPES AND THE MODULE REGISTRY.
//
// A site type is descriptive: it tells the appliance (through the signed assignment) and the operator what kind
// of place the site is. It NEVER authorises or gates anything; licence modules do (license/modules.go). The
// database column accepts any value of the right shape so a later type needs no migration; the API accepts only
// the types below for writes.

// SiteTypeUnspecified is the type of a site nobody has classified. The assignment carries it as an empty value
// so an untyped site's document signs byte-identically to the pre-site-type layout.
const SiteTypeUnspecified = "UNSPECIFIED"

// SiteTypes are the values Central accepts for writes, in display order. Mirrored by cloud-admin lib/site-types.ts.
var SiteTypes = []string{
	"HOTEL", "CAFE", "OFFICE", "CLINIC", "CAMPUS", "VENUE", "COMPOUND", "BEACH_CLUB", "OTHER", SiteTypeUnspecified,
}

// siteTypePresets are SUGGESTED modules for a new licence at a site of that type. Suggestions only: the operator
// chooses the modules, and a site's type never grants one.
var siteTypePresets = map[string][]string{
	"HOTEL": {lic.ModuleHospitality},
}

// normalizeSiteType validates a site type for a write. "" means UNSPECIFIED; case and surrounding spaces are
// forgiven. It returns the canonical value, or false for a value Central does not know.
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

// assignmentSiteType is the value signed into an assignment: empty for UNSPECIFIED (omitempty keeps an untyped
// site's document byte-identical to the old layout), the stored value otherwise.
func assignmentSiteType(stored string) string {
	if stored == SiteTypeUnspecified {
		return ""
	}
	return stored
}

// moduleInfo is one registered module as the console sees it.
type moduleInfo struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Requires []string `json:"requires"`
}

// siteTypeInfo is one site type with its suggested modules.
type siteTypeInfo struct {
	ID     string   `json:"id"`
	Preset []string `json:"preset"`
}

// modulesCatalog is the body of GET /cloud/v1/modules.
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

// listModules: GET /cloud/v1/modules — the licence module registry (id, label, requires) and the site types
// with their suggested modules, so the console never hard-codes a dependency. Static; any signed-in operator.
func (b *Base) listModules(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, buildModulesCatalog())
}
