// internal/derivative/formatter.go

package derivative

import (
	"math"

	"github.com/shopspring/decimal"
)

// Formatter 精度格式化器
type Formatter struct{}

// NewFormatter 创建格式化器
func NewFormatter() *Formatter {
	return &Formatter{}
}

// FormatPrice 格式化价格（2位小数）
func (f *Formatter) FormatPrice(price float64) string {
	d := decimal.NewFromFloat(price)
	return d.Round(2).String()
}

// FormatQuantity 格式化数量
func (f *Formatter) FormatQuantity(qty float64, market string) string {
	if market == "coinm" {
		// 合约：四舍五入取整，最小1张
		rounded := math.Round(qty)
		if rounded < 1 {
			rounded = 1
		}
		return decimal.NewFromFloat(rounded).String()
	}

	// 现货：保留8位小数
	d := decimal.NewFromFloat(qty)
	return d.Round(8).String()
}

// ParseFloat 安全解析字符串为 float64
func ParseFloat(s string) float64 {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return 0
	}
	f, _ := d.Float64()
	return f
}
