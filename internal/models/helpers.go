// internal/models/converters.go
package models

import (
	"strings"
	"unicode"
)

// ============================================
// Binance 状态映射
// ============================================

// MapBinanceStatus 将 Binance 订单状态映射为内部状态
func MapBinanceStatus(status string) OrderStatus {
	switch status {
	case "NEW":
		return OrderStatusSubmitted
	case "PARTIALLY_FILLED":
		return OrderStatusPartiallyFilled
	case "FILLED":
		return OrderStatusFilled
	case "CANCELED":
		return OrderStatusCancelled
	case "REJECTED":
		return OrderStatusRejected
	case "EXPIRED":
		return OrderStatusExpired
	default:
		return OrderStatusPending
	}
}

// ============================================
// 内部状态判断
// ============================================

// IsTerminalOrderStatus 检查订单状态是否为终态
func IsTerminalOrderStatus(status string) bool {
	status = strings.ToUpper(status)
	switch status {
	case "FILLED", "CANCELED", "REJECTED", "EXPIRED":
		return true
	default:
		return false
	}
}

// IsActiveOrderStatus 检查订单状态是否为活跃状态
func IsActiveOrderStatus(status OrderStatus) bool {
	switch status {
	case OrderStatusPending, OrderStatusSubmitted, OrderStatusPartiallyFilled:
		return true
	default:
		return false
	}
}

// ============================================
// 方向转换
// ============================================

// DirectionToSide 将内部方向转换为 Binance Side
func DirectionToSide(d Direction) string {
	switch d {
	case DirectionLong, DirectionCloseShort:
		return "BUY"
	case DirectionShort, DirectionCloseLong:
		return "SELL"
	default:
		return "BUY"
	}
}

// DirectionToPositionSide 将内部方向转换为 Binance PositionSide
func DirectionToPositionSide(d Direction) string {
	switch d {
	case DirectionLong, DirectionCloseLong:
		return "LONG"
	case DirectionShort, DirectionCloseShort:
		return "SHORT"
	default:
		return "BOTH"
	}
}

// OrderTypeToString 将内部订单类型转换为 Binance 订单类型
func OrderTypeToString(t OrderType) string {
	switch t {
	case OrderTypeLimit:
		return "LIMIT"
	case OrderTypeMarket:
		return "MARKET"
	default:
		return "LIMIT"
	}
}

// NormalizeMarketKey 归一化 market 字段。
// 统一输出：
//   - "spot"
//   - "coin-m"
//
// 兼容输入：spot / SPOT / " coin-m " / "coin_m" / "coinm"
func NormalizeMarketKey(m string) MarketType {
	s := strings.TrimSpace(m)
	if s == "" {
		return ""
	}

	s = strings.ToLower(s)
	// 去掉所有空白字符
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)

	// 统一去掉分隔符：coin-m / coin_m -> coinm
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "_", "")

	switch s {
	case "spot":
		return MarketTypeSpot
	case "coinm":
		return MarketTypeCoinM
	default:
		// 未知值：返回“清洗后”的结果，便于后续统一处理/报错
		return MarketType(s)
	}
}
