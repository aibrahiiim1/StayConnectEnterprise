package main

// PAGING THE DHCP LISTS.
//
// The active-lease list comes whole from netd (which reads it from Kea); netd has no paging of its own, and a
// guest network's pool is bounded, so edged pages the list it already holds rather than teaching netd and Kea a
// second protocol. The search runs over the whole list before paging, so a lease on page 4 is found from page 1.
//
// A caller that sends no ?page / ?page_size gets the whole list exactly as before: the overview and older
// screens read it that way.

import (
	"encoding/json"
	"errors"
	"net/netip"
	"sort"
	"strings"
)

// dhcpSearchHeader carries the DHCP screen's search (an address, a MAC or a device name), never the URL.
const dhcpSearchHeader = "X-Dhcp-Search"

type dhcpLeasesPage struct {
	Leases   []json.RawMessage `json:"leases"`
	Meta     listMeta          `json:"meta"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
	Total    int               `json:"total"`
}

// pageLeases filters netd's lease list by search, orders it by address so a page boundary is stable, and
// returns one page. Each lease is passed through as netd wrote it: edged reads only the three fields it
// searches and sorts by, so a field netd adds later still reaches the screen.
func pageLeases(body []byte, search string, pg pageReq) (dhcpLeasesPage, error) {
	var v struct {
		Leases []json.RawMessage `json:"leases"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return dhcpLeasesPage{}, errors.New("the lease list could not be read")
	}
	type keyed struct {
		raw  json.RawMessage
		addr netip.Addr
		ip   string
	}
	needle := strings.ToLower(strings.TrimSpace(search))
	all := make([]keyed, 0, len(v.Leases))
	for _, raw := range v.Leases {
		var f struct {
			IP       string `json:"ip-address"`
			MAC      string `json:"hw-address"`
			Hostname string `json:"hostname"`
		}
		_ = json.Unmarshal(raw, &f)
		if needle != "" && !strings.Contains(strings.ToLower(f.IP), needle) &&
			!strings.Contains(strings.ToLower(f.MAC), needle) &&
			!strings.Contains(strings.ToLower(f.Hostname), needle) {
			continue
		}
		a, _ := netip.ParseAddr(f.IP)
		all = append(all, keyed{raw: raw, addr: a, ip: f.IP})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].addr.IsValid() && all[j].addr.IsValid() {
			return all[i].addr.Less(all[j].addr)
		}
		if all[i].addr.IsValid() != all[j].addr.IsValid() {
			return all[i].addr.IsValid()
		}
		return all[i].ip < all[j].ip
	})
	page, more := slicePage(all, pg)
	out := dhcpLeasesPage{Leases: make([]json.RawMessage, 0, len(page)), Meta: listMeta{HasMore: more},
		Page: pg.Page, PageSize: pg.Size, Total: len(all)}
	for _, k := range page {
		out.Leases = append(out.Leases, k.raw)
	}
	return out, nil
}
