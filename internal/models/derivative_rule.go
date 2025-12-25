// internal/models/derivative_rule.go

package models

import (
	"time"

	"gorm.io/gorm"
)

// DerivativeRule 衍生订单规则表
type DerivativeRule struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"uniqueIndex;not null;size:100" json:"name"`
	Enabled     bool   `gorm:"not null;default:true;index" json:"enabled"`
	Priority    int    `gorm:"not null;default:0;index" json:"priority"` // 数值越小优先级越高
	Description string `gorm:"type:text" json:"description"`

	// 主订单匹配条件
	PrimaryMarket    string `gorm:"not null;size:20;index:idx_primary" json:"primary_market"` // spot | coinm
	PrimarySymbol    string `gorm:"not null;size:50;index:idx_primary" json:"primary_symbol"` // BTCUSDT | btc_current | *
	PrimaryDirection string `gorm:"size:100" json:"primary_direction"`                        // close_short | long | close_short,close_long | * | 空

	// 衍生订单生成规则
	DerivativeMarket    string `gorm:"not null;size:20" json:"derivative_market"`                   // spot | coinm
	DerivativeSymbol    string `gorm:"not null;size:50" json:"derivative_symbol"`                   // btc_current | {base}USDT
	DerivativeDirection string `gorm:"not null;size:20" json:"derivative_direction"`                // opposite | same | long | short
	DerivativeOrderType string `gorm:"not null;size:20;default:limit" json:"derivative_order_type"` // limit | market
	PriceExpression     string `gorm:"not null;type:text" json:"price_expression"`                  // d_price - 5
	QuantityExpression  string `gorm:"not null;type:text" json:"quantity_expression"`               // delta_value / 100

	// 审计字段
	CreatedAt time.Time      `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time      `gorm:"not null" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
	CreatedBy string         `gorm:"size:100" json:"created_by"`
	UpdatedBy string         `gorm:"size:100" json:"updated_by"`
}

func (DerivativeRule) TableName() string {
	return "derivative_rules"
}

// BeforeCreate Hook
func (r *DerivativeRule) BeforeCreate(tx *gorm.DB) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = time.Now()
	}
	return nil
}

// BeforeUpdate Hook
func (r *DerivativeRule) BeforeUpdate(tx *gorm.DB) error {
	r.UpdatedAt = time.Now()
	return nil
}

// DerivativeRuleLog 规则执行日志
type DerivativeRuleLog struct {
	ID                uint   `gorm:"primaryKey" json:"id"`
	RuleID            uint   `gorm:"not null;index" json:"rule_id"`
	RuleName          string `gorm:"not null;size:100" json:"rule_name"`
	FlowID            uint   `gorm:"not null;index" json:"flow_id"`
	PrimaryOrderID    uint   `gorm:"not null;index" json:"primary_order_id"`
	DerivativeOrderID uint   `json:"derivative_order_id,omitempty"`

	// 执行上下文快照
	EvaluationContext  string `gorm:"type:text" json:"evaluation_context"` // JSON
	PriceExpression    string `gorm:"type:text" json:"price_expression"`
	PriceResult        string `gorm:"size:50" json:"price_result"`
	QuantityExpression string `gorm:"type:text" json:"quantity_expression"`
	QuantityResult     string `gorm:"size:50" json:"quantity_result"`

	// 执行结果
	Success      bool      `gorm:"not null" json:"success"`
	ErrorMessage string    `gorm:"type:text" json:"error_message,omitempty"`
	ExecutedAt   time.Time `gorm:"not null;index" json:"executed_at"`
}

func (DerivativeRuleLog) TableName() string {
	return "derivative_rule_logs"
}

// BeforeCreate Hook
func (l *DerivativeRuleLog) BeforeCreate(tx *gorm.DB) error {
	if l.ExecutedAt.IsZero() {
		l.ExecutedAt = time.Now()
	}
	return nil
}
