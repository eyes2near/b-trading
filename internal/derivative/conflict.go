// internal/derivative/conflict.go

package derivative

import (
	"fmt"
	"strings"

	"github.com/eyes2near/b-trading/internal/models"
	"github.com/eyes2near/b-trading/internal/notify"
)

// ConflictChecker 规则冲突检测器
type ConflictChecker struct{}

// NewConflictChecker 创建冲突检测器
func NewConflictChecker() *ConflictChecker {
	return &ConflictChecker{}
}

// CheckConflicts 检查规则冲突
func (c *ConflictChecker) CheckConflicts(rules []models.DerivativeRule) []notify.RuleConflict {
	var conflicts []notify.RuleConflict

	for i := 0; i < len(rules); i++ {
		for j := i + 1; j < len(rules); j++ {
			r1, r2 := rules[i], rules[j]

			// 只检查都启用的规则
			if !r1.Enabled || !r2.Enabled {
				continue
			}

			// 检查完全相同的匹配条件
			if r1.PrimaryMarket == r2.PrimaryMarket &&
				strings.EqualFold(r1.PrimarySymbol, r2.PrimarySymbol) &&
				c.directionsOverlap(r1.PrimaryDirection, r2.PrimaryDirection) {

				// 确定哪个优先级更低
				lowerPriorityRule := r2.Name
				if r1.Priority > r2.Priority {
					lowerPriorityRule = r1.Name
				}

				conflicts = append(conflicts, notify.RuleConflict{
					Rule1ID:      r1.ID,
					Rule1Name:    r1.Name,
					Rule2ID:      r2.ID,
					Rule2Name:    r2.Name,
					ConflictType: "same_match_condition",
					Description: fmt.Sprintf(
						"规则 [%s](优先级%d) 和 [%s](优先级%d) 匹配条件重叠 (market=%s, symbol=%s, direction overlap)，[%s] 可能不会执行",
						r1.Name, r1.Priority, r2.Name, r2.Priority,
						r1.PrimaryMarket, r1.PrimarySymbol,
						lowerPriorityRule,
					),
				})
			}

			// 检查通配符重叠
			if r1.PrimaryMarket == r2.PrimaryMarket {
				if c.patternsOverlap(r1.PrimarySymbol, r2.PrimarySymbol) {
					conflicts = append(conflicts, notify.RuleConflict{
						Rule1ID:      r1.ID,
						Rule1Name:    r1.Name,
						Rule2ID:      r2.ID,
						Rule2Name:    r2.Name,
						ConflictType: "overlapping_symbol",
						Description: fmt.Sprintf(
							"规则 [%s](%s) 和 [%s](%s) 的 symbol 匹配范围重叠，请检查优先级设置",
							r1.Name, r1.PrimarySymbol, r2.Name, r2.PrimarySymbol,
						),
					})
				}
			}
		}
	}

	return conflicts
}

// directionsOverlap 检查两个方向配置是否有重叠
func (c *ConflictChecker) directionsOverlap(d1, d2 string) bool {
	// 空值或 * 表示匹配所有
	d1 = strings.TrimSpace(d1)
	d2 = strings.TrimSpace(d2)

	if d1 == "" || d1 == "*" || d2 == "" || d2 == "*" {
		return true
	}

	// 解析为集合
	set1 := c.parseDirections(d1)
	set2 := c.parseDirections(d2)

	// 检查交集
	for dir := range set1 {
		if set2[dir] {
			return true
		}
	}

	return false
}

// parseDirections 解析方向配置为集合
func (c *ConflictChecker) parseDirections(pattern string) map[string]bool {
	result := make(map[string]bool)

	if pattern == "" || pattern == "*" {
		// 通配符匹配所有
		result["long"] = true
		result["short"] = true
		result["close_long"] = true
		result["close_short"] = true
		return result
	}

	for _, d := range strings.Split(pattern, ",") {
		d = strings.TrimSpace(strings.ToLower(d))
		if d != "" {
			result[d] = true
		}
	}

	return result
}

// patternsOverlap 检查两个 pattern 是否重叠
func (c *ConflictChecker) patternsOverlap(p1, p2 string) bool {
	p1 = strings.ToUpper(p1)
	p2 = strings.ToUpper(p2)

	// 其中一个是 * 通配符
	if p1 == "*" || p2 == "*" {
		return true
	}

	// 都是后缀通配符：*USDT vs *USDT
	if strings.HasPrefix(p1, "*") && strings.HasPrefix(p2, "*") {
		s1, s2 := p1[1:], p2[1:]
		return strings.HasSuffix(s1, s2) || strings.HasSuffix(s2, s1)
	}

	// 都是前缀通配符：BTC* vs BTC*
	if strings.HasSuffix(p1, "*") && strings.HasSuffix(p2, "*") {
		s1, s2 := p1[:len(p1)-1], p2[:len(p2)-1]
		return strings.HasPrefix(s1, s2) || strings.HasPrefix(s2, s1)
	}

	// 一个是前缀通配符，一个是精确：BTC* vs BTCUSDT
	if strings.HasSuffix(p1, "*") && !strings.Contains(p2, "*") {
		prefix := p1[:len(p1)-1]
		return strings.HasPrefix(p2, prefix)
	}
	if strings.HasSuffix(p2, "*") && !strings.Contains(p1, "*") {
		prefix := p2[:len(p2)-1]
		return strings.HasPrefix(p1, prefix)
	}

	// 一个是后缀通配符，一个是精确：*USDT vs BTCUSDT
	if strings.HasPrefix(p1, "*") && !strings.Contains(p2, "*") {
		suffix := p1[1:]
		return strings.HasSuffix(p2, suffix)
	}
	if strings.HasPrefix(p2, "*") && !strings.Contains(p1, "*") {
		suffix := p2[1:]
		return strings.HasSuffix(p1, suffix)
	}

	return false
}
