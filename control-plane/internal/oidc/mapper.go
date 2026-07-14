package oidc

import "encoding/json"

// ClaimsMap is the per-provider role-mapping config stored as jsonb in
// idp_providers.claims_map. Shape:
//
//	{
//	  "default_role":  "tenant_operator",
//	  "groups_to_role": {"sc-admins":"tenant_admin","sc-billing":"billing"}
//	}
type ClaimsMap struct {
	DefaultRole  string            `json:"default_role,omitempty"`
	GroupsToRole map[string]string `json:"groups_to_role,omitempty"`
}

func ParseClaimsMap(raw []byte) ClaimsMap {
	var cm ClaimsMap
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cm)
	}
	return cm
}

// ResolveRoles maps the IdP's groups claim to roles. Always at least the
// default role (when set) so an SSO user without group entitlements still
// has minimum access.
//
// Order is deterministic: the explicit group mappings come first, then the
// default role last (de-duplicated).
func (m ClaimsMap) ResolveRoles(groups []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, g := range groups {
		if r, ok := m.GroupsToRole[g]; ok {
			if _, dup := seen[r]; !dup {
				seen[r] = struct{}{}
				out = append(out, r)
			}
		}
	}
	if m.DefaultRole != "" {
		if _, dup := seen[m.DefaultRole]; !dup {
			out = append(out, m.DefaultRole)
		}
	}
	return out
}
