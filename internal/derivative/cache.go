// internal/derivative/cache.go

package derivative

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/eyes2near/b-trading/internal/database"
	"github.com/eyes2near/b-trading/internal/models"
)

// RuleCache 规则缓存（支持热更新）
type RuleCache struct {
	mu          sync.RWMutex
	rules       []models.DerivativeRule
	lastRefresh time.Time
	repo        database.DerivativeRuleRepository
}

// NewRuleCache 创建规则缓存
func NewRuleCache(repo database.DerivativeRuleRepository) *RuleCache {
	return &RuleCache{repo: repo}
}

// Refresh 从数据库刷新缓存
func (c *RuleCache) Refresh(ctx context.Context) error {
	rules, err := c.repo.ListEnabled(ctx)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.rules = rules
	c.lastRefresh = time.Now()
	c.mu.Unlock()

	log.Printf("Derivative rule cache refreshed: %d rules loaded", len(rules))
	return nil
}

// GetRules 获取当前缓存的规则（返回副本）
func (c *RuleCache) GetRules() []models.DerivativeRule {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]models.DerivativeRule, len(c.rules))
	copy(result, c.rules)
	return result
}

// GetLastRefreshTime 获取上次刷新时间
func (c *RuleCache) GetLastRefreshTime() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastRefresh
}

// StartAutoRefresh 启动自动刷新
func (c *RuleCache) StartAutoRefresh(ctx context.Context, interval time.Duration) {
	// 立即执行一次刷新
	if err := c.Refresh(ctx); err != nil {
		log.Printf("Initial rule cache refresh failed: %v", err)
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Println("Rule cache auto-refresh stopped")
				return
			case <-ticker.C:
				if err := c.Refresh(context.Background()); err != nil {
					log.Printf("Failed to refresh rule cache: %v", err)
				}
			}
		}
	}()
}
