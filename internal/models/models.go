package models

import (
	"database/sql"
	"time"

	"gorm.io/gorm"
)

// ============================================
// 枚举类型定义
// ============================================

type FlowStatus string

const (
	FlowStatusActive    FlowStatus = "active"
	FlowStatusCompleted FlowStatus = "completed"
	FlowStatusTimeout   FlowStatus = "timeout"
	FlowStatusCancelled FlowStatus = "cancelled"
)

type OrderRole string

const (
	OrderRolePrimary    OrderRole = "primary"
	OrderRoleDerivative OrderRole = "derivative"
)

type MarketType string

const (
	MarketTypeSpot  MarketType = "spot"
	MarketTypeCoinM MarketType = "coin-m"
)

type ContractType string

const (
	ContractTypeCurrent ContractType = "current"
	ContractTypeNext    ContractType = "next"
)

type Direction string

const (
	DirectionLong       Direction = "long"
	DirectionShort      Direction = "short"
	DirectionCloseLong  Direction = "close_long"
	DirectionCloseShort Direction = "close_short"
)

type OrderType string

const (
	OrderTypeLimit  OrderType = "limit"
	OrderTypeMarket OrderType = "market"
)

type OrderStatus string

const (
	OrderStatusPending         OrderStatus = "pending"
	OrderStatusSubmitted       OrderStatus = "submitted"
	OrderStatusPartiallyFilled OrderStatus = "partially_filled"
	OrderStatusFilled          OrderStatus = "filled"
	OrderStatusCancelled       OrderStatus = "cancelled"
	OrderStatusRejected        OrderStatus = "rejected"
	OrderStatusExpired         OrderStatus = "expired"
)

type LogType string

const (
	LogTypeOrderCreate       LogType = "order_create"
	LogTypeOrderSubmit       LogType = "order_submit"
	LogTypeOrderCancel       LogType = "order_cancel"
	LogTypeFillReceived      LogType = "fill_received"
	LogTypeDerivativeTrigger LogType = "derivative_trigger"
	LogTypeFlowComplete      LogType = "flow_complete"
	LogTypeError             LogType = "error"
	LogTypeSystem            LogType = "system"
	LogTypeOrderComplete     LogType = "order_complete"
)

type LogSeverity string

const (
	LogSeverityDebug    LogSeverity = "debug"
	LogSeverityInfo     LogSeverity = "info"
	LogSeverityWarning  LogSeverity = "warning"
	LogSeverityError    LogSeverity = "error"
	LogSeverityCritical LogSeverity = "critical"
)

// ============================================
// 核心模型
// ============================================

