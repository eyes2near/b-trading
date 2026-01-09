// internal/models/flow_rule.go

package models

import (
	"encoding/json"
	"fmt"
)

// FlowDerivativeRule Flow 级别的衍生规则配置
// 存储在 TradingFlow.DerivativeRuleConfig 中（JSON 格式）
type FlowDerivativeRule struct {
	// 是否启用衍生订单
	Enabled bool `json:"enabled"`

	// 来源信息（用于审计追踪）
	SourceType   string `json:"source_type,omitempty"`   // "custom" | "template"
	TemplateID   uint   `json:"template_id,omitempty"`   // 如果基于模板创建
	TemplateName string `json:"template_name,omitempty"` // 模板名称快照

	// 衍生订单配置
	DerivativeMarket    string `json:"derivative_market"`     // spot | coinm
	DerivativeSymbol    string `json:"derivative_symbol"`     // btc_current | {base}USDT
	DerivativeDirection string `json:"derivative_direction"`  // opposite | same | rollover | long | short
	DerivativeOrderType string `json:"derivative_order_type"` // limit | market
	PriceExpression     string `json:"price_expression"`      // d_price + 40
	QuantityExpression  string `json:"quantity_expression"`   // delta_value / 100

	// 可选：条件触发（未来扩展）
	MinFillQuantity string `json:"min_fill_quantity,omitempty"` // 最小触发数量
	MaxDerivatives  int    `json:"max_derivatives,omitempty"`   // 最大衍生订单数量
}

// Validate 验证规则配置
func (r *FlowDerivativeRule) Validate() error {
	if !r.Enabled {
		return nil // 禁用状态无需验证
	}

	if r.DerivativeMarket == "" {
		return fmt.Errorf("derivative_market is required")
	}
	if r.DerivativeSymbol == "" {
		return fmt.Errorf("derivative_symbol is required")
	}
	if r.DerivativeDirection == "" {
		return fmt.Errorf("derivative_direction is required")
	}
	if r.PriceExpression == "" {
		return fmt.Errorf("price_expression is required")
	}
	if r.QuantityExpression == "" {
		return fmt.Errorf("quantity_expression is required")
	}

	// 验证 derivative_direction
	validDirections := map[string]bool{
		"opposite": true, "same": true, "rollover": true,
		"long": true, "short": true, "close_long": true, "close_short": true,
	}
	if !validDirections[r.DerivativeDirection] {
		return fmt.Errorf("invalid derivative_direction: %s", r.DerivativeDirection)
	}

	// 验证 derivative_order_type
	if r.DerivativeOrderType == "" {
		r.DerivativeOrderType = "limit" // 默认限价单
	}
	if r.DerivativeOrderType != "limit" && r.DerivativeOrderType != "market" {
		return fmt.Errorf("invalid derivative_order_type: %s", r.DerivativeOrderType)
	}

	return nil
}

// ToJSON 序列化为 JSON
func (r *FlowDerivativeRule) ToJSON() (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ParseFlowDerivativeRule 从 JSON 解析规则
func ParseFlowDerivativeRule(jsonStr string) (*FlowDerivativeRule, error) {
	if jsonStr == "" {
		return nil, nil
	}

	var rule FlowDerivativeRule
	if err := json.Unmarshal([]byte(jsonStr), &rule); err != nil {
		return nil, fmt.Errorf("failed to parse flow derivative rule: %w", err)
	}

	return &rule, nil
}

// ToDerivativeRule 转换为内部 DerivativeRule 格式（用于引擎处理）
func (r *FlowDerivativeRule) ToDerivativeRule(primaryOrder *Order) *DerivativeRule {
	return &DerivativeRule{
		Name:                fmt.Sprintf("flow-%d-rule", primaryOrder.FlowID),
		Enabled:             r.Enabled,
		Priority:            0, // 最高优先级
		Description:         fmt.Sprintf("Flow-level rule (source: %s)", r.SourceType),
		PrimaryMarket:       string(primaryOrder.MarketType),
		PrimarySymbol:       primaryOrder.FullSymbol,
		PrimaryDirection:    string(primaryOrder.Direction),
		DerivativeMarket:    r.DerivativeMarket,
		DerivativeSymbol:    r.DerivativeSymbol,
		DerivativeDirection: r.DerivativeDirection,
		DerivativeOrderType: r.DerivativeOrderType,
		PriceExpression:     r.PriceExpression,
		QuantityExpression:  r.QuantityExpression,
	}
}
