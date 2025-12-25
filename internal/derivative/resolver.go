// internal/derivative/resolver.go

package derivative

import (
	"context"
	"fmt"
	"strings"

	"github.com/eyes2near/b-trading/internal/binance"
	"github.com/eyes2near/b-trading/internal/models"
)

// SymbolResolver Symbol 解析器
type SymbolResolver struct {
	binanceClient binance.Client
}

// NewSymbolResolver 创建解析器
func NewSymbolResolver(binanceClient binance.Client) *SymbolResolver {
	return &SymbolResolver{binanceClient: binanceClient}
}

// ResolveResult 解析结果
type ResolveResult struct {
	Symbol     string // 实际交易 symbol，如 BTCUSD_251226
	SymbolType string // 类型标识，如 btc-current
	Base       string // 基础货币，如 BTC
}

// Resolve 解析 symbol 模板
func (r *SymbolResolver) Resolve(ctx context.Context, symbolPattern string, primaryOrder *models.Order) (*ResolveResult, error) {
	// 提取主订单的 base 货币
	primaryBase := ExtractBaseCurrency(primaryOrder.FullSymbol)

	// 替换模板变量 {base}
	resolved := strings.ReplaceAll(symbolPattern, "{base}", primaryBase)
	resolved = strings.ReplaceAll(resolved, "{BASE}", primaryBase)

	// 处理 coinm 语义 symbol
	if strings.Contains(resolved, "_") {
		parts := strings.Split(resolved, "_")
		if len(parts) == 2 {
			base := strings.ToUpper(parts[0])
			contractType := strings.ToLower(parts[1])

			// 查询实际合约 symbol
			quarterResp, err := r.binanceClient.CoinMGetQuarterSymbols(ctx, base)
			if err != nil {
				return nil, fmt.Errorf("failed to get quarter symbols for %s: %w", base, err)
			}

			var actualSymbol string
			switch contractType {
			case "current":
				actualSymbol = quarterResp.Current.Symbol
			case "next":
				actualSymbol = quarterResp.Next.Symbol
			default:
				return nil, fmt.Errorf("unknown contract type: %s", contractType)
			}

			return &ResolveResult{
				Symbol:     actualSymbol,
				SymbolType: fmt.Sprintf("%s-%s", strings.ToLower(base), contractType),
				Base:       base,
			}, nil
		}
	}

	// 现货 symbol，直接使用
	return &ResolveResult{
		Symbol:     strings.ToUpper(resolved),
		SymbolType: strings.ToUpper(resolved),
		Base:       primaryBase,
	}, nil
}

// ExtractBaseCurrency 从 symbol 提取基础货币
// 例如: BTCUSDT -> BTC, BTCUSD_251226 -> BTC
func ExtractBaseCurrency(symbol string) string {
	// 处理 BTCUSD_251226 格式
	if idx := strings.Index(symbol, "_"); idx > 0 {
		symbol = symbol[:idx]
	}

	// 移除常见的计价货币后缀
	quotes := []string{"USDT", "BUSD", "USD", "PERP"}
	result := symbol
	for _, q := range quotes {
		if strings.HasSuffix(result, q) {
			result = result[:len(result)-len(q)]
			break
		}
	}

	return strings.ToUpper(result)
}
