//go:build integration

package main

import (
	"context"
	"net/http"
	"testing"
)

func (f *apiFixture) newProfile(t *testing.T, name, copyFrom string) string {
	t.Helper()
	st, body := f.do(t, "POST", "/portal-branding/profiles", map[string]any{"name": name, "copy_from": copyFrom})
	if st != 201 {
		t.Fatalf("create profile %q = %d %v", name, st, body)
	}
	return body["id"].(string)
}

func (f *apiFixture) publishDesign(t *testing.T, path string, design map[string]any) {
	t.Helper()
	st, body := f.do(t, "POST", path+"/publish", map[string]any{"design": design, "password": f.password})
	if st != 200 {
		t.Fatalf("publish %s = %d %v", path, st, body)
	}
}

func designTitle(t *testing.T, f *apiFixture, path string) any {
	t.Helper()
	st, body := f.do(t, "GET", path, nil)
	if st != 200 {
		t.Fatalf("get %s = %d %v", path, st, body)
	}
	d, _ := body["design"].(map[string]any)
	return d["welcome_title"]
}

func TestIntegration_PortalProfiles_AreSeparateDesignsAndLeaveTheDefaultAlone(t *testing.T) {
	f := newAPI(t)
	f.publishDesign(t, "/portal-branding", map[string]any{"welcome_title": "Site default"})

	pool := f.newProfile(t, "Pool bar", "default")
	base := "/portal-branding/profiles/" + pool + "/branding"
	if got := designTitle(t, f, base); got != "Site default" {
		t.Fatalf("a new profile does not start as a copy of the default: %v", got)
	}

	f.publishDesign(t, base, map[string]any{"welcome_title": "Pool bar"})
	if got := designTitle(t, f, "/portal-branding"); got != "Site default" {
		t.Fatalf("publishing a profile changed the site default: %v", got)
	}
	if got := designTitle(t, f, base); got != "Pool bar" {
		t.Fatalf("the profile did not keep its own design: %v", got)
	}

	if st, _ := f.do(t, "POST", base+"/rollback/1", map[string]any{"password": f.password}); st != 200 {
		t.Fatalf("a profile's history cannot be restored: %d", st)
	}
	if got := designTitle(t, f, base); got != "Site default" {
		t.Fatalf("restore did not bring back the profile's earlier revision: %v", got)
	}
	if got := designTitle(t, f, "/portal-branding"); got != "Site default" {
		t.Fatalf("restoring a profile touched the default: %v", got)
	}
}

func TestIntegration_PortalProfiles_NamesAreUniqueAndIdsAreScoped(t *testing.T) {
	f := newAPI(t)
	id := f.newProfile(t, "Lobby", "default")
	if st, _ := f.do(t, "POST", "/portal-branding/profiles", map[string]any{"name": " lobby "}); st != 409 {
		t.Fatalf("a duplicate name = %d", st)
	}
	if st, _ := f.do(t, "POST", "/portal-branding/profiles", map[string]any{"name": "  "}); st != 400 {
		t.Fatalf("a blank name = %d", st)
	}
	for _, bad := range []string{"not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		if st, _ := f.do(t, "GET", "/portal-branding/profiles/"+bad+"/branding", nil); st != 404 {
			t.Fatalf("profile %q = %d", bad, st)
		}
	}
	// A profile of another site is not reachable by its id.
	var other string
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO iam_v2.portal_profiles (tenant_id, site_id, name) VALUES ($1::uuid, gen_random_uuid(), 'Elsewhere') RETURNING id::text`, f.tenant).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.do(t, "GET", "/portal-branding/profiles/"+other+"/branding", nil); st != 404 {
		t.Fatalf("another site's profile = %d", st)
	}
	if st, _ := f.do(t, "DELETE", "/portal-branding/profiles/"+other, nil); st != 404 {
		t.Fatalf("deleting another site's profile = %d", st)
	}
	if st, _ := f.do(t, "PATCH", "/portal-branding/profiles/"+id, map[string]any{"name": "Reception"}); st != 200 {
		t.Fatalf("rename = %d", st)
	}
}

func TestIntegration_PortalProfiles_AssignmentAndDeletionReturnNetworksToTheDefault(t *testing.T) {
	f := newAPI(t)
	net := f.seedNet(t, "Conference")
	p := f.newProfile(t, "Conference", "default")

	if st, body := f.do(t, "PUT", "/portal-branding/profiles/assignments/"+net, map[string]any{"profile_id": p}); st != 200 {
		t.Fatalf("assign = %d %v", st, body)
	}
	_, list := f.do(t, "GET", "/portal-branding/profiles", nil)
	profiles := list["profiles"].([]any)
	if len(profiles) != 1 || len(profiles[0].(map[string]any)["network_ids"].([]any)) != 1 {
		t.Fatalf("assignment not listed: %v", list)
	}
	for _, n := range list["default"].(map[string]any)["network_ids"].([]any) {
		if n == net {
			t.Fatal("an assigned network is still listed under the default")
		}
	}

	if st, _ := f.do(t, "PUT", "/portal-branding/profiles/assignments/"+net, map[string]any{"profile_id": "11111111-1111-1111-1111-111111111111"}); st != 404 {
		t.Fatalf("assigning a profile that does not exist = %d", st)
	}
	if st, _ := f.do(t, "PUT", "/portal-branding/profiles/assignments/11111111-1111-1111-1111-111111111111", map[string]any{"profile_id": p}); st != 404 {
		t.Fatalf("assigning a network that does not exist = %d", st)
	}

	st, del := f.do(t, "DELETE", "/portal-branding/profiles/"+p, nil)
	if st != 200 || del["networks_returned_to_default"] != float64(1) {
		t.Fatalf("delete = %d %v", st, del)
	}
	var n int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.portal_profile_assignments WHERE network_id=$1::uuid`, net).Scan(&n)
	if n != 0 {
		t.Fatal("the assignment outlived its profile")
	}
}

func TestIntegration_PortalProfiles_AssetsInUseSpansProfiles(t *testing.T) {
	f := newAPI(t)
	p := f.newProfile(t, "Beach", "default")
	f.publishDesign(t, "/portal-branding/profiles/"+p+"/branding", map[string]any{"logo_url": "/assets/beach-logo.png"})
	req, _ := http.NewRequest("GET", "/", nil)
	used, err := f.app.assetsInUse(req)
	if err != nil {
		t.Fatal(err)
	}
	if !used["beach-logo.png"] {
		t.Fatalf("an image used only by a profile is not reported as in use: %v", used)
	}
}
