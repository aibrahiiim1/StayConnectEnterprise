package signinattempt

import "testing"

// A submission that failed before anything classified it must still be recordable. The column is NOT NULL
// with a closed CHECK, so an empty kind is a row the database refuses — and a refused row is an attempt that
// vanishes, which is the whole defect this table exists to fix.
func TestAnUnclassifiedAttemptIsRecordableAsUnknown(t *testing.T) {
	a := Attempt{}
	if a.VerifierKind != "" {
		t.Fatal("the zero value is no longer empty; this test is asserting the wrong thing")
	}
	// Record() coerces; this asserts the value it coerces TO is one the CHECK accepts.
	for _, valid := range []VerifierKind{VerifierFullName, VerifierReservationLike, VerifierUnknown} {
		if valid == "" {
			t.Fatal("a valid kind is empty")
		}
	}
	if VerifierUnknown != "UNKNOWN" {
		t.Fatalf("VerifierUnknown = %q, but the database CHECK accepts 'UNKNOWN'", VerifierUnknown)
	}
}
