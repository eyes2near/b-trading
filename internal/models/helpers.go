// internal/models/converters.go
package models

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

// IsTerminalBinanceStatus 检查 Binance 状态是否为终态
func IsTerminalBinanceStatus(status string) bool {
	switch status {
	case "FILLED", "CANCELED", "REJECTED", "EXPIRED":
		return true
	default:
		return false
	}
}

// ============================================
// 内部状态判断
// ============================================

// IsTerminalOrderStatus 检查订单状态是否为终态
func IsTerminalOrderStatus(status OrderStatus) bool {
	switch status {
	case OrderStatusFilled, OrderStatusCancelled, OrderStatusRejected, OrderStatusExpired:
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
