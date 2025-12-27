// internal/binance/types.go

package binance

import "time"

// ============================================
// 通用响应结构
// ============================================

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ErrorResponse struct {
	Error APIError `json:"error"`
}

// ============================================
// 健康检查
// ============================================

type HealthResponse struct {
	OK      bool   `json:"ok"`
	Service string `json:"service"`
}

// ============================================
// 价格查询
// ============================================

type PriceResponse struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
}

// ============================================
// 订单相关（标准 JSON 命名：snake_case）
// ============================================

// OrderResponse 下单/查单响应
// OrderWrapper API 响应的订单包装层
type OrderResponse struct {
	Order OrderInfo `json:"order"`
}
type OrderInfo struct {
	OrderID            int64     `json:"order_id"`
	ClientOrderID      string    `json:"client_order_id,omitempty"`
	Symbol             string    `json:"symbol"`
	Side               string    `json:"side"`
	Type               string    `json:"type"`
	Price              string    `json:"price"`
	OrigQty            string    `json:"orig_qty"`
	ExecutedQty        string    `json:"executed_qty"`
	AvgPrice           string    `json:"avg_price,omitempty"`            // 平均成交价
	CumulativeQuoteQty string    `json:"cumulative_quote_qty,omitempty"` // 累计成交额,只对现货订单有效
	Status             string    `json:"status"`
	TimeInForce        string    `json:"time_in_force,omitempty"`
	PositionSide       string    `json:"position_side,omitempty"`
	CreateTime         time.Time `json:"create_time"`
	UpdateTime         time.Time `json:"update_time"`
}

// SpotOrderRequest 现货下单请求
type SpotOrderRequest struct {
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	Quantity      string `json:"quantity"`
	Price         string `json:"price,omitempty"`
	ClientOrderID string `json:"client_order_id,omitempty"`
}

// CoinMOrderRequest 币本位合约下单请求
type CoinMOrderRequest struct {
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	Quantity      string `json:"quantity"`
	Price         string `json:"price,omitempty"`
	PositionSide  string `json:"position_side,omitempty"`
	Leverage      int    `json:"leverage,omitempty"`
	ClientOrderID string `json:"client_order_id,omitempty"`
}

// CancelOrderRequest 撤单请求
type CancelOrderRequest struct {
	Symbol  string `json:"symbol"`
	OrderID int64  `json:"order_id"`
}

// ============================================
// Webhook Track 相关
// ============================================

// TrackWebhookRequest 创建 Track Job 请求
type TrackWebhookRequest struct {
	Symbol        string            `json:"symbol"`
	OrderID       int64             `json:"order_id"`
	WebhookURL    string            `json:"webhook_url"`
	WebhookSecret string            `json:"webhook_secret,omitempty"`
	Retry         *TrackRetryConfig `json:"retry,omitempty"`
}

// TrackRetryConfig 重试配置
type TrackRetryConfig struct {
	BaseDelayMs       int     `json:"base_delay_ms,omitempty"`
	MaxDelayMs        int     `json:"max_delay_ms,omitempty"`
	Multiplier        float64 `json:"multiplier,omitempty"`
	JitterRatio       float64 `json:"jitter_ratio,omitempty"`
	MaxAttempts       int     `json:"max_attempts,omitempty"`
	MaxElapsedSeconds int     `json:"max_elapsed_seconds,omitempty"`
}

// TrackWebhookResponse 创建 Track Job 响应
type TrackWebhookResponse struct {
	JobID         string    `json:"job_id"`
	Status        string    `json:"status"`
	Symbol        string    `json:"symbol"`
	OrderID       int64     `json:"order_id"`
	WebhookURL    string    `json:"webhook_url"`
	WebhookSecret string    `json:"webhook_secret"`
	Order         OrderInfo `json:"order"`
	Note          string    `json:"note,omitempty"`
}

// TrackJobInfo Track Job 信息
type TrackJobInfo struct {
	JobID             string    `json:"job_id"`
	Market            string    `json:"market"`
	Symbol            string    `json:"symbol"`
	OrderID           int64     `json:"order_id"`
	Status            string    `json:"status"`
	WebhookURL        string    `json:"webhook_url"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	LastExecutedQty   string    `json:"last_executed_qty"`
	LastOrderStatus   string    `json:"last_order_status"`
	PendingDeliveries int       `json:"pending_deliveries"`
}

// ============================================
// Webhook 回调事件（REST API 服务推送给我们的）
// 全部使用标准 JSON 命名：snake_case
// ============================================

// WebhookEvent Webhook 回调事件体
type WebhookEvent struct {
	EventID    string           `json:"event_id"`
	JobID      string           `json:"job_id"`
	Market     string           `json:"market"`
	Symbol     string           `json:"symbol"`
	OrderID    int64            `json:"order_id"`
	EventType  string           `json:"event_type"`
	OccurredAt string           `json:"occurred_at"`
	Data       WebhookEventData `json:"data"`
}

// WebhookEventData 事件数据
type WebhookEventData struct {
	DeltaExecutedQty string    `json:"delta_executed_qty,omitempty"`
	Status           string    `json:"status"`
	Order            OrderInfo `json:"order"`
}

// WebhookOrder 事件中的订单信息（标准 JSON 命名：snake_case）
type WebhookOrder struct {
	OrderID       int64  `json:"order_id"`
	ClientOrderID string `json:"client_order_id"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	Price         string `json:"price"`
	OrigQty       string `json:"orig_qty"`
	ExecutedQty   string `json:"executed_qty"`
	Status        string `json:"status"`
	TimeInForce   string `json:"time_in_force,omitempty"`
}

// ============================================
// 季度合约查询
// ============================================

type QuarterSymbolInfo struct {
	Symbol         string `json:"symbol"`
	DeliveryUnixMs int64  `json:"delivery_unix_ms"`
	DeliveryTime   string `json:"delivery_time"`
	ContractType   string `json:"contract_type"`
}

type QuarterSymbolsResponse struct {
	Base    string            `json:"base"`
	Current QuarterSymbolInfo `json:"current"`
	Next    QuarterSymbolInfo `json:"next"`
	Cache   struct {
		Status    string `json:"status"`
		UpdatedAt string `json:"updated_at"`
	} `json:"cache"`
}
