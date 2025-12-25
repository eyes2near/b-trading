// internal/derivative/engine.go

package derivative

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/eyes2near/b-trading/internal/binance"
	"github.com/eyes2near/b-trading/internal/database"
	"github.com/eyes2near/b-trading/internal/models"
	"github.com/eyes2near/b-trading/internal/notify"
)

// Engine 衍生订单引擎接口
type Engine interface {
	// RefreshRules 从数据库刷新规则缓存
	RefreshRules(ctx context.Context) error

	// GenerateDerivativeOrder 根据主订单成交生成衍生订单
	GenerateDerivativeOrder(ctx context.Context, params FillParams) (*DerivativeOrderParams, error)

	// ValidateRule 验证规则配置是否有效
	ValidateRule(rule *models.DerivativeRule) error

	// CheckConflicts 检查规则冲突
	CheckConflicts(ctx context.Context) ([]notify.RuleConflict, error)

	// CheckRuleExists 检查是否存在匹配的规则（不执行，仅检查）
	CheckRuleExists(ctx context.Context, order *models.Order) (bool, *models.DerivativeRule)

	// StartAutoRefresh 启动自动刷新
	StartAutoRefresh(ctx context.Context, interval time.Duration)
}

type engine struct {
	cache           *RuleCache
	matcher         *Matcher
	resolver        *SymbolResolver
	evaluator       *Evaluator
	formatter       *Formatter
	conflictChecker *ConflictChecker
	notifier        *notify.Notifier
	binanceClient   binance.Client
	ruleRepo        database.DerivativeRuleRepository
}

// NewEngine 创建衍生订单引擎
func NewEngine(
	binanceClient binance.Client,
	ruleRepo database.DerivativeRuleRepository,
	notifier *notify.Notifier,
) Engine {
	cache := NewRuleCache(ruleRepo)

	return &engine{
		cache:           cache,
		matcher:         NewMatcher(),
		resolver:        NewSymbolResolver(binanceClient),
		evaluator:       NewEvaluator(),
		formatter:       NewFormatter(),
		conflictChecker: NewConflictChecker(),
		notifier:        notifier,
		binanceClient:   binanceClient,
		ruleRepo:        ruleRepo,
	}
}

func (e *engine) RefreshRules(ctx context.Context) error {
	if err := e.cache.Refresh(ctx); err != nil {
		return err
	}

	// 刷新后检查冲突
	conflicts, _ := e.CheckConflicts(ctx)
	if len(conflicts) > 0 {
		e.notifier.NotifyRuleConflict(conflicts)
	}

	return nil
}

func (e *engine) StartAutoRefresh(ctx context.Context, interval time.Duration) {
	e.cache.StartAutoRefresh(ctx, interval)

	// 初始检查冲突
	conflicts, _ := e.CheckConflicts(ctx)
	if len(conflicts) > 0 {
		e.notifier.NotifyRuleConflict(conflicts)
	}
}

// CheckRuleExists 检查是否存在匹配的规则
func (e *engine) CheckRuleExists(ctx context.Context, order *models.Order) (bool, *models.DerivativeRule) {
	rules := e.cache.GetRules()
	rule := e.matcher.MatchFirst(rules, order)
	if rule == nil {
		return false, nil
	}
	return true, rule
}

