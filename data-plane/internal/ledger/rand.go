package ledger

import "crypto/rand"

type randReader struct{}

func (randReader) Read(p []byte) (int, error) { return rand.Read(p) }