// TradingFlow 交易流程主表
type TradingFlow struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	FlowUUID  string         `gorm:"uniqueIndex;not null;size:64" json:"flow_uuid"`
	Status    FlowStatus     `gorm:"not null;index;size:20" json:"status"`
	CreatedAt time.Time      `gorm:"not null;index" json:"created_at"`
	UpdatedAt time.Time      `gorm:"not null" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	CompletedAt sql.NullTime `json:"completed_at,omitempty"`
	TimeoutAt   sql.NullTime `json:"timeout_at,omitempty"`
	Notes       string       `gorm:"type:text" json:"notes,omitempty"`

	DerivativeRuleConfig string `gorm:"type:text" json:"derivative_rule_config,omitempty"` // JSON

	// 审计字段
	CreatedBy       string `gorm:"size:100" json:"created_by,omitempty"`
	CancelledReason string `gorm:"type:text" json:"cancelled_reason,omitempty"`
	Metadata        string `gorm:"type:text" json:"metadata,omitempty"` // JSON

	// 关联关系
	Orders        []Order             `gorm:"foreignKey:FlowID;constraint:OnDelete:CASCADE" json:"orders,omitempty"`
	FillEvents    []FillEvent         `gorm:"foreignKey:FlowID;constraint:OnDelete:CASCADE" json:"fill_events,omitempty"`
	StatusHistory []FlowStatusHistory `gorm:"foreignKey:FlowID;constraint:OnDelete:CASCADE" json:"status_history,omitempty"`
}

func (TradingFlow) TableName() string {
	return "trading_flows"
}

// internal/models/models.go - 更新 Order 模型，新增字段

// 在 Order 结构体中添加以下字段（约在 Metadata 字段之后）

type Order struct {
	ID            uint          `gorm:"primaryKey" json:"id"`
	OrderUUID     string        `gorm:"uniqueIndex;not null;size:64" json:"order_uuid"`
	FlowID        uint          `gorm:"not null;index" json:"flow_id"`
	ParentOrderID sql.NullInt64 `gorm:"index" json:"parent_order_id,omitempty"`
	OrderRole     OrderRole     `gorm:"not null;index;size:20" json:"order_role"`

	// 市场和品种信息
	MarketType   MarketType   `gorm:"not null;size:20" json:"market_type"`
	SymbolType   string       `gorm:"not null;size:50" json:"symbol_type"`
	ContractType ContractType `gorm:"size:20" json:"contract_type,omitempty"`
	FullSymbol   string       `gorm:"not null;size:50" json:"full_symbol"`

	// 订单方向和类型
	Direction Direction `gorm:"not null;size:20" json:"direction"`
	OrderType OrderType `gorm:"not null;size:20;default:limit" json:"order_type"`

	// 订单参数
	Price          sql.NullString `gorm:"type:decimal(20,8)" json:"price,omitempty"`
	Quantity       string         `gorm:"not null;type:decimal(20,8)" json:"quantity"`
	FilledQuantity string         `gorm:"not null;type:decimal(20,8);default:0" json:"filled_quantity"`
	AvgFillPrice   string         `gorm:"type:decimal(20,8);default:0" json:"avg_fill_price"`

	// 订单状态
	Status               OrderStatus `gorm:"not null;index;size:20" json:"status"`
	BinanceOrderID       string      `gorm:"index;size:100" json:"binance_order_id,omitempty"`
	BinanceClientOrderID string      `gorm:"size:100" json:"binance_client_order_id,omitempty"`

	// Track Job 关联
	TrackJobID     string `gorm:"size:100;index" json:"track_job_id,omitempty"`
	TrackJobStatus string `gorm:"size:20" json:"track_job_status,omitempty"`
	WebhookSecret  string `gorm:"size:200" json:"-"` // 不暴露给 JSON

	// 时间戳
	CreatedAt   time.Time      `gorm:"not null" json:"created_at"`
	SubmittedAt sql.NullTime   `json:"submitted_at,omitempty"`
	UpdatedAt   time.Time      `gorm:"not null" json:"updated_at"`
	CompletedAt sql.NullTime   `json:"completed_at,omitempty"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`

	// 审计和计算字段
	TriggerFillEventID    sql.NullInt64 `json:"trigger_fill_event_id,omitempty"`
	PriceCalculationLogic string        `gorm:"type:text" json:"price_calculation_logic,omitempty"`
	ErrorMessage          string        `gorm:"type:text" json:"error_message,omitempty"`
	RetryCount            int           `gorm:"default:0" json:"retry_count"`
	Metadata              string        `gorm:"type:text" json:"metadata,omitempty"`
	BinanceRawResponse    string        `gorm:"type:text" json:"binance_raw_response,omitempty"`

	// 乐观锁版本号
	Version int `gorm:"not null;default:0" json:"version"`

	// 关联关系
	Flow             *TradingFlow         `gorm:"foreignKey:FlowID" json:"flow,omitempty"`
	ParentOrder      *Order               `gorm:"foreignKey:ParentOrderID" json:"parent_order,omitempty"`
	DerivativeOrders []Order              `gorm:"foreignKey:ParentOrderID" json:"derivative_orders,omitempty"`
	FillEvents       []FillEvent          `gorm:"foreignKey:OrderID;constraint:OnDelete:CASCADE" json:"fill_events,omitempty"`
	StatusHistory    []OrderStatusHistory `gorm:"foreignKey:OrderID;constraint:OnDelete:CASCADE" json:"status_history,omitempty"`
	TriggerFillEvent *FillEvent           `gorm:"foreignKey:TriggerFillEventID" json:"trigger_fill_event,omitempty"`
}

func (Order) TableName() string {
	return "orders"
}

// FillEvent 成交事件表
type FillEvent struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	FillUUID string `gorm:"uniqueIndex;not null;size:64" json:"fill_uuid"`
	OrderID  uint   `gorm:"not null;index" json:"order_id"`
	FlowID   uint   `gorm:"not null;index" json:"flow_id"`

	// 成交信息
	FillPrice    string    `gorm:"not null;type:decimal(20,8)" json:"fill_price"`
	FillQuantity string    `gorm:"not null;type:decimal(20,8)" json:"fill_quantity"`
	FillTime     time.Time `gorm:"not null;index" json:"fill_time"`

	// Binance相关信息
	BinanceTradeID  string `gorm:"size:100" json:"binance_trade_id,omitempty"`
	BinanceOrderID  string `gorm:"not null;size:100" json:"binance_order_id"`
	Commission      string `gorm:"type:decimal(20,8)" json:"commission,omitempty"`
	CommissionAsset string `gorm:"size:20" json:"commission_asset,omitempty"`

	// 衍生订单追踪
	HasTriggeredDerivative bool          `gorm:"default:false;index" json:"has_triggered_derivative"`
	DerivativeOrderID      sql.NullInt64 `json:"derivative_order_id,omitempty"`

	// 审计字段
	ReceivedAt  time.Time    `gorm:"not null" json:"received_at"`
	ProcessedAt sql.NullTime `json:"processed_at,omitempty"`
	RawData     string       `gorm:"type:text" json:"raw_data,omitempty"` // JSON

	// 关联关系
	Order           *Order       `gorm:"foreignKey:OrderID" json:"order,omitempty"`
	Flow            *TradingFlow `gorm:"foreignKey:FlowID" json:"flow,omitempty"`
	DerivativeOrder *Order       `gorm:"foreignKey:DerivativeOrderID" json:"derivative_order,omitempty"`
}

func (FillEvent) TableName() string {
	return "fill_events"
}

// OrderStatusHistory 订单状态变更历史
type OrderStatusHistory struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	OrderID         uint      `gorm:"not null;index" json:"order_id"`
	FromStatus      string    `gorm:"size:20" json:"from_status,omitempty"`
	ToStatus        string    `gorm:"not null;size:20" json:"to_status"`
	ChangedAt       time.Time `gorm:"not null;index" json:"changed_at"`
	Reason          string    `gorm:"type:text" json:"reason,omitempty"`
	BinanceResponse string    `gorm:"type:text" json:"binance_response,omitempty"`

	// 关联关系
	Order *Order `gorm:"foreignKey:OrderID" json:"order,omitempty"`
}

func (OrderStatusHistory) TableName() string {
	return "order_status_history"
}

// FlowStatusHistory 流程状态变更历史
type FlowStatusHistory struct {
	ID                      uint      `gorm:"primaryKey" json:"id"`
	FlowID                  uint      `gorm:"not null;index" json:"flow_id"`
	FromStatus              string    `gorm:"size:20" json:"from_status,omitempty"`
	ToStatus                string    `gorm:"not null;size:20" json:"to_status"`
	ChangedAt               time.Time `gorm:"not null;index" json:"changed_at"`
	Reason                  string    `gorm:"type:text" json:"reason,omitempty"`
	PendingDerivativesCount int       `json:"pending_derivatives_count"`

	// 关联关系
	Flow *TradingFlow `gorm:"foreignKey:FlowID" json:"flow,omitempty"`
}

func (FlowStatusHistory) TableName() string {
	return "flow_status_history"
}

// AuditLog 审计日志表
type AuditLog struct {
	ID          uint          `gorm:"primaryKey" json:"id"`
	LogType     LogType       `gorm:"not null;index;size:30" json:"log_type"`
	FlowID      sql.NullInt64 `json:"flow_id,omitempty"`
	OrderID     sql.NullInt64 `json:"order_id,omitempty"`
	FillEventID sql.NullInt64 `json:"fill_event_id,omitempty"`
	Severity    LogSeverity   `gorm:"not null;index;size:20" json:"severity"`
	Message     string        `gorm:"not null;type:text" json:"message"`
	Detail      string        `gorm:"type:text" json:"detail,omitempty"` // JSON
	CreatedAt   time.Time     `gorm:"not null;index" json:"created_at"`

	// 关联关系
	Flow      *TradingFlow `gorm:"foreignKey:FlowID" json:"flow,omitempty"`
	Order     *Order       `gorm:"foreignKey:OrderID" json:"order,omitempty"`
	FillEvent *FillEvent   `gorm:"foreignKey:FillEventID" json:"fill_event,omitempty"`
}

func (AuditLog) TableName() string {
	return "audit_logs"
}

// MarketConfig 市场配置表
type MarketConfig struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	MarketType  string         `gorm:"not null;size:20;uniqueIndex:idx_market_config" json:"market_type"`
	Symbol      string         `gorm:"not null;size:50;uniqueIndex:idx_market_config" json:"symbol"`
	ConfigKey   string         `gorm:"not null;size:100;uniqueIndex:idx_market_config" json:"config_key"`
	ConfigValue string         `gorm:"not null;type:text" json:"config_value"`
	IsActive    bool           `gorm:"default:true" json:"is_active"`
	CreatedAt   time.Time      `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time      `gorm:"not null" json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

func (MarketConfig) TableName() string {
	return "market_configs"
}

// ============================================
// 视图模型(只读)
// ============================================

// CreateOrderRequest represents the request body for creating a new order from the UI
type CreateOrderRequest struct {
	Market    string `json:"market" binding:"required"` // "spot" or "coinm"
	Symbol    string `json:"symbol" binding:"required"`
	Side      string `json:"side" binding:"required"`      // "BUY" or "SELL"
	OrderType string `json:"orderType" binding:"required"` // "MARKET" or "LIMIT"
	Quantity  string `json:"quantity" binding:"required"`
	Price     string `json:"price,omitempty"`    // Optional for MARKET orders
	Position  string `json:"position,omitempty"` // Optional for Spot market ("LONG" or "SHORT")
}

// CreateOrderARequest represents the request body for creating a new Order A from the UI
type CreateOrderARequest struct {
	OrderID  string  `json:"orderID" binding:"required"`
	Item     string  `json:"item" binding:"required"`
	Quantity float64 `json:"quantity" binding:"required"` // Use float64 for numbers
	Price    float64 `json:"price" binding:"required"`    // Use float64 for numbers
	Status   string  `json:"status" binding:"required"`   // "Pending", "Completed", "Cancelled"
}

// ActiveFlowView 活跃流程视图
type ActiveFlowView struct {
	ID                  uint      `json:"id"`
	FlowUUID            string    `json:"flow_uuid"`
	Status              string    `json:"status"`
	CreatedAt           time.Time `json:"created_at"`
	TotalOrders         int       `json:"total_orders"`
	PrimaryOrders       int       `json:"primary_orders"`
	DerivativeOrders    int       `json:"derivative_orders"`
	ActiveOrders        int       `json:"active_orders"`
	TotalFilledQuantity string    `json:"total_filled_quantity"`
}

func (ActiveFlowView) TableName() string {
	return "v_active_flows"
}

// OrderDetailView 订单详情视图
type OrderDetailView struct {
	ID              uint   `json:"id"`
	OrderUUID       string `json:"order_uuid"`
	OrderRole       string `json:"order_role"`
	MarketType      string `json:"market_type"`
	FullSymbol      string `json:"full_symbol"`
	Direction       string `json:"direction"`
	OrderType       string `json:"order_type"`
	Price           string `json:"price"`
	Quantity        string `json:"quantity"`
	FilledQuantity  string `json:"filled_quantity"`
	Status          string `json:"status"`
	BinanceOrderID  string `json:"binance_order_id"`
	FlowUUID        string `json:"flow_uuid"`
	ParentOrderUUID string `json:"parent_order_uuid"`
	FillCount       int    `json:"fill_count"`
	TotalFillValue  string `json:"total_fill_value"`
}

func (OrderDetailView) TableName() string {
	return "v_order_details"
}

// ============================================
// Hook方法(自动更新时间戳等)
// ============================================

// BeforeCreate 创建前的Hook
func (f *TradingFlow) BeforeCreate(tx *gorm.DB) error {
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now()
	}
	if f.UpdatedAt.IsZero() {
		f.UpdatedAt = time.Now()
	}
	return nil
}

// BeforeUpdate 更新前的Hook
func (f *TradingFlow) BeforeUpdate(tx *gorm.DB) error {
	f.UpdatedAt = time.Now()
	return nil
}

// BeforeCreate Order创建前的Hook
func (o *Order) BeforeCreate(tx *gorm.DB) error {
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now()
	}
	if o.UpdatedAt.IsZero() {
		o.UpdatedAt = time.Now()
	}
	return nil
}

// BeforeUpdate Order更新前的Hook
func (o *Order) BeforeUpdate(tx *gorm.DB) error {
	o.UpdatedAt = time.Now()
	return nil
}

// BeforeCreate FillEvent创建前的Hook
func (f *FillEvent) BeforeCreate(tx *gorm.DB) error {
	if f.ReceivedAt.IsZero() {
		f.ReceivedAt = time.Now()
	}
	return nil
}

// BeforeCreate AuditLog创建前的Hook
func (a *AuditLog) BeforeCreate(tx *gorm.DB) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	return nil
}

// BeforeCreate MarketConfig创建前的Hook
func (m *MarketConfig) BeforeCreate(tx *gorm.DB) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = time.Now()
	}
	return nil
}

// BeforeUpdate MarketConfig更新前的Hook
func (m *MarketConfig) BeforeUpdate(tx *gorm.DB) error {
	m.UpdatedAt = time.Now()
	return nil
}

// ============================================
// Webhook 幂等去重模型 (新增)
// ============================================

// WebhookDelivery Webhook 投递记录（用于幂等去重）
type WebhookDelivery struct {
	ID          uint         `gorm:"primaryKey" json:"id"`
	DeliveryID  string       `gorm:"uniqueIndex;not null;size:100" json:"delivery_id"` // X-Webhook-Delivery-ID
	JobID       string       `gorm:"index;size:100" json:"job_id"`                     // X-Webhook-Job-ID
	EventID     string       `gorm:"index;size:100" json:"event_id"`                   // body 中的 event_id
	EventType   string       `gorm:"size:30" json:"event_type"`                        // fill_update / terminal
	Market      string       `gorm:"size:20" json:"market"`                            // spot / coinm
	Symbol      string       `gorm:"size:50" json:"symbol"`
	OrderID     int64        `json:"order_id"`
	RawPayload  string       `gorm:"type:text" json:"raw_payload"` // 原始 JSON
	ReceivedAt  time.Time    `gorm:"not null;index" json:"received_at"`
	ProcessedAt sql.NullTime `json:"processed_at,omitempty"`
	ProcessErr  string       `gorm:"type:text" json:"process_err,omitempty"` // 处理错误信息
}

func (WebhookDelivery) TableName() string {
	return "webhook_deliveries"
}

// BeforeCreate WebhookDelivery 创建前的 Hook
func (w *WebhookDelivery) BeforeCreate(tx *gorm.DB) error {
	if w.ReceivedAt.IsZero() {
		w.ReceivedAt = time.Now()
	}
	return nil
}