func (e *engine) GenerateDerivativeOrder(ctx context.Context, params FillParams) (*DerivativeOrderParams, error) {
	order := params.PrimaryOrder

	// 1. 匹配规则（只返回第一条）
	rules := e.cache.GetRules()
	rule := e.matcher.MatchFirst(rules, order)
	if rule == nil {
		log.Printf("No matching derivative rule for order %d (market=%s, symbol=%s)",
			order.ID, order.MarketType, order.FullSymbol)
		return nil, nil
	}

	log.Printf("Matched derivative rule [%s] for order %d", rule.Name, order.ID)

	// 2. 解析目标 symbol
	resolved, err := e.resolver.Resolve(ctx, rule.DerivativeSymbol, order)
	if err != nil {
		errMsg := fmt.Sprintf("failed to resolve symbol: %v", err)
		e.notifier.NotifyDerivativeFailure(order.ID, rule.DerivativeSymbol, rule.Name, "解析Symbol", errMsg)
		e.logExecution(ctx, rule, order, nil, "", "", err)
		return nil, fmt.Errorf("failed to resolve symbol: %w", err)
	}

	// 3. 获取市场数据，构建变量上下文
	evalCtx, err := e.buildEvaluationContext(ctx, params, rule, resolved)
	if err != nil {
		errMsg := fmt.Sprintf("failed to build evaluation context: %v", err)
		e.notifier.NotifyDerivativeFailure(order.ID, resolved.Symbol, rule.Name, "构建上下文", errMsg)
		e.logExecution(ctx, rule, order, nil, "", "", err)
		return nil, fmt.Errorf("failed to build evaluation context: %w", err)
	}

	// 4. 求值价格表达式
	priceFloat, err := e.evaluator.Evaluate(rule.PriceExpression, evalCtx)
	if err != nil {
		e.notifier.NotifyDerivativeFailure(order.ID, resolved.Symbol, rule.Name, "价格表达式求值", err.Error())
		e.logExecution(ctx, rule, order, evalCtx, "", "", err)
		return nil, fmt.Errorf("failed to evaluate price expression: %w", err)
	}

	// 5. 求值数量表达式
	qtyFloat, err := e.evaluator.Evaluate(rule.QuantityExpression, evalCtx)
	if err != nil {
		e.notifier.NotifyDerivativeFailure(order.ID, resolved.Symbol, rule.Name, "数量表达式求值", err.Error())
		e.logExecution(ctx, rule, order, evalCtx, "", "", err)
		return nil, fmt.Errorf("failed to evaluate quantity expression: %w", err)
	}

	// 6. 格式化精度
	price := e.formatter.FormatPrice(priceFloat)
	quantity := e.formatter.FormatQuantity(qtyFloat, rule.DerivativeMarket)

	// 7. 计算方向
	direction := e.resolveDirection(rule.DerivativeDirection, order.Direction)
	// 检查方向是否有效
	if direction == "" {
		log.Printf("Skipping derivative order: primary direction %s not applicable for rule direction %s", order.Direction, rule.DerivativeDirection)
		return nil, nil
	}

	// 8. 归一化市场类型
	market := normalizeMarketType(rule.DerivativeMarket)

	// 9. 记录执行日志
	logID := e.logExecution(ctx, rule, order, evalCtx, price, quantity, nil)

	// 10. 构建结果
	result := &DerivativeOrderParams{
		RuleID:            rule.ID,
		RuleName:          rule.Name,
		Market:            market,
		Symbol:            resolved.Symbol,
		SymbolType:        resolved.SymbolType,
		Direction:         direction,
		OrderType:         models.OrderType(rule.DerivativeOrderType),
		Price:             price,
		Quantity:          quantity,
		EvaluationContext: e.contextToStringMap(evalCtx),
		LogID:             logID,
	}

	log.Printf("Generated derivative order: symbol=%s, direction=%s, price=%s, qty=%s (rule=%s)",
		result.Symbol, result.Direction, result.Price, result.Quantity, result.RuleName)

	return result, nil
}

// normalizeMarketType 归一化市场类型为标准 models.MarketType
func normalizeMarketType(market string) models.MarketType {
	m := strings.TrimSpace(strings.ToLower(market))
	m = strings.ReplaceAll(m, "-", "")

	switch m {
	case "spot":
		return models.MarketTypeSpot // "spot"
	case "coinm":
		return models.MarketTypeCoinM // "coin-m"
	default:
		return models.MarketType(market)
	}
}

