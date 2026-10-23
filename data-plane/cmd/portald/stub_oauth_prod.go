//go:build !devlicense

package main

import "github.com/go-chi/chi/v5"

// mountStubOAuth mounts nothing in production: the dev stub accepts any identity and redirects to a URI it is handed.
// /api/oauth/stub/* answers as any unknown path does.
func mountStubOAuth(chi.Router, *handler) {}
