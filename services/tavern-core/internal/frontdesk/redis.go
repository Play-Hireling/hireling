package frontdesk

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
)

// RedisKey holds the desk state as one JSON document.
const RedisKey = "tavern:frontdesk"

// Redis persists the desk in the tavern's Redis (AOF on in the Compose file).
type Redis struct{ C *redis.Client }

func (p *Redis) Load(ctx context.Context) ([]byte, error) {
	b, err := p.C.Get(ctx, RedisKey).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	return b, err
}

func (p *Redis) Save(ctx context.Context, data []byte) error {
	return p.C.Set(context.WithoutCancel(ctx), RedisKey, data, 0).Err()
}
