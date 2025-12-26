// internal/market/transformer.go
package market

import (
	"strconv"

	"github.com/eyes2near/b-trading/internal/models"
)

// TransformConfig 转换配置
type TransformConfig struct {
	MaxLevels int // 最大档位数，默认 5
}

// DefaultTransformConfig 默认转换配置
func DefaultTransformConfig() TransformConfig {
	return TransformConfig{
		MaxLevels: 5,
	}
}

// Transform 将 Publisher 的 PublishedBandEvent 转换为前端格式
// 使用 Publisher 的 publishedAt 作为时间戳
func Transform(event models.PublishedBandEvent, cfg TransformConfig) models.DepthUpdateMsg {
	snap := event.Snapshot
	return models.DepthUpdateMsg{
		Type:   "depthUpdate",
		Symbol: snap.Symbol,
		Ts:     event.PublishedAt,
		Asks:   processLevels(snap.Asks, cfg.MaxLevels),
		Bids:   processLevels(snap.Bids, cfg.MaxLevels),
	}
}

func processLevels(in [][]string, maxLevels int) []models.OrderLevel {
	limit := maxLevels
	if len(in) < limit {
		limit = len(in)
	}

	out := make([]models.OrderLevel, 0, limit)
	var cumulativeTotal float64

	for i := 0; i < limit; i++ {
		if len(in[i]) < 2 {
			continue
		}
		priceStr := in[i][0]
		qtyStr := in[i][1]
		qty, _ := strconv.ParseFloat(qtyStr, 64)
		cumulativeTotal += qty

		out = append(out, models.OrderLevel{
			Price:    priceStr,
			Quantity: qtyStr,
			Total:    cumulativeTotal,
		})
	}
	return out
}
