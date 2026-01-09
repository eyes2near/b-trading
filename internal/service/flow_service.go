// internal/service/flow_service.go

package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/eyes2near/b-trading/internal/config"
	"github.com/eyes2near/b-trading/internal/database"
	"github.com/eyes2near/b-trading/internal/derivative"
	"github.com/eyes2near/b-trading/internal/models"
	"github.com/eyes2near/b-trading/internal/notify"
	"github.com/google/uuid"
)

type FlowService interface {
	CreateFlow(ctx context.Context, req CreateFlowRequest) (*models.TradingFlow, error)
	CancelFlow(ctx context.Context, flowID uint) error
	CheckFlowCompletion(ctx context.Context, flowID uint) error
}

// ErrNoMatchingRule 无匹配的衍生规则错误
type ErrNoMatchingRule struct {
	Market    string
	Symbol    string
	Direction string
}

func (e *ErrNoMatchingRule) Error() string {
	return fmt.Sprintf("no matching derivative rule for order (market=%s, symbol=%s, direction=%s)",
		e.Market, e.Symbol, e.Direction)
}

type CreateFlowRequest struct {
	MarketType     string
	Symbol         string
	SymbolType     string
	ContractType   string
	Direction      string
	OrderType      string
	Quantity       string
	Price          string
	DerivativeRule *models.FlowDerivativeRule
}

type flowServiceImpl struct {
	cfg              *config.Config
	flowRepo         database.TradingFlowRepository
	orderSvc         OrderService
	audit            AuditService
	notifier         *notify.Notifier
	derivativeEngine derivative.Engine
}

func NewFlowService(
	cfg *config.Config,
	flowRepo database.TradingFlowRepository,
	orderSvc OrderService,
	audit AuditService,
	notifier *notify.Notifier,
	derivativeEngine derivative.Engine,
) FlowService {
	svc := &flowServiceImpl{
		cfg:              cfg,
		flowRepo:         flowRepo,
		orderSvc:         orderSvc,
		audit:            audit,
		notifier:         notifier,
		derivativeEngine: derivativeEngine,
	}

	// 设置流程完成检查器（打破循环依赖）
	orderSvc.SetFlowCompletionChecker(svc)

	return svc
}

