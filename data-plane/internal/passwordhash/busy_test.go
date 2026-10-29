package passwordhash

import (
	"errors"
	"testing"
	"time"
)

// AD-35: when every slot stays taken a check gives up after MaxSlotWait with ErrBusy, instead of queueing for as long as
// the burst lasts; ErrBusy is neither a match nor a wrong password, and a free slot is still taken at once.
func TestACheckWaitsOnlyBrieflyForASlotThenReportsBusy(t *testing.T) {
	h, err := Hash("a password")
	if err != nil {
		t.Fatal(err)
	}
	prev := MaxSlotWait
	MaxSlotWait = 50 * time.Millisecond
	defer func() { MaxSlotWait = prev }()

	n := cap(slots)
	for i := 0; i < n; i++ {
		slots <- struct{}{}
	}
	start := time.Now()
	ok, err := Verify("a password", h)
	waited := time.Since(start)
	if !errors.Is(err, ErrBusy) || ok {
		t.Fatalf("ok=%v err=%v, want ErrBusy and no match", ok, err)
	}
	if waited < 40*time.Millisecond || waited > 2*time.Second {
		t.Fatalf("gave up after %v, want about 50ms", waited)
	}
	if _, err := Hash("another"); !errors.Is(err, ErrBusy) {
		t.Fatalf("Hash with no free slot: %v, want ErrBusy", err)
	}
	for i := 0; i < n; i++ {
		<-slots
	}
	if ok, err := Verify("a password", h); err != nil || !ok {
		t.Fatalf("with free slots: ok=%v err=%v", ok, err)
	}
	// BurnVerify never panics or blocks the caller beyond the bound.
	for i := 0; i < n; i++ {
		slots <- struct{}{}
	}
	BurnVerify("x")
	for i := 0; i < n; i++ {
		<-slots
	}
}
