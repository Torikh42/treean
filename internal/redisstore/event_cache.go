package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/torikh42/treean/internal/domain"
)

const (
	eventCatalogCacheKey   = "events:catalog:v1"
	eventDetailCachePrefix = "events:detail:v1:"
	defaultEventCacheTTL   = 3 * time.Second
)

// GetEventsCatalog returns cached public events on a hit, failing open (cache miss) on any error.
func (c *Client) GetEventsCatalog(ctx context.Context) ([]domain.Event, bool) {
	if !c.Enabled() {
		return nil, false
	}

	opCtx, cancel := c.withTimeout(ctx)
	defer cancel()

	raw, err := c.rdb.Get(opCtx, eventCatalogCacheKey).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			c.logger.WarnContext(opCtx, "redis event catalog cache get failed open", slog.String("error", err.Error()))
		}
		return nil, false
	}

	var events []domain.Event
	if err := json.Unmarshal(raw, &events); err != nil {
		c.logger.WarnContext(opCtx, "redis event catalog cache decode failed", slog.String("error", err.Error()))
		return nil, false
	}
	return events, true
}

// SetEventsCatalog caches public events best-effort for ttl without failing requests if Redis errors.
func (c *Client) SetEventsCatalog(ctx context.Context, events []domain.Event, ttl time.Duration) {
	if !c.Enabled() {
		return
	}
	if ttl <= 0 {
		ttl = defaultEventCacheTTL
	}
	if events == nil {
		events = []domain.Event{}
	}

	payload, err := json.Marshal(events)
	if err != nil {
		return
	}

	opCtx, cancel := c.withTimeout(ctx)
	defer cancel()
	if err := c.rdb.Set(opCtx, eventCatalogCacheKey, payload, ttl).Err(); err != nil {
		c.logger.WarnContext(opCtx, "redis event catalog cache set failed", slog.String("error", err.Error()))
	}
}

// GetEventDetail returns a cached single event detail on a hit, failing open on any error.
func (c *Client) GetEventDetail(ctx context.Context, eventID uuid.UUID) (*domain.Event, bool) {
	if !c.Enabled() || eventID == uuid.Nil {
		return nil, false
	}

	opCtx, cancel := c.withTimeout(ctx)
	defer cancel()

	key := eventDetailCacheKey(eventID)
	raw, err := c.rdb.Get(opCtx, key).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			c.logger.WarnContext(opCtx, "redis event detail cache get failed open",
				slog.String("event_id", eventID.String()),
				slog.String("error", err.Error()),
			)
		}
		return nil, false
	}

	var ev domain.Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		c.logger.WarnContext(opCtx, "redis event detail cache decode failed",
			slog.String("event_id", eventID.String()),
			slog.String("error", err.Error()),
		)
		return nil, false
	}
	return &ev, true
}

// SetEventDetail caches a single event detail best-effort for ttl.
func (c *Client) SetEventDetail(ctx context.Context, ev *domain.Event, ttl time.Duration) {
	if !c.Enabled() || ev == nil || ev.ID == uuid.Nil {
		return
	}
	if ttl <= 0 {
		ttl = defaultEventCacheTTL
	}

	payload, err := json.Marshal(ev)
	if err != nil {
		return
	}

	opCtx, cancel := c.withTimeout(ctx)
	defer cancel()
	if err := c.rdb.Set(opCtx, eventDetailCacheKey(ev.ID), payload, ttl).Err(); err != nil {
		c.logger.WarnContext(opCtx, "redis event detail cache set failed",
			slog.String("event_id", ev.ID.String()),
			slog.String("error", err.Error()),
		)
	}
}

// InvalidateEvents removes the catalog list key and any targeted event detail keys best-effort.
func (c *Client) InvalidateEvents(ctx context.Context, eventIDs ...uuid.UUID) {
	if !c.Enabled() {
		return
	}

	keys := make([]string, 0, 1+len(eventIDs))
	keys = append(keys, eventCatalogCacheKey)
	seen := make(map[uuid.UUID]struct{}, len(eventIDs))
	for _, id := range eventIDs {
		if id == uuid.Nil {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		keys = append(keys, eventDetailCacheKey(id))
	}

	opCtx, cancel := c.withTimeout(ctx)
	defer cancel()
	if err := c.rdb.Del(opCtx, keys...).Err(); err != nil {
		c.logger.WarnContext(opCtx, "redis event cache invalidation failed", slog.String("error", err.Error()))
	}
}

func eventDetailCacheKey(eventID uuid.UUID) string {
	return eventDetailCachePrefix + eventID.String()
}