func (e *engine) ValidateRule(rule *models.DerivativeRule) error {
	// 验证市场类型
	normalizedPrimary := normalizeMarket(rule.PrimaryMarket)
	if normalizedPrimary != "spot" && normalizedPrimary != "coinm" {
		return fmt.Errorf("invalid primary_market: %s", rule.PrimaryMarket)
	}
	normalizedDerivative := normalizeMarket(rule.DerivativeMarket)
	if normalizedDerivative != "spot" && normalizedDerivative != "coinm" {
		return fmt.Errorf("invalid derivative_market: %s", rule.DerivativeMarket)
	}

	// 自动归一化存储
	rule.PrimaryMarket = normalizedPrimary
	rule.DerivativeMarket = normalizedDerivative

	// 验证 primary_direction
	if rule.PrimaryDirection != "" && rule.PrimaryDirection != "*" {
		validDirections := map[string]bool{
			"long": true, "short": true, "close_long": true, "close_short": true,
		}
		directions := strings.Split(rule.PrimaryDirection, ",")
		for _, d := range directions {
			d = strings.TrimSpace(strings.ToLower(d))
			if d == "" {
				continue
			}
			if !validDirections[d] {
				return fmt.Errorf("invalid primary_direction: %s", d)
			}
		}
	}

	// 验证 derivative_direction
	validDirections := map[string]bool{
		"opposite": true, "same": true, "rollover": true,
		"long": true, "short": true, "close_long": true, "close_short": true,
	}
	if !validDirections[rule.DerivativeDirection] {
		return fmt.Errorf("invalid derivative_direction: %s", rule.DerivativeDirection)
	}

	// 验证订单类型
	if rule.DerivativeOrderType != "limit" && rule.DerivativeOrderType != "market" {
		return fmt.Errorf("invalid derivative_order_type: %s", rule.DerivativeOrderType)
	}

	// 验证表达式语法
	if err := e.evaluator.ValidateWithVariables(rule.PriceExpression, nil); err != nil {
		return fmt.Errorf("invalid price_expression: %w", err)
	}
	if err := e.evaluator.ValidateWithVariables(rule.QuantityExpression, nil); err != nil {
		return fmt.Errorf("invalid quantity_expression: %w", err)
	}

	return nil
}

// normalizeMarket 归一化市场类型
func normalizeMarket(market string) string {
	m := strings.ToLower(market)
	m = strings.ReplaceAll(m, "-", "")
	return m
}

func (e *engine) CheckConflicts(ctx context.Context) ([]notify.RuleConflict, error) {
	rules := e.cache.GetRules()
	return e.conflictChecker.CheckConflicts(rules), nil
}

// buildEvaluationContext 构建表达式求值上下文
func (e *engine) buildEvaluationContext(
	ctx context.Context,
	params FillParams,
	rule *models.DerivativeRule,
	resolved *ResolveResult,
) (map[string]interface{}, error) {
	order := params.PrimaryOrder

	// 解析输入参数
	fillPrice := ParseFloat(params.FillPrice)
	delta := ParseFloat(params.Delta)
	cumQty := ParseFloat(params.CumulativeQty)

	// 获取主订单标的当前价格
	var pPrice float64
	if order.MarketType == models.MarketTypeSpot {
		resp, err := e.binanceClient.SpotGetPrice(ctx, order.FullSymbol)
		if err != nil {
			return nil, fmt.Errorf("failed to get primary price: %w", err)
		}
		pPrice = ParseFloat(resp.Price)
	} else {
		resp, err := e.binanceClient.CoinMGetPrice(ctx, order.FullSymbol)
		if err != nil {
			return nil, fmt.Errorf("failed to get primary price: %w", err)
		}
		pPrice = ParseFloat(resp.Price)
	}

	// 获取衍生品标的当前价格
	var dPrice float64
	if rule.DerivativeMarket == "spot" {
		resp, err := e.binanceClient.SpotGetPrice(ctx, resolved.Symbol)
		if err != nil {
			return nil, fmt.Errorf("failed to get derivative price: %w", err)
		}
		dPrice = ParseFloat(resp.Price)
	} else {
		resp, err := e.binanceClient.CoinMGetPrice(ctx, resolved.Symbol)
		if err != nil {
			return nil, fmt.Errorf("failed to get derivative price: %w", err)
		}
		dPrice = ParseFloat(resp.Price)
	}

	// 计算价值
	var deltaValue float64
	var deltaContracts float64
	contractSize := GetContractSize(resolved.Base)

	if order.MarketType == models.MarketTypeSpot {
		// 现货：delta 是币数量
		deltaValue = delta * fillPrice
		deltaContracts = deltaValue / contractSize
	} else {
		// 合约：delta 是合约张数
		deltaContracts = delta
		deltaValue = delta * contractSize
	}

	return map[string]interface{}{
		"d_price":         dPrice,
		"p_price":         pPrice,
		"fill_price":      fillPrice,
		"delta":           delta,
		"delta_value":     deltaValue,
		"delta_contracts": deltaContracts,
		"cum_qty":         cumQty,
		"contract_size":   contractSize,
	}, nil
}

