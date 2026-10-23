package shape

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// A failed counter read is not "no classes": that made netd re-create every class and acctd read zeros.
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
