package main

import (
	"fmt"
	"os/user"
)

// The socket has its own group: portald gets only this group (held by edged and portald via SupplementaryGroups=) rather than edged's primary
// group stayconnect, which would expose env files with database credentials, the DB dumps and netd's socket. Per-route peer checks
// (admin_surface.go) still decide what each caller may do. Provisioned by provision-fresh-appliance.sh; until the group exists the legacy group is used, loudly.
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
