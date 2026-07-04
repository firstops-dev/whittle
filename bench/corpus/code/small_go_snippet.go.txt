package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

type CustomerCache struct {
	mu      sync.RWMutex
	data    map[string]*Customer
	ttl     time.Duration
	logger  *zap.Logger
	cleanup *time.Ticker
}

func NewCustomerCache(ttl time.Duration, logger *zap.Logger) *CustomerCache {
	cache := &CustomerCache{
		data:   make(map[string]*Customer),
		ttl:    ttl,
		logger: logger,
	}

	go cache.runCleanup()
	return cache
}

func (c *CustomerCache) Get(ctx context.Context, id string) (*Customer, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	customer, ok := c.data[id]
	if !ok {
		return nil, fmt.Errorf("customer not found: %s", id)
	}

	if time.Now().After(customer.ExpiresAt) {
		return nil, fmt.Errorf("cache entry expired: %s", id)
	}

	return customer, nil
}

func (c *CustomerCache) Set(ctx context.Context, customer *Customer) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	customer.ExpiresAt = time.Now().Add(c.ttl)
	c.data[customer.ID] = customer

	c.logger.Info("cached customer",
		zap.String("customer_id", customer.ID),
		zap.Time("expires_at", customer.ExpiresAt),
	)

	return nil
}

func (c *CustomerCache) runCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		count := 0

		for id, customer := range c.data {
			if now.After(customer.ExpiresAt) {
				delete(c.data, id)
				count++
			}
		}

		c.logger.Info("cache cleanup complete",
			zap.Int("entries_removed", count),
			zap.Int("total_entries", len(c.data)),
		)
		c.mu.Unlock()
	}
}

type Customer struct {
	ID        string
	Name      string
	Email     string
	ExpiresAt time.Time
}
