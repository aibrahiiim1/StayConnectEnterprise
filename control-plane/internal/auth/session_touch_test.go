package auth

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// Touch must not write back a session deleted by a sign-out meanwhile (SET ... XX). Needs CTRLAPI_TEST_REDIS_URL (skipped without one).
func TestTouchDoesNotResurrectASignedOutSession(t *testing.T) {
	url := os.Getenv("CTRLAPI_TEST_REDIS_URL")
	if url == "" {
		t.Skip("CTRLAPI_TEST_REDIS_URL is not set: this test needs a Redis")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	r := redis.NewClient(opt)
	defer r.Close()
	ctx := context.Background()
	store := &SessionStore{R: r}

	sess, err := store.Create(ctx, Session{OperatorID: "op-touch", Email: "touch@central.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Del(ctx, key(sess.Token))
	read, err := store.Get(ctx, sess.Token)
	if err != nil || read == nil {
		t.Fatalf("get = %v %v", read, err)
	}
	read.LastSeenAt = time.Now().Add(-2 * time.Minute) // old enough to be written

	if err := store.Touch(ctx, read); err != nil {
		t.Fatal(err)
	}
	again, _ := store.Get(ctx, sess.Token)
	if again == nil || !again.LastSeenAt.After(read.LastSeenAt) {
		t.Fatalf("touch did not record activity: %+v", again)
	}
	if ttl := r.TTL(ctx, key(sess.Token)).Val(); ttl <= SessionTTL-time.Minute {
		t.Fatalf("touch did not slide the lifetime: ttl %v", ttl)
	}

	if err := store.Destroy(ctx, sess.Token); err != nil {
		t.Fatal(err)
	}
	if err := store.Touch(ctx, read); err != nil {
		t.Fatalf("touch of a deleted session = %v, want nil", err)
	}
	if n := r.Exists(ctx, key(sess.Token)).Val(); n != 0 {
		t.Fatal("touch wrote a signed-out session back")
	}
}
