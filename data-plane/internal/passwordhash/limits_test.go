package passwordhash

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// D2-H1 / P2 L7: no more than cap(slots) argon2 computations run at once, whatever the number of callers, and every result is unchanged.
func TestConcurrentHashesAreBounded(t *testing.T) {
	h, err := Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	var inFlight, peak atomic.Int32
	real := argon2Key
	argon2Key = func(pw, salt []byte, tm, m uint32, p uint8, l uint32) []byte {
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		defer inFlight.Add(-1)
		return real(pw, salt, tm, m, p, l)
	}
	defer func() { argon2Key = real }()

	const callers = 40
	var wg sync.WaitGroup
	var wrong atomic.Int32
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if ok, err := Verify("correct horse battery", h); err != nil || !ok {
					wrong.Add(1)
				}
			} else {
				BurnVerify("anything")
			}
		}(i)
	}
	wg.Wait()
	if wrong.Load() != 0 {
		t.Fatalf("%d verifications changed result under the limiter", wrong.Load())
	}
	if got, limit := peak.Load(), int32(cap(slots)); got > limit {
		t.Fatalf("%d argon2 computations ran at once, limit %d", got, limit)
	}
}

// P2 L7: the parameter string must match exactly; trailing junk is refused.
func TestVerifyRefusesTrailingParameterJunk(t *testing.T) {
	h, err := Hash("pw-for-junk-test")
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(h, "p=4$", "p=4junk$", 1)
	if _, err := Verify("pw-for-junk-test", bad); !errors.Is(err, ErrFormat) {
		t.Fatalf("trailing junk in parameters: want ErrFormat, got %v", err)
	}
}

// P2 L7: inputs over MaxPasswordBytes are not hashed.
func TestPasswordLengthCap(t *testing.T) {
	long := strings.Repeat("a", MaxPasswordBytes+1)
	if _, err := Hash(long); !errors.Is(err, ErrTooLong) {
		t.Fatalf("Hash: want ErrTooLong, got %v", err)
	}
	if ok, err := Verify(long, Dummy()); ok || err != nil {
		t.Fatalf("Verify of an over-long password: ok=%v err=%v, want no match and no error", ok, err)
	}
}