func (s *flowServiceImpl) CreateFlow(ctx context.Context, req CreateFlowRequest) (*models.TradingFlow, error) {
	flowUUID := uuid.New().String()
	orderUUID := uuid.New().String()

	flow := &models.TradingFlow{
		FlowUUID:  flowUUID,
		Status:    models.FlowStatusActive,
		CreatedBy: "system",
	}

	// =========================================================
	// 处理衍生规则配置
	// 优先级：Flow 规则 > 全局规则
	// =========================================================
	var ruleSource string // 用于审计日志

	if req.DerivativeRule != nil {
		// =====================================================
		// 情况 1：提供了 Flow 级别规则
		// =====================================================
		if err := req.DerivativeRule.Validate(); err != nil {
			return nil, fmt.Errorf("invalid derivative rule: %w", err)
		}

		// 验证表达式语法
		if req.DerivativeRule.Enabled && s.derivativeEngine != nil {
			tempRule := &models.DerivativeRule{
				PrimaryMarket:    req.MarketType,
				PrimarySymbol:    req.Symbol,
				PrimaryDirection: req.Direction,

				DerivativeMarket:    req.DerivativeRule.DerivativeMarket,
				DerivativeDirection: req.DerivativeRule.DerivativeDirection,
				DerivativeOrderType: req.DerivativeRule.DerivativeOrderType,
				PriceExpression:     req.DerivativeRule.PriceExpression,
				QuantityExpression:  req.DerivativeRule.QuantityExpression,
			}
			if err := s.derivativeEngine.ValidateRule(tempRule); err != nil {
				return nil, fmt.Errorf("invalid derivative rule: %w", err)
			}
		}

		// 序列化规则配置
		ruleJSON, err := req.DerivativeRule.ToJSON()
		if err != nil {
			return nil, fmt.Errorf("failed to serialize derivative rule: %w", err)
		}
		flow.DerivativeRuleConfig = ruleJSON

		if req.DerivativeRule.Enabled {
			ruleSource = "flow_rule"
			log.Printf("Flow %s will use custom flow-level rule (enabled=true)", flowUUID)
		} else {
			ruleSource = "flow_rule_disabled"
			log.Printf("Flow %s has flow-level rule but disabled", flowUUID)
		}

	} else if s.cfg.Derivative.Enabled && s.derivativeEngine != nil {
		// =====================================================
		// 情况 2：没有 Flow 规则，尝试匹配全局规则
		// =====================================================
		tempOrder := &models.Order{
			MarketType:   models.MarketType(req.MarketType),
			FullSymbol:   req.Symbol,
			SymbolType:   req.SymbolType,
			ContractType: models.ContractType(req.ContractType),
			Direction:    models.Direction(req.Direction),
		}

		exists, matchedRule := s.derivativeEngine.CheckRuleExists(ctx, tempOrder)
		if exists {
			// 匹配到全局规则
			ruleSource = fmt.Sprintf("global_rule:%s", matchedRule.Name)
			log.Printf("Flow %s will use global rule [%s]", flowUUID, matchedRule.Name)
		} else {
			// 没有匹配的全局规则
			if s.cfg.Derivative.RequireMatchingRule {
				// 配置要求必须有规则 → 拒绝创建
				log.Printf("Flow creation blocked: no derivative rule provided and no global rule matches (market=%s, symbol=%s, direction=%s)",
					req.MarketType, req.Symbol, req.Direction)

				s.notifier.NotifyNoMatchingRule(0, req.MarketType, req.Symbol)
				s.audit.LogEvent(ctx, 0, 0, models.LogTypeError, models.LogSeverityWarning,
					"Flow creation blocked: no matching derivative rule", map[string]interface{}{
						"market_type": req.MarketType,
						"symbol":      req.Symbol,
						"direction":   req.Direction,
					})

				return nil, &ErrNoMatchingRule{
					Market:    req.MarketType,
					Symbol:    req.Symbol,
					Direction: req.Direction,
				}
			} else {
				// 配置不要求必须有规则 → 允许创建，但记录日志
				ruleSource = "none"
				log.Printf("Flow %s has no matching derivative rule, derivatives will be skipped (market=%s, symbol=%s, direction=%s)",
					flowUUID, req.MarketType, req.Symbol, req.Direction)
			}
		}
	} else {
		// =====================================================
		// 情况 3：衍生功能未启用
		// =====================================================
		ruleSource = "derivative_disabled"
		log.Printf("Flow %s: derivative feature is disabled", flowUUID)
	}

	// 创建主订单对象
	order := &models.Order{
		OrderUUID:      orderUUID,
		OrderRole:      models.OrderRolePrimary,
		MarketType:     models.MarketType(req.MarketType),
		SymbolType:     req.SymbolType,
		ContractType:   models.ContractType(req.ContractType),
		FullSymbol:     req.Symbol,
		Direction:      models.Direction(req.Direction),
		OrderType:      models.OrderType(req.OrderType),
		Quantity:       req.Quantity,
		Price:          sql.NullString{String: req.Price, Valid: req.Price != ""},
		Status:         models.OrderStatusPending,
		FilledQuantity: "0",
	}

	// 原子创建 Flow + Order
	if err := s.flowRepo.CreateFlowWithOrder(ctx, flow, order); err != nil {
		s.notifier.NotifyPrimaryOrderFailed(flowUUID, req.Symbol, "创建订单记录: "+err.Error())
		return nil, fmt.Errorf("failed to create flow: %w", err)
	}

	s.audit.LogEvent(ctx, flow.ID, order.ID, models.LogTypeOrderCreate, models.LogSeverityInfo,
		"Created flow and primary order", map[string]interface{}{
			"flow_uuid":   flowUUID,
			"order_uuid":  orderUUID,
			"symbol":      req.Symbol,
			"direction":   req.Direction,
			"quantity":    req.Quantity,
			"rule_source": ruleSource,
		})

	// 提交订单到 Binance
	binanceOrder, err := s.orderSvc.SubmitOrder(ctx, order)
	if err != nil {
		s.flowRepo.UpdateStatus(ctx, flow.ID, models.FlowStatusCancelled, "Primary order rejected: "+err.Error())
		s.notifier.NotifyPrimaryOrderFailed(flowUUID, req.Symbol, "提交到Binance: "+err.Error())
		return nil, fmt.Errorf("failed to submit order: %w", err)
	}

	// =========================================================
	// 处理订单的不同终态情况
	// =========================================================
	if models.IsTerminalOrderStatus(binanceOrder.Status) {
		// 检查是否有成交量
		executedQty := strings.TrimSpace(binanceOrder.ExecutedQty)
		hasFill := executedQty != "" && executedQty != "0"

		switch binanceOrder.Status {
		case "FILLED":
			// 订单立即完全成交
			log.Printf("Primary order %d filled immediately", order.ID)

			if hasFill {
				if err := s.orderSvc.HandleImmediateFill(ctx, order, binanceOrder); err != nil {
					log.Printf("Warning: Failed to handle immediate fill: %v", err)
					s.audit.LogEvent(ctx, flow.ID, order.ID, models.LogTypeError, models.LogSeverityWarning,
						"Failed to handle immediate fill", map[string]interface{}{
							"error": err.Error(),
						})
				}
			}

		case "CANCELED", "REJECTED", "EXPIRED":
			// 订单被取消/拒绝/过期
			log.Printf("Primary order %d terminated with status: %s", order.ID, binanceOrder.Status)

			// 如果有部分成交，先处理成交
			if hasFill {
				if err := s.orderSvc.HandleImmediateFill(ctx, order, binanceOrder); err != nil {
					log.Printf("Warning: Failed to handle partial fill before termination: %v", err)
				}
			}

			reason := fmt.Sprintf("Primary order %s", binanceOrder.Status)
			if err := s.flowRepo.UpdateStatus(ctx, flow.ID, models.FlowStatusCancelled, reason); err != nil {
				log.Printf("Failed to update flow status: %v", err)
			}

			s.audit.LogEvent(ctx, flow.ID, order.ID, models.LogTypeError, models.LogSeverityError,
				fmt.Sprintf("Primary order %s", strings.ToLower(binanceOrder.Status)), map[string]interface{}{
					"binance_status": binanceOrder.Status,
					"executed_qty":   executedQty,
				})

			s.notifier.NotifyPrimaryOrderFailed(flowUUID, req.Symbol, reason)
		}
	} else {
		// 订单未立即完成，创建 Track Job 进行追踪
		if err := s.orderSvc.CreateTrackJob(ctx, order); err != nil {
			log.Printf("Warning: Failed to create track job: %v", err)
			s.audit.LogEvent(ctx, flow.ID, order.ID, models.LogTypeError, models.LogSeverityWarning,
				"Failed to create track job", map[string]interface{}{
					"error": err.Error(),
				})
		}
	}

	return s.flowRepo.GetByID(ctx, flow.ID)
}

