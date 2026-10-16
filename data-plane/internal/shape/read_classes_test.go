package shape

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// B-18: a FAILED counter read is not "no classes". Treating it so made netd re-create every class (a new epoch,
// a restarted series, the bytes since the last reading lost) and made acctd read every counter as zero.
func TestReadClassesFailureIsAnErrorNotAnEmptyInventory(t *testing.T) {
	c := &Client{TCPath: "tc", IPPath: "ip", infraReady: map[string]bool{}, bridgeMu: map[string]*sync.Mutex{},
		exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte("RTNETLINK answers: Resource temporarily unavailable"), errors.New("exit status 2")
		}}
	got, err := c.ReadClasses(context.Background(), "br-guest")
	if err == nil {
		t.Fatalf("a failed tc read returned %v and no error; it must be reported, never read as empty", got)
	}
}

// The one failure that IS an empty inventory: the device does not exist (an IFB never created).
func TestReadClassesAbsentDeviceIsEmpty(t *testing.T) {
	c := &Client{TCPath: "tc", IPPath: "ip", infraReady: map[string]bool{}, bridgeMu: map[string]*sync.Mutex{},
		exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte(`Cannot find device "ifb-guest"`), errors.New("exit status 1")
		}}
	got, err := c.ReadClasses(context.Background(), "ifb-guest")
	if err != nil || len(got) != 0 {
		t.Fatalf("absent device: got %v, %v; want an empty inventory", got, err)
	}
}
