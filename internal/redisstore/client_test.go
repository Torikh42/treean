package redisstore

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/torikh42/treean/internal/domain"
)

func TestNewClient_BlankAndInvalidURL(t *testing.T) {
	t.Parallel()

	c, err := NewClient("   ", 200*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("unexpected error for blank REDIS_URL: %v", err)
	}
	if c != nil || c.Enabled() {
		t.Fatalf("expected nil disabled client for blank URL, got %+v", c)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("closing nil client failed: %v", err)
	}

	if _, err := NewClient("not-a-redis-url://bad", 200*time.Millisecond, nil); err == nil {
		t.Fatal("expected error for malformed REDIS_URL")
	}
}

func TestClient_StrictFailOpenOnNilOrUnreachableRedis(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	id := uuid.New()

	var nilClient *Client
	allowed, retryAfter, err := nilClient.Allow(ctx, "rl:test", 5, time.Minute)
	if err != nil || !allowed || retryAfter != 0 {
		t.Fatalf("nil Allow = (%v, %v, %v), want (true, 0, nil)", allowed, retryAfter, err)
	}
	if _, ok := nilClient.GetEventsCatalog(ctx); ok {
		t.Fatal("expected cache miss on nil client catalog get")
	}
	if _, ok := nilClient.GetEventDetail(ctx, id); ok {
		t.Fatal("expected cache miss on nil client detail get")
	}
	nilClient.SetEventsCatalog(ctx, []domain.Event{{ID: id}}, time.Second)
	nilClient.SetEventDetail(ctx, &domain.Event{ID: id}, time.Second)
	nilClient.InvalidateEvents(ctx, id)

	// Point to an unroutable closed localhost port with ultra-short timeout to exercise fail-open.
	rdb := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  30 * time.Millisecond,
		ReadTimeout:  30 * time.Millisecond,
		WriteTimeout: 30 * time.Millisecond,
		MaxRetries:   0,
	})
	t.Cleanup(func() { _ = rdb.Close() })

	unreachable := NewClientFromGoRedis(rdb, 30*time.Millisecond, slog.New(slog.DiscardHandler))
	allowed, retryAfter, err = unreachable.Allow(ctx, "rl:unreachable", 1, time.Minute)
	if err != nil || !allowed || retryAfter != 0 {
		t.Fatalf("unreachable Allow = (%v, %v, %v), want fail-open (true, 0, nil)", allowed, retryAfter, err)
	}
	if _, ok := unreachable.GetEventsCatalog(ctx); ok {
		t.Fatal("expected cache miss when Redis is unreachable")
	}
	if _, ok := unreachable.GetEventDetail(ctx, id); ok {
		t.Fatal("expected detail cache miss when Redis is unreachable")
	}
	unreachable.SetEventsCatalog(ctx, []domain.Event{{ID: id}}, time.Second)
	unreachable.SetEventDetail(ctx, &domain.Event{ID: id}, time.Second)
	unreachable.InvalidateEvents(ctx, id, id, uuid.Nil)
}

func openLiveTestRedisClient(t *testing.T) *Client {
	t.Helper()
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not configured")
	}
	c, err := NewClient(redisURL, 500*time.Millisecond, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.rdb.FlushDB(t.Context()).Err(); err != nil {
		t.Fatalf("FlushDB: %v", err)
	}
	return c
}

func TestClient_LiveRedisRateLimit(t *testing.T) {
	ctx := t.Context()
	c := openLiveTestRedisClient(t)

	rlKey := "rl:live:" + uuid.NewString()
	for i := range 2 {
		allowed, retryAfter, err := c.Allow(ctx, rlKey, 2, 10*time.Second)
		if err != nil || !allowed || retryAfter != 0 {
			t.Fatalf("hit %d = (%v, %v, %v), want allowed", i+1, allowed, retryAfter, err)
		}
	}
	allowed3, retryAfter3, err := c.Allow(ctx, rlKey, 2, 10*time.Second)
	if err != nil || allowed3 || retryAfter3 < time.Second {
		t.Fatalf("hit 3 = (%v, %v, %v), want blocked with retryAfter >= 1s", allowed3, retryAfter3, err)
	}
}

func TestClient_LiveRedisEventCache(t *testing.T) {
	ctx := t.Context()
	c := openLiveTestRedisClient(t)

	evID := uuid.New()
	ev := domain.Event{ID: evID, Title: "Live Cached Event", Venue: "Hall", EventDate: time.Now().UTC()}
	c.SetEventsCatalog(ctx, []domain.Event{ev}, 5*time.Second)
	c.SetEventDetail(ctx, &ev, 5*time.Second)

	gotList, hitList := c.GetEventsCatalog(ctx)
	if !hitList || len(gotList) != 1 || gotList[0].ID != evID {
		t.Fatalf("GetEventsCatalog = (%v, %v)", gotList, hitList)
	}
	gotEv, hitEv := c.GetEventDetail(ctx, evID)
	if !hitEv || gotEv == nil || gotEv.ID != evID {
		t.Fatalf("GetEventDetail = (%v, %v)", gotEv, hitEv)
	}

	c.InvalidateEvents(ctx, evID)
	if _, hit := c.GetEventsCatalog(ctx); hit {
		t.Fatal("expected catalog cache invalidated")
	}
	if _, hit := c.GetEventDetail(ctx, evID); hit {
		t.Fatal("expected detail cache invalidated")
	}
}
