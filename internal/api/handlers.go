// internal/api/handlers.go

package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/eyes2near/b-trading/internal/binance"
	"github.com/eyes2near/b-trading/internal/config"
	"github.com/eyes2near/b-trading/internal/database"
	"github.com/eyes2near/b-trading/internal/derivative"
	"github.com/eyes2near/b-trading/internal/models"
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

// CreateFlow 处理创建交易流程请求 (POST /api/flows)
func (h *Handler) CreateFlow(c *gin.Context) {
	var reqDTO CreateFlowRequest
	if err := c.ShouldBindJSON(&reqDTO); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}

	//归一化market_type
	reqDTO.MarketType = string(models.NormalizeMarketKey(reqDTO.MarketType))

	// 构造 Service 层请求
	// 注意：前端传入的 symbol_type 如果为空，对于 Coin-M 可能需要特殊处理，
	// 但根据 API 文档，symbol_type 是顶层字段。
	svcReq := service.CreateFlowRequest{
		MarketType:   reqDTO.MarketType,
		Symbol:       reqDTO.Symbol,
		SymbolType:   reqDTO.SymbolType,
		ContractType: reqDTO.ContractType, // DTO 中可选
		Direction:    reqDTO.Direction,
		OrderType:    reqDTO.OrderType,
		Quantity:     reqDTO.Quantity,
		Price:        reqDTO.Price,
	}

	// 处理衍生规则
	if reqDTO.DerivativeRule != nil {
		// 方式1：直接使用自定义规则
		if reqDTO.DerivativeRule.PriceExpression != "" || reqDTO.DerivativeRule.QuantityExpression != "" {
			svcReq.DerivativeRule = reqDTO.DerivativeRule.ToFlowDerivativeRule()
		} else if reqDTO.DerivativeRule.TemplateRuleID != nil || reqDTO.DerivativeRule.TemplateRuleName != "" {
			// 方式2：基于模板创建
			rule, err := h.resolveTemplateRule(c.Request.Context(), reqDTO.DerivativeRule)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to resolve template rule: " + err.Error()})
				return
			}
			svcReq.DerivativeRule = rule
		}
	}

	// 针对 Coin-M 的兼容性逻辑 (如果前端未传 symbol_type)
	if strings.ToLower(svcReq.MarketType) == "coin-m" && svcReq.SymbolType == "" {
		// 尝试构造一个默认的 SymbolType，或者报错
		c.JSON(http.StatusBadRequest, gin.H{"error": "symbol_type is required for Coin-M market"})
		return
	}
	// 针对 Spot，SymbolType 通常等于 Symbol
	if strings.ToLower(svcReq.MarketType) == "spot" && svcReq.SymbolType == "" {
		svcReq.SymbolType = svcReq.Symbol
	}

	flow, err := h.flowService.CreateFlow(c.Request.Context(), svcReq)
	if err != nil {
		// 检查是否为规则不匹配错误
		var noRuleErr *service.ErrNoMatchingRule
		if errors.As(err, &noRuleErr) {
			log.Printf("Flow creation rejected: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "无法创建订单：未找到匹配的衍生规则",
				"details": gin.H{
					"market":    noRuleErr.Market,
					"symbol":    noRuleErr.Symbol,
					"direction": noRuleErr.Direction,
				},
			})
			return
		}

		log.Printf("Failed to create flow: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建订单失败: " + err.Error()})
		return
	}

	log.Printf("Flow created successfully: %s", flow.FlowUUID)
	c.JSON(http.StatusCreated, mapFlowToResponse(flow))
}

// CancelFlow 取消流程 (POST /api/flows/:id/cancel)
func (h *Handler) CancelFlow(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid flow ID"})
		return
	}

	if err := h.flowService.CancelFlow(c.Request.Context(), uint(id)); err != nil {
		log.Printf("Failed to cancel flow: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error cancelling flow: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Flow cancelled successfully"})
}

