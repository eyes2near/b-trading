// internal/database/seed.go

package database

import (
	"log"

	"github.com/eyes2near/b-trading/internal/models"
	"gorm.io/gorm"
)

// seedDefaultDerivativeRules 初始化默认衍生订单规则
func seedDefaultDerivativeRules(db *gorm.DB) error {
	rules := []models.DerivativeRule{
		{
			Name:                "roll-btc_current_to_next",
			Enabled:             true,
			Priority:            10,
			Description:         "BTC本季合约滚仓到下季合约，先本季后次季",
			PrimaryMarket:       "coinm",
			PrimarySymbol:       "btc_current",
			PrimaryDirection:    "close_short",
			DerivativeMarket:    "coinm",
			DerivativeSymbol:    "btc_next",
			DerivativeDirection: "rollover",
			DerivativeOrderType: "limit",
			PriceExpression:     "d_price + 40",
			QuantityExpression:  "delta_value / 100",
			CreatedBy:           "system",
		},
		{
			Name:                "roll-btc_current_to_next_reverse",
			Enabled:             true,
			Priority:            10,
			Description:         "BTC本季合约滚仓到下季合约，后本季先次季",
			PrimaryMarket:       "coinm",
			PrimarySymbol:       "btc_next",
			PrimaryDirection:    "short",
			DerivativeMarket:    "coinm",
			DerivativeSymbol:    "btc_current",
			DerivativeDirection: "rollover",
			DerivativeOrderType: "limit",
			PriceExpression:     "d_price - 40",
			QuantityExpression:  "delta_value / 100",
			CreatedBy:           "system",
		},
	}

	for _, rule := range rules {
		// 使用 FirstOrCreate 避免重复插入
		result := db.Where("name = ?", rule.Name).FirstOrCreate(&rule)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			log.Printf("Created derivative rule: %s", rule.Name)
		}
	}

	return nil
}
