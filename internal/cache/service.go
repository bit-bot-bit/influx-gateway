package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"influx-bouncer/internal/config"
	"influx-bouncer/internal/metrics"
)

type Service struct {
	client  *redis.Client
	cfg     config.Config
	metrics *metrics.Metrics
}

func NewService(cfg config.Config, m *metrics.Metrics) *Service {
	client := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddr,
	})

	// Enable OpenTelemetry tracing for Redis
	if err := redisotel.InstrumentTracing(client); err != nil {
		// Log error but continue? Or just panic?
		// Since we don't have a logger passed here, we might just ignore or print to stdout.
		// ideally we should pass a logger.
		// For now, let's assume it works or fails silently (it returns error).
		// We can print it.
		fmt.Printf("failed to instrument redis tracing: %v\n", err)
	}

	return &Service{
		client:  client,
		cfg:     cfg,
		metrics: m,
	}
}

func (s *Service) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *Service) Get(ctx context.Context, key string) ([]byte, error) {
	start := time.Now()
	val, err := s.client.Get(ctx, key).Bytes()
	s.metrics.ObserveRedis("get", err == nil || errors.Is(err, redis.Nil), time.Since(start))
	return val, err
}

func (s *Service) Set(ctx context.Context, key string, val []byte) error {
	start := time.Now()
	err := s.client.Set(ctx, key, val, s.jitteredTTL()).Err()
	s.metrics.ObserveRedis("set", err == nil, time.Since(start))
	return err
}

func (s *Service) AcquireLock(ctx context.Context, key string) (bool, string, error) {
	lockKey := key + ":lock"
	token := fmt.Sprintf("%d", time.Now().UnixNano())
	start := time.Now()
	locked, err := s.client.SetNX(ctx, lockKey, token, s.cfg.CacheLockTTL).Result()
	s.metrics.ObserveRedis("setnx", err == nil, time.Since(start))
	return locked, lockKey, err
}

func (s *Service) ReleaseLock(ctx context.Context, lockKey string) error {
	return s.client.Del(ctx, lockKey).Err()
}

func (s *Service) WaitForLock(ctx context.Context, key string) ([]byte, error) {
	deadline := time.Now().Add(s.cfg.CacheLockWait)
	for time.Now().Before(deadline) {
		start := time.Now()
		cached, err := s.client.Get(ctx, key).Bytes()
		s.metrics.ObserveRedis("get_wait", err == nil || errors.Is(err, redis.Nil), time.Since(start))
		if err == nil {
			return cached, nil
		}
		if !errors.Is(err, redis.Nil) {
			return nil, err
		}
		time.Sleep(30 * time.Millisecond)
	}
	return nil, redis.Nil
}

func Key(org, query string) string {
	sum := sha256.Sum256([]byte(org + "\n" + query))
	return fmt.Sprintf("igw:v1:%s:%s", org, hex.EncodeToString(sum[:]))
}

func (s *Service) jitteredTTL() time.Duration {
	ttl := s.cfg.CacheTTL
	jitter := s.cfg.CacheTTLJitter
	if jitter <= 0 {
		return ttl
	}
	delta := time.Duration(rand.Int63n(int64(jitter)*2+1)) - jitter
	ttl += delta
	if ttl < time.Second {
		return time.Second
	}
	return ttl
}
