package payment

// A marker that cannot be read is not a marker that is not there: ReadRestoreMarker must return an error for an
// unreadable or malformed marker, not (0, false). The permission case is skipped when running as root, which defeats
// it.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAMissingMarkerIsAbsentAndNotAnError(t *testing.T) {
	t.Setenv(EnvMarkerPath, filepath.Join(t.TempDir(), "not-created.json"))
	gen, present, err := ReadRestoreMarker()
	if err != nil {
		t.Fatalf("a site that has never been restored has no marker; that is the normal case, got %v", err)
	}
	if present || gen != 0 {
		t.Errorf("expected absent/0, got present=%v gen=%d", present, gen)
	}
}

func TestAMarkerThatExistsIsReadWithItsGeneration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "marker.json")
	if err := os.WriteFile(p, []byte(`{"restore_generation": 7, "advanced_at": "2026-09-23T03:23:52Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMarkerPath, p)
	gen, present, err := ReadRestoreMarker()
	if err != nil || !present || gen != 7 {
		t.Errorf("expected 7/present/no error, got gen=%d present=%v err=%v", gen, present, err)
	}
}

func TestAnUnreadableMarkerIsAnErrorAndNotAbsence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file-mode permissions are not enforced the same way here")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0000 file is still readable, so this cannot be produced")
	}
	p := filepath.Join(t.TempDir(), "marker.json")
	if err := os.WriteFile(p, []byte(`{"restore_generation": 1}`), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMarkerPath, p)

	gen, present, err := ReadRestoreMarker()
	if err == nil {
		t.Fatal("an existing marker that cannot be read was reported as absence; that is the PRE-LIVE " +
			"defect -- a real restore concluded UNCHANGED because the reader lacked permission")
	}
	if present || gen != 0 {
		t.Errorf("an unreadable marker must not also claim a generation: present=%v gen=%d", present, gen)
	}
	// The message must name the file, because the operator's fix is a chmod on that path.
	if !strings.Contains(err.Error(), p) {
		t.Errorf("the error does not name the marker path: %v", err)
	}
}

func TestAMalformedMarkerIsAnErrorAndNotAbsence(t *testing.T) {
	for _, body := range []string{
		`{this is not json`,
		`{"restore_generation": -3}`,
	} {
		p := filepath.Join(t.TempDir(), "marker.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv(EnvMarkerPath, p)
		if _, present, err := ReadRestoreMarker(); err == nil {
			t.Errorf("a marker containing %q was accepted as absence (present=%v); a file that exists and "+
				"cannot be understood is not a site that was never restored", body, present)
		}
	}
}

// The restore tool must write the marker with a mode edged can read; asserted against the shell script itself so the
// two cannot drift.
func TestTheRestoreToolWritesAMarkerItsReaderCanRead(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "scripts", "stayconnect-financial-restore.sh"))
	if err != nil {
		t.Skipf("restore tool not readable from here: %v", err)
	}
	src := string(b)
	if strings.Contains(src, `chmod 0600 "$TMP"`) {
		t.Error("the restore tool writes the marker 0600 root-owned again. edged reads it as an " +
			"unprivileged account and will get EACCES, which is how a real restore on PRE-LIVE produced " +
			"outcome=UNCHANGED with no hold and no record.")
	}
	if !strings.Contains(src, `chmod 0644 "$TMP"`) {
		t.Error("the restore tool no longer sets an explicit mode on the marker it writes; the umask then " +
			"decides whether restore detection works")
	}
}
