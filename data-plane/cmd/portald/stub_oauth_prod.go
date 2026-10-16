//go:build !devlicense

package main

import "github.com/go-chi/chi/v5"

// mountStubOAuth mounts nothing in a production build: the development OAuth stub (stub_oauth_dev.go) accepts any
// identity named in its code and redirects to a URI it is handed, so a production portald does not contain it
// (HTTPS contract section 11, audit P-2). /api/oauth/stub/* answers as any unknown path does: the portal.
func mountStubOAuth(chi.Router, *handler) {}
