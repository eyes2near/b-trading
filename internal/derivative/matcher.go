// internal/derivative/matcher.go

package derivative

import (
	"strings"

	"github.com/eyes2near/b-trading/internal/models"
)

// Matcher 规则匹配器
type Matcher struct{}

// NewMatcher 创建匹配器
func NewMatcher() *Matcher {
	return &Matcher{}
}

// MatchFirst 匹配第一条符合条件的规则
func (m *Matcher) MatchFirst(rules []models.DerivativeRule, order *models.Order) *models.DerivativeRule {
	for i := range rules {
		if m.RuleMatches(&rules[i], order) {
			return &rules[i]
		}
	}
	return nil
}

// RuleMatches 检查规则是否匹配订单
func (m *Matcher) RuleMatches(rule *models.DerivativeRule, order *models.Order) bool {
	// 1. 市场类型匹配
	if !m.marketMatches(rule.PrimaryMarket, string(order.MarketType)) {
		return false
	}

	// 2. Direction 匹配
	if !m.DirectionMatches(rule.PrimaryDirection, order.Direction) {
		return false
	}

	// 3. Symbol 匹配
	return m.SymbolMatches(rule.PrimarySymbol, order)
}

// marketMatches 检查市场类型是否匹配（归一化处理）
func (m *Matcher) marketMatches(ruleMarket, orderMarket string) bool {
	// 归一化：统一转小写，移除连字符
	normalize := func(s string) string {
		s = strings.ToLower(s)
		s = strings.ReplaceAll(s, "-", "")
		return s
	}

	return normalize(ruleMarket) == normalize(orderMarket)
}

// DirectionMatches 检查方向是否匹配
func (m *Matcher) DirectionMatches(pattern string, orderDirection models.Direction) bool {
	// 空值或 * 匹配所有
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return true
	}

	orderDir := strings.ToLower(string(orderDirection))

	// 检查是否包含逗号（多值匹配）
	if strings.Contains(pattern, ",") {
		directions := strings.Split(pattern, ",")
		for _, d := range directions {
			d = strings.TrimSpace(strings.ToLower(d))
			if d == orderDir {
				return true
			}
		}
		return false
	}

	// 单值精确匹配
	return strings.ToLower(pattern) == orderDir
}

// SymbolMatches 检查 symbol 是否匹配
func (m *Matcher) SymbolMatches(pattern string, order *models.Order) bool {
	orderSymbol := strings.ToUpper(order.FullSymbol)
	pattern = strings.ToUpper(pattern)

	// 精确匹配
	if pattern == orderSymbol {
		return true
	}

	// 通配符 * 匹配所有
	if pattern == "*" {
		return true
	}

	// 后缀通配符：*USDT
	if strings.HasPrefix(pattern, "*") {
		suffix := pattern[1:]
		return strings.HasSuffix(orderSymbol, suffix)
	}

	// 前缀通配符：BTC*
	if strings.HasSuffix(pattern, "*") {
		prefix := pattern[:len(pattern)-1]
		return strings.HasPrefix(orderSymbol, prefix)
	}

	// coinm 语义匹配：btc_current / btc_next
	if strings.Contains(pattern, "_") {
		parts := strings.Split(strings.ToLower(pattern), "_")
		if len(parts) == 2 {
			base := strings.ToUpper(parts[0])
			// contractType := parts[1] // current / next

			orderBase := ExtractBaseCurrency(orderSymbol)
			if orderBase != base {
				return false
			}

			// 对于 coinm 订单，检查其 symbol_type 或 contract_type
			if order.MarketType == models.MarketTypeCoinM {
				// 简化处理：只匹配 base 货币
				// 如果需要精确匹配 current/next，可以扩展
				return true
			}
		}
	}

	return false
}
