package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

// AD-10: with no backup key holder the departing customer's archive is not written, in any form.
func TestArchiveIsRefusedWithoutAKeyHolderAndNeverWrittenInTheClear(t *testing.T) {
	if err := requireArchiveKeyHolder(nil); !errors.Is(err, errNoArchiveKeyHolder) {
		t.Fatalf("no holder was accepted: %v", err)
	}
	// The operator is told exactly what to do, and that nothing was removed.
	for _, want := range []string{"key holder", "never written unencrypted", "nothing was removed", "Backup keys"} {
		if !strings.Contains(errNoArchiveKeyHolder.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, errNoArchiveKeyHolder)
		}
	}
	p := filepath.Join(t.TempDir(), "tenant.json.age")
	if err := writeArchiveFile(p, []byte(`{"card":"4111"}`), nil); !errors.Is(err, errNoArchiveKeyHolder) {
		t.Fatalf("an unencrypted write was attempted: %v", err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("a file was created for an archive that cannot be encrypted")
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeArchiveFile(p, []byte(`{"card":"4111"}`), []age.Recipient{id.Recipient()}); err != nil {
		t.Fatalf("an encrypted archive was refused: %v", err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "4111") {
		t.Fatal("the archive is readable in the clear")
	}
}
