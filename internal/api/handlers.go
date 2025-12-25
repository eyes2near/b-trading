// internal/api/handlers.go - 更新 Handler 结构体和方法

package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eyes2near/b-trading/internal/binance"
	"github.com/eyes2near/b-trading/internal/config"
	"github.com/eyes2near/b-trading/internal/database"
	"github.com/eyes2near/b-trading/internal/derivative"
	"github.com/eyes2near/b-trading/internal/service"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	flowRepo         database.TradingFlowRepository
	orderRepo        database.OrderRepository
	ruleRepo         database.DerivativeRuleRepository
	cfg              *config.Config
	binanceClient    binance.Client
	flowService      service.FlowService
	derivativeEngine derivative.Engine
}

func NewHandler(
	flowRepo database.TradingFlowRepository,
	orderRepo database.OrderRepository,
	cfg *config.Config,
	binanceClient binance.Client,
	flowService service.FlowService,
	ruleRepo database.DerivativeRuleRepository,
	derivativeEngine derivative.Engine,
) *Handler {
	return &Handler{
		flowRepo:         flowRepo,
		orderRepo:        orderRepo,
		ruleRepo:         ruleRepo,
		cfg:              cfg,
		binanceClient:    binanceClient,
		flowService:      flowService,
		derivativeEngine: derivativeEngine,
	}
}

func (h *Handler) RenderDashboard(c *gin.Context) {
	log.Println("Handling / request, rendering dashboard.html")

	c.HTML(http.StatusOK, "dashboard.html", gin.H{
		"Env":         h.cfg.App.Env,
		"SpotSymbols": h.cfg.Markets.SpotSymbols,
		"CoinmBases":  h.cfg.Markets.CoinmBases,
	})
}

// CreateFlow 处理表单提交 - 使用 FlowService 真实下单
func (h *Handler) CreateFlow(c *gin.Context) {
	marketType := c.PostForm("market_type")
	qtyStr := c.PostForm("quantity")
	priceStr := c.PostForm("price")
	direction := c.PostForm("direction")
	orderType := c.PostForm("order_type")

	var symbolStr string
	var symbolType string
	var contractType string

	if strings.ToLower(marketType) == "spot" {
		symbolStr = strings.ToUpper(c.PostForm("symbol_spot"))
		symbolType = symbolStr
	} else if strings.ToLower(marketType) == "coin-m" {
		symbolStr = strings.ToUpper(c.PostForm("resolved_symbol"))
		baseCurrency := strings.ToUpper(c.PostForm("base_currency"))
		contractType = c.PostForm("contract_type")
		symbolType = baseCurrency + "-" + contractType

		if symbolStr == "" {
			c.String(http.StatusBadRequest, "Error: resolved_symbol is required for Coin-M")
			return
		}
	}

	if symbolStr == "" {
		c.String(http.StatusBadRequest, "Error: symbol is required")
		return
	}

	// 调用 FlowService 创建流程并下单
	req := service.CreateFlowRequest{
		MarketType:   marketType,
		Symbol:       symbolStr,
		SymbolType:   symbolType,
		ContractType: contractType,
		Direction:    direction,
		OrderType:    orderType,
		Quantity:     qtyStr,
		Price:        priceStr,
	}

	flow, err := h.flowService.CreateFlow(c.Request.Context(), req)
	if err != nil {
		// 检查是否为规则不匹配错误
		var noRuleErr *service.ErrNoMatchingRule
		if errors.As(err, &noRuleErr) {
			log.Printf("Flow creation rejected: %v", err)
			c.String(http.StatusBadRequest,
				"无法创建订单：未找到匹配的衍生规则 (市场=%s, 品种=%s, 方向=%s)。请先配置对应的衍生规则。",
				noRuleErr.Market, noRuleErr.Symbol, noRuleErr.Direction)
			return
		}

		log.Printf("Failed to create flow: %v", err)
		c.String(http.StatusInternalServerError, "创建订单失败: "+err.Error())
		return
	}

	log.Printf("Flow created successfully: %s", flow.FlowUUID)

	c.Header("HX-Trigger", "load")
	c.Status(http.StatusCreated)
}

// CancelFlow 取消流程
func (h *Handler) CancelFlow(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.String(http.StatusBadRequest, "Invalid flow ID")
		return
	}

	if err := h.flowService.CancelFlow(c.Request.Context(), uint(id)); err != nil {
		log.Printf("Failed to cancel flow: %v", err)
		c.String(http.StatusInternalServerError, "Error cancelling flow: "+err.Error())
		return
	}

	c.Header("HX-Trigger", "load")
	c.Status(http.StatusOK)
}

func (h *Handler) GetActiveFlows(c *gin.Context) {
	flows, err := h.flowRepo.GetActiveFlowsView(c.Request.Context(), 50, 0)
	if err != nil {
		c.String(http.StatusInternalServerError, "Error fetching flows")
		return
	}

	c.HTML(http.StatusOK, "flow_list.html", gin.H{
		"Flows": flows,
	})
}