func (s *flowServiceImpl) CancelFlow(ctx context.Context, flowID uint) error {
	flow, err := s.flowRepo.GetByID(ctx, flowID)
	if err != nil {
		return fmt.Errorf("flow not found: %w", err)
	}

	for i := range flow.Orders {
		order := &flow.Orders[i]
		if models.IsActiveOrderStatus(order.Status) {
			_ = s.orderSvc.CancelOrder(ctx, order)
		}
	}

	if err := s.flowRepo.UpdateStatus(ctx, flowID, models.FlowStatusCancelled, "User cancelled"); err != nil {
		return err
	}

	s.audit.LogEvent(ctx, flowID, 0, models.LogTypeFlowComplete, models.LogSeverityInfo, "Flow cancelled", nil)
	return nil
}

func (s *flowServiceImpl) CheckFlowCompletion(ctx context.Context, flowID uint) error {
	flow, err := s.flowRepo.GetByID(ctx, flowID)
	if err != nil {
		return err
	}

	if flow.Status != models.FlowStatusActive {
		return nil
	}

	allTerminal := true
	for _, order := range flow.Orders {
		if !models.IsTerminalOrderStatus(string(order.Status)) {
			allTerminal = false
			break
		}
	}

	if allTerminal {
		if err := s.flowRepo.UpdateStatus(ctx, flowID, models.FlowStatusCompleted, "All orders completed"); err != nil {
			return err
		}
		s.audit.LogEvent(ctx, flowID, 0, models.LogTypeFlowComplete, models.LogSeverityInfo,
			"Flow completed - all orders in terminal state", nil)
	}

	return nil
}
