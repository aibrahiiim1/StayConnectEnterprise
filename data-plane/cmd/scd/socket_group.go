package main

import (
	"fmt"
	"os/user"
)

// THE SOCKET HAS ITS OWN GROUP (audit N-26).
//
// scd's socket used to be root:stayconnect 0660, and portald -- the process every guest network talks to -- was
// given the whole stayconnect group to reach it. That group is edged's PRIMARY group, so it also opened every
// other file grouped stayconnect: edged's, netd's and acctd's env files (database credentials), the 0640 database
// dumps in /opt/stayconnect/backups/db and netd's socket. A compromised portal would have read the credentials of
// more privileged services. The socket now carries a group that does nothing else, held by exactly the two
// services that call scd (edged and portald, through SupplementaryGroups= in their units), so reaching scd
// buys nothing more. scd's own per-route peer checks (admin_surface.go) still decide what each caller may do.
//
// defaultSocketGroup is provisioned by provision-fresh-appliance.sh. An appliance upgraded before the group
// exists keeps working on the legacy group, loudly, until the group is created.
const (
	defaultSocketGroup = "stayconnect-scd"
	legacySocketGroup  = "stayconnect"
)

// lookupSocketGroup resolves the configured group (SCD_SOCKET_GROUP), else the dedicated default, else the legacy
// group. An explicitly configured group that does not exist is an error: a typo must not silently widen access.
func lookupSocketGroup(configured string, lookup func(string) (*user.Group, error)) (*user.Group, error) {
	if configured != "" {
		g, err := lookup(configured)
		if err != nil {
			return nil, fmt.Errorf("SCD_SOCKET_GROUP=%q: %w", configured, err)
		}
		return g, nil
	}
	if g, err := lookup(defaultSocketGroup); err == nil {
		return g, nil
	}
	return lookup(legacySocketGroup)
}
