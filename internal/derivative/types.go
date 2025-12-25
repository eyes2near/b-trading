// internal/derivative/types.go

package derivative

import (
	"github.com/eyes2near/b-trading/internal/models"
)

// FillParams 成交事件参数（输入）
type FillParams struct {
	PrimaryOrder  *models.Order
	FillPrice     string // 本次成交价格
	Delta         string // 本次成交增量（原始单位）
	CumulativeQty string // 累计成交量
}

// DerivativeOrderParams 衍生订单参数（输出）
type DerivativeOrderParams struct {
	RuleID     uint
	RuleName   string
	Market     models.MarketType
	Symbol     string
	SymbolType string // 记录原始配置，如 btc-current
	Direction  models.Direction
	OrderType  models.OrderType
	Price      string // 已格式化：2位小数
	Quantity   string // 已格式化：合约为整数

	// 审计追踪
	EvaluationContext map[string]string // 求值变量快照

	// 执行日志 ID（用于后续更新）
	LogID uint
}

// ContractInfo 合约信息
type ContractInfo struct {
	Symbol       string
	ContractSize float64 // 合约面值（USD）
}

// 合约面值配置
var ContractSizes = map[string]float64{
	"BTC": 100, // BTC 合约每张 100 USD
	"ETH": 10,  // ETH 合约每张 10 USD
	// 可扩展其他币种
}

// GetContractSize 获取合约面值
func GetContractSize(base string) float64 {
	if size, ok := ContractSizes[base]; ok {
		return size
	}
	return 100 // 默认 100 USD
}
