// internal/api/dto.go

package api

import (
	"time"

	"github.com/eyes2near/b-trading/internal/models"
)

// ==========================================
// Request DTOs
// ==========================================

// CreateFlowRequest 对应文档 1.3 创建交易流程的请求体
type CreateFlowRequest struct {
	MarketType string `json:"market_type" binding:"required"` // "spot" or "coin-m"
	Symbol     string `json:"symbol" binding:"required"`      // 交易对名称，如 "BTCUSDT"
	SymbolType string `json:"symbol_type"`                    // 内部标识，如 "BTCUSDT" 或 "BTC-CURRENT"
	Direction  string `json:"direction" binding:"required"`   // "long", "short"
	OrderType  string `json:"order_type" binding:"required"`  // "limit", "market"
	Quantity   string `json:"quantity" binding:"required"`    // 下单数量
	Price      string `json:"price"`                          // 价格 (限价单必填)

	// Coin-M 特有字段，如果前端逻辑包含 contract_type 也可在此接收
	ContractType   string                   `json:"contract_type,omitempty"`
	DerivativeRule *FlowDerivativeRuleInput `json:"derivative_rule,omitempty"`
}

// FlowDerivativeRuleInput 创建 Flow 时的衍生规则输入
type FlowDerivativeRuleInput struct {
	// 方式1：直接指定完整规则
	Enabled             *bool  `json:"enabled,omitempty"`               // 默认 true
	DerivativeMarket    string `json:"derivative_market,omitempty"`     // spot | coinm
	DerivativeSymbol    string `json:"derivative_symbol,omitempty"`     // btc_current | {base}USDT
	DerivativeDirection string `json:"derivative_direction,omitempty"`  // opposite | same | rollover
	DerivativeOrderType string `json:"derivative_order_type,omitempty"` // limit | market
	PriceExpression     string `json:"price_expression,omitempty"`      // d_price + 40
	QuantityExpression  string `json:"quantity_expression,omitempty"`   // delta_value / 100

	// 方式2：基于模板创建（可选覆盖部分字段）
	TemplateRuleID   *uint             `json:"template_rule_id,omitempty"`   // 模板规则 ID
	TemplateRuleName string            `json:"template_rule_name,omitempty"` // 或者通过名称指定
	Overrides        map[string]string `json:"overrides,omitempty"`          // 覆盖模板的字段
}

// ToFlowDerivativeRule 转换为内部规则配置
func (input *FlowDerivativeRuleInput) ToFlowDerivativeRule() *models.FlowDerivativeRule {
	if input == nil {
		return nil
	}

	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}

	return &models.FlowDerivativeRule{
		Enabled:             enabled,
		SourceType:          "custom",
		DerivativeMarket:    input.DerivativeMarket,
		DerivativeSymbol:    input.DerivativeSymbol,
		DerivativeDirection: input.DerivativeDirection,
		DerivativeOrderType: input.DerivativeOrderType,
		PriceExpression:     input.PriceExpression,
		QuantityExpression:  input.QuantityExpression,
	}
}

// ==========================================
// Response DTOs
// ==========================================

// OrderDTO 对应文档中的 Order 结构
type OrderDTO struct {
	ID             uint   `json:"id"`
	OrderRole      string `json:"order_role"`                 // "Main" or "Hedge"
	FullSymbol     string `json:"full_symbol"`                // e.g. "BTCUSDT"
	Direction      string `json:"direction"`                  // "long", "short"
	Quantity       string `json:"quantity"`                   // 数量
	FilledQuantity string `json:"filled_quantity"`            // 已成交数量
	Status         string `json:"status"`                     // 状态
	BinanceOrderID string `json:"binance_order_id"`           // 币安订单号
	ArgFilledPrice string `json:"avg_filled_price,omitempty"` // 价格
}

// FlowResponse 对应文档 1.1 和 1.2 的 Flow 响应结构
type FlowResponse struct {
	ID                   uint                       `json:"id"`
	FlowUUID             string                     `json:"flow_uuid"`
	Status               string                     `json:"status"`
	CreatedAt            time.Time                  `json:"created_at"`
	TotalFilledQuantity  string                     `json:"total_filled_quantity"`
	DerivativeOrders     int                        `json:"derivative_orders"`
	Orders               []OrderDTO                 `json:"orders"`
	DerivativeRuleConfig *models.FlowDerivativeRule `json:"derivative_rule_config,omitempty"`
}

// ==========================================
// Mappers (Model -> DTO)
// ==========================================

func mapFlowToResponse(flow *models.TradingFlow) FlowResponse {
	resp := FlowResponse{
		ID:        flow.ID,
		FlowUUID:  flow.FlowUUID,
		Status:    string(flow.Status),
		CreatedAt: flow.CreatedAt,
		Orders:    make([]OrderDTO, 0),
	}

	// 解析衍生规则配置
	if flow.DerivativeRuleConfig != "" {
		rule, err := models.ParseFlowDerivativeRule(flow.DerivativeRuleConfig)
		if err == nil && rule != nil {
			resp.DerivativeRuleConfig = rule
		}
	}

	derivativeCount := 0
	totalFilled := "0"
	primaryFound := false

	for _, order := range flow.Orders {
		roleDisplay := "Main"
		if order.OrderRole == models.OrderRoleDerivative {
			roleDisplay = "Hedge"
			derivativeCount++
		} else if !primaryFound {
			totalFilled = order.FilledQuantity
			primaryFound = true
		}

		resp.Orders = append(resp.Orders, OrderDTO{
			ID:             order.ID,
			OrderRole:      roleDisplay,
			FullSymbol:     order.FullSymbol,
			Direction:      string(order.Direction),
			Quantity:       order.Quantity,
			FilledQuantity: order.FilledQuantity,
			Status:         string(order.Status),
			BinanceOrderID: order.BinanceOrderID,
			ArgFilledPrice: order.AvgFillPrice,
		})
	}

	resp.DerivativeOrders = derivativeCount
	resp.TotalFilledQuantity = totalFilled

	return resp
}

func mapFlowsToResponse(flows []models.TradingFlow) []FlowResponse {
	result := make([]FlowResponse, len(flows))
	for i, f := range flows {
		result[i] = mapFlowToResponse(&f)
	}
	return result
}