func (h *Handler) GetFlowDetail(c *gin.Context) {
	idStr := c.Param("id")
	id, _ := strconv.Atoi(idStr)

	flow, err := h.flowRepo.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		c.String(http.StatusNotFound, "Flow not found")
		return
	}

	html := fmt.Sprintf(`
		<div class="fixed inset-0 bg-gray-600 bg-opacity-50 overflow-y-auto h-full w-full z-50 flex items-center justify-center" 
			 x-data @click.self="$el.remove()">
			<div class="relative bg-white dark:bg-dark-card rounded-lg shadow-xl w-3/4 max-w-4xl max-h-[90vh] flex flex-col">
				<div class="flex justify-between items-center p-5 border-b border-gray-200 dark:border-dark-border">
					<h3 class="text-xl font-bold text-gray-900 dark:text-white">流程详情: %s</h3>
					<button @click="$root.remove()" class="text-gray-400 hover:text-gray-600 dark:hover:text-gray-300">
						<svg class="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"></path></svg>
					</button>
				</div>
				<div class="p-6 overflow-y-auto">
					<div class="mb-4 bg-blue-50 dark:bg-blue-900/20 p-4 rounded border border-blue-100 dark:border-blue-800">
						<h4 class="font-bold text-blue-800 dark:text-blue-300">主订单 (Order A)</h4>
						<div class="grid grid-cols-3 gap-4 mt-2 text-sm text-gray-700 dark:text-gray-300">
							<div>ID: %d</div>
							<div>Status: %s</div>
							<div>Binance ID: %s</div>
						</div>
					</div>
					<div class="border-t border-gray-200 dark:border-dark-border pt-4 mt-4">
						<h4 class="font-bold text-gray-700 dark:text-gray-300 mb-2">订单列表</h4>
						<table class="min-w-full text-sm">
							<thead><tr class="text-left text-gray-500 dark:text-gray-400">
								<th class="pb-2">角色</th><th class="pb-2">Symbol</th><th class="pb-2">方向</th><th class="pb-2">数量</th><th class="pb-2">成交</th><th class="pb-2">状态</th>
							</tr></thead>
							<tbody class="text-gray-700 dark:text-gray-300">`,
		flow.FlowUUID, flow.Orders[0].ID, flow.Orders[0].Status, flow.Orders[0].BinanceOrderID)

	for _, order := range flow.Orders {
		html += fmt.Sprintf(`
								<tr class="border-t border-gray-100 dark:border-dark-border">
									<td class="py-2">%s</td>
									<td class="py-2 font-mono">%s</td>
									<td class="py-2">%s</td>
									<td class="py-2 font-mono">%s</td>
									<td class="py-2 font-mono">%s</td>
									<td class="py-2"><span class="px-2 py-1 rounded text-xs %s">%s</span></td>
								</tr>`,
			order.OrderRole, order.FullSymbol, order.Direction, order.Quantity, order.FilledQuantity,
			getStatusClass(string(order.Status)), order.Status)
	}

	html += `
							</tbody>
						</table>
					</div>
					<div class="border-t border-gray-200 dark:border-dark-border pt-4 mt-4">
						<h4 class="font-bold text-gray-700 dark:text-gray-300 mb-2">创建时间</h4>
						<p class="text-gray-500 dark:text-gray-400 text-sm">` + flow.CreatedAt.Format(time.RFC3339) + `</p>
					</div>
				</div>
			</div>
		</div>`

	c.Writer.WriteString(html)
}

func getStatusClass(status string) string {
	switch status {
	case "filled":
		return "bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-400"
	case "submitted", "partially_filled":
		return "bg-blue-100 text-blue-800 dark:bg-blue-900/30 dark:text-blue-400"
	case "cancelled", "rejected", "expired":
		return "bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-400"
	default:
		return "bg-gray-100 text-gray-800 dark:bg-gray-700 dark:text-gray-300"
	}
}

// 价格查询接口保持不变
func (h *Handler) GetSpotPrice(c *gin.Context) {
	symbol := strings.ToUpper(c.Param("symbol"))
	if symbol == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "symbol is required"})
		return
	}

	price, err := h.binanceClient.SpotGetPrice(c.Request.Context(), symbol)
	if err != nil {
		log.Printf("Failed to get spot price for %s: %v", symbol, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, price)
}

func (h *Handler) GetCoinMPrice(c *gin.Context) {
	symbol := strings.ToUpper(c.Param("symbol"))
	if symbol == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "symbol is required"})
		return
	}

	price, err := h.binanceClient.CoinMGetPrice(c.Request.Context(), symbol)
	if err != nil {
		log.Printf("Failed to get coinm price for %s: %v", symbol, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, price)
}

func (h *Handler) GetQuarterSymbols(c *gin.Context) {
	base := strings.ToUpper(c.Param("base"))
	if base == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "base is required"})
		return
	}

	symbols, err := h.binanceClient.CoinMGetQuarterSymbols(c.Request.Context(), base)
	if err != nil {
		log.Printf("Failed to get quarter symbols for %s: %v", base, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, symbols)
}
