package signinattempt

import "testing"

// An unclassified submission must still be recordable: the column is NOT NULL with a closed CHECK, so an
// empty kind would be refused and the attempt would vanish.
func TestAnUnclassifiedAttemptIsRecordableAsUnknown(t *testing.T) {
	a := Attempt{}
	if a.VerifierKind != "" {
		t.Fatal("the zero value is no longer empty; this test is asserting the wrong thing")
	}
	for _, valid := range []VerifierKind{VerifierFullName, VerifierReservationLike, VerifierUnknown} {
		if valid == "" {
			t.Fatal("a valid kind is empty")
		}
	}
	if VerifierUnknown != "UNKNOWN" {
		t.Fatalf("VerifierUnknown = %q, but the database CHECK accepts 'UNKNOWN'", VerifierUnknown)
	}
}