// resolveDirection 解析衍生订单方向
func (e *engine) resolveDirection(directionRule string, primaryDirection models.Direction) models.Direction {
	switch directionRule {
	case "opposite":
		// 反向对冲：多↔空互换
		switch primaryDirection {
		case models.DirectionLong:
			return models.DirectionShort
		case models.DirectionShort:
			return models.DirectionLong
		case models.DirectionCloseLong:
			return models.DirectionCloseShort
		case models.DirectionCloseShort:
			return models.DirectionCloseLong
		}
	case "rollover":
		// 滚仓：平仓↔开仓互换（保持多空方向一致）
		switch primaryDirection {
		case models.DirectionLong:
			return models.DirectionCloseLong // 开多 → 平多
		case models.DirectionShort:
			return models.DirectionCloseShort // 开空 → 平空
		case models.DirectionCloseLong:
			return models.DirectionLong // 平多 → 开多
		case models.DirectionCloseShort:
			return models.DirectionShort // 平空 → 开空
		}
	case "same":
		return primaryDirection
	case "long":
		return models.DirectionLong
	case "short":
		return models.DirectionShort
	case "close_long":
		return models.DirectionCloseLong
	case "close_short":
		return models.DirectionCloseShort
	}
	return "" // 无效配置
}

// logExecution 记录规则执行日志
func (e *engine) logExecution(
	ctx context.Context,
	rule *models.DerivativeRule,
	order *models.Order,
	evalCtx map[string]interface{},
	priceResult, qtyResult string,
	execErr error,
) uint {
	var evalCtxJSON string
	if evalCtx != nil {
		b, _ := json.Marshal(evalCtx)
		evalCtxJSON = string(b)
	}

	var errMsg string
	if execErr != nil {
		errMsg = execErr.Error()
	}

	logEntry := &models.DerivativeRuleLog{
		RuleID:             rule.ID,
		RuleName:           rule.Name,
		FlowID:             order.FlowID,
		PrimaryOrderID:     order.ID,
		EvaluationContext:  evalCtxJSON,
		PriceExpression:    rule.PriceExpression,
		PriceResult:        priceResult,
		QuantityExpression: rule.QuantityExpression,
		QuantityResult:     qtyResult,
		Success:            execErr == nil,
		ErrorMessage:       errMsg,
		ExecutedAt:         time.Now(),
	}

	if err := e.ruleRepo.CreateLog(ctx, logEntry); err != nil {
		log.Printf("Failed to create derivative rule log: %v", err)
	}

	return logEntry.ID
}

// contextToStringMap 转换上下文为字符串 map（用于审计）
func (e *engine) contextToStringMap(ctx map[string]interface{}) map[string]string {
	result := make(map[string]string)
	for k, v := range ctx {
		result[k] = fmt.Sprintf("%v", v)
	}
	return result
}
