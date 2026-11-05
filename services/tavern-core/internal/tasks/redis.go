package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Redis stores tasks as JSON in a hash and events on the `events` stream
// (docs/architecture.md: streams `inbox:<agent>`, `bar`, `events`).
type Redis struct {
	C      *redis.Client
	Prefix string // key prefix, default "tavern"
}

func (r *Redis) key(s string) string {
	p := r.Prefix
	if p == "" {
		p = "tavern"
	}
	return p + ":" + s
}

func (r *Redis) Get(ctx context.Context, id string) (*Task, error) {
	raw, err := r.C.HGet(ctx, r.key("tasks"), id).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var t Task
	return &t, json.Unmarshal(raw, &t)
}

func (r *Redis) Put(ctx context.Context, t *Task) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return r.C.HSet(ctx, r.key("tasks"), t.ID, raw).Err()
}

func (r *Redis) List(ctx context.Context) ([]*Task, error) {
	all, err := r.C.HGetAll(ctx, r.key("tasks")).Result()
	if err != nil {
		return nil, err
	}
	out := make([]*Task, 0, len(all))
	for _, raw := range all {
		var t Task
		if err := json.Unmarshal([]byte(raw), &t); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, nil
}

func (r *Redis) NextID(ctx context.Context) (string, error) {
	n, err := r.C.Incr(ctx, r.key("tasks:seq")).Result()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("T-%d", n), nil
}

// Emit appends to the events stream, capped so it never grows without bound.
func (r *Redis) Emit(ctx context.Context, e Event) error {
	return r.C.XAdd(ctx, &redis.XAddArgs{
		Stream: "events",
		MaxLen: 100000,
		Approx: true,
		Values: map[string]any{"type": e.Type, "task": e.Task, "agent": e.Agent, "to": e.To, "at": e.At.UTC().Format("2006-01-02T15:04:05Z")},
	}).Err()
}