// GetActiveFlows 获取活跃流程列表 (GET /api/flows)
func (h *Handler) GetActiveFlows(c *gin.Context) {
	// 1. 获取活跃 Flow 的基本列表 (repository 中 ListActive 未预加载 Orders)
	basicFlows, err := h.flowRepo.ListActive(c.Request.Context(), 50, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error fetching flows: " + err.Error()})
		return
	}

	// 2. 数据组装 (Hydration)
	// 由于 ListActive 没有 Preload Orders，而 API 响应需要 Orders 摘要，
	// 这里通过循环调用 GetByID (它包含 Preload) 来获取完整信息。
	// 注意：在生产环境高并发下这会导致 N+1 查询，建议后续优化 Repository 层增加 FindAllWithOrders 方法。
	var fullFlows []models.TradingFlow
	for _, f := range basicFlows {
		// 利用缓存或数据库查询完整信息
		fullDetails, err := h.flowRepo.GetByID(c.Request.Context(), f.ID)
		if err == nil {
			fullFlows = append(fullFlows, *fullDetails)
		} else {
			// 如果获取详情失败，降级使用基本信息（虽然 Orders 为空）
			fullFlows = append(fullFlows, f)
		}
	}

	c.JSON(http.StatusOK, mapFlowsToResponse(fullFlows))
}

// GetFlowDetail 获取流程详情 (GET /api/flows/:id)
func (h *Handler) GetFlowDetail(c *gin.Context) {
	idStr := c.Param("id")
	id, _ := strconv.Atoi(idStr)

	// GetByID 已经预加载了 Orders 和 FillEvents
	flow, err := h.flowRepo.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Flow not found"})
		return
	}

	c.JSON(http.StatusOK, mapFlowToResponse(flow))
}

// GetSpotPrice 查询现货价格 (GET /api/prices/spot/:symbol)
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

// GetCoinMPrice 查询币本位价格 (GET /api/prices/coinm/:symbol)
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

// GetQuarterSymbols 查询季度合约信息 (GET /api/coinm/quarter-symbols/:base)
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

// resolveTemplateRule 从模板解析规则
func (h *Handler) resolveTemplateRule(ctx context.Context, input *FlowDerivativeRuleInput) (*models.FlowDerivativeRule, error) {
	var templateRule *models.DerivativeRule
	var err error

	if input.TemplateRuleID != nil {
		templateRule, err = h.ruleRepo.GetByID(ctx, *input.TemplateRuleID)
	} else if input.TemplateRuleName != "" {
		templateRule, err = h.ruleRepo.GetByName(ctx, input.TemplateRuleName)
	}

	if err != nil {
		return nil, fmt.Errorf("template rule not found: %w", err)
	}

	// 从模板创建 Flow 规则
	flowRule := &models.FlowDerivativeRule{
		Enabled:             templateRule.Enabled,
		SourceType:          "template",
		TemplateID:          templateRule.ID,
		TemplateName:        templateRule.Name,
		DerivativeMarket:    templateRule.DerivativeMarket,
		DerivativeSymbol:    templateRule.DerivativeSymbol,
		DerivativeDirection: templateRule.DerivativeDirection,
		DerivativeOrderType: templateRule.DerivativeOrderType,
		PriceExpression:     templateRule.PriceExpression,
		QuantityExpression:  templateRule.QuantityExpression,
	}

	// 应用覆盖
	if input.Overrides != nil {
		if v, ok := input.Overrides["derivative_market"]; ok {
			flowRule.DerivativeMarket = v
		}
		if v, ok := input.Overrides["derivative_symbol"]; ok {
			flowRule.DerivativeSymbol = v
		}
		if v, ok := input.Overrides["derivative_direction"]; ok {
			flowRule.DerivativeDirection = v
		}
		if v, ok := input.Overrides["derivative_order_type"]; ok {
			flowRule.DerivativeOrderType = v
		}
		if v, ok := input.Overrides["price_expression"]; ok {
			flowRule.PriceExpression = v
		}
		if v, ok := input.Overrides["quantity_expression"]; ok {
			flowRule.QuantityExpression = v
		}
	}

	return flowRule, nil
}
