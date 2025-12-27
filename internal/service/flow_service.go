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
	MarketType   string
	Symbol       string
	SymbolType   string
	ContractType string
	Direction    string
	OrderType    string
	Quantity     string
	Price        string
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

// internal/service/flow_service.go

func (s *flowServiceImpl) CreateFlow(ctx context.Context, req CreateFlowRequest) (*models.TradingFlow, error) {
	flowUUID := uuid.New().String()
	orderUUID := uuid.New().String()

	flow := &models.TradingFlow{
		FlowUUID:  flowUUID,
		Status:    models.FlowStatusActive,
		CreatedBy: "system",
	}

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

	// 检查衍生规则匹配
	if s.cfg.Derivative.Enabled && s.cfg.Derivative.RequireMatchingRule && s.derivativeEngine != nil {
		exists, matchedRule := s.derivativeEngine.CheckRuleExists(ctx, order)
		if !exists {
			log.Printf("Flow creation blocked: no matching derivative rule (market=%s, symbol=%s, direction=%s)",
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
		}
		log.Printf("Matched derivative rule [%s] for new order", matchedRule.Name)
	}

	// 原子创建 Flow + Order
	if err := s.flowRepo.CreateFlowWithOrder(ctx, flow, order); err != nil {
		s.notifier.NotifyPrimaryOrderFailed(flowUUID, req.Symbol, "创建订单记录: "+err.Error())
		return nil, fmt.Errorf("failed to create flow: %w", err)
	}

	s.audit.LogEvent(ctx, flow.ID, order.ID, models.LogTypeOrderCreate, models.LogSeverityInfo,
		"Created flow and primary order", map[string]interface{}{
			"flow_uuid":  flowUUID,
			"order_uuid": orderUUID,
			"symbol":     req.Symbol,
			"direction":  req.Direction,
			"quantity":   req.Quantity,
		})

	// 提交订单到 Binance
	binanceOrder, err := s.orderSvc.SubmitOrder(ctx, order)
	if err != nil {
		s.flowRepo.UpdateStatus(ctx, flow.ID, models.FlowStatusCancelled, "Primary order rejected: "+err.Error())
		s.notifier.NotifyPrimaryOrderFailed(flowUUID, req.Symbol, "提交到Binance: "+err.Error())
		return nil, fmt.Errorf("failed to submit order: %w", err)
	}

	// =========================================================
	// 关键修复：处理订单的不同终态情况
	// =========================================================
	if models.IsTerminalBinanceStatus(binanceOrder.Status) {
		executedQty := strings.TrimSpace(binanceOrder.ExecutedQty)
		if executedQty != "" && executedQty != "0" {
			// 处理立即成交：创建 FillEvent + 触发衍生订单
			if err := s.orderSvc.HandleImmediateFill(ctx, order, binanceOrder); err != nil {
				log.Printf("Warning: Failed to handle immediate fill: %v", err)
				s.audit.LogEvent(ctx, flow.ID, order.ID, models.LogTypeError, models.LogSeverityWarning,
					"Failed to handle immediate fill", map[string]interface{}{
						"error": err.Error(),
					})
			}
		}
		auditMsg := ""
		reason := fmt.Sprintf("Primary order %s", binanceOrder.Status)
		switch binanceOrder.Status {
		case "FILLED":
			// 订单立即完全成交（如市价单）
			log.Printf("Primary order %d filled immediately", order.ID)
			// 注意：此时主订单已完成，但衍生订单可能刚创建
			// Flow 状态会在所有订单完成后由 CheckFlowCompletion 更新
		case "CANCELED":
			auditMsg = "Primary order canceled"
			// 订单被拒绝或取消
			log.Printf("Primary order %d canceled with status: %s", order.ID, binanceOrder.Status)
		case "REJECTED":
			auditMsg = "Primary order rejected"
			// 订单被拒绝或取消
			log.Printf("Primary order %d rejected with status: %s", order.ID, binanceOrder.Status)
		case "EXPIRED":
			auditMsg = "Primary order expired"
			// 订单被拒绝或取消
			log.Printf("Primary order %d expired with status: %s", order.ID, binanceOrder.Status)
		}
		if auditMsg != "" {
			if err := s.flowRepo.UpdateStatus(ctx, flow.ID, models.FlowStatusCancelled, reason); err != nil {
				log.Printf("Failed to update flow status: %v", err)
			}
			s.audit.LogEvent(ctx, flow.ID, order.ID, models.LogTypeError, models.LogSeverityError,
				auditMsg, map[string]interface{}{
					"binance_status": binanceOrder.Status,
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
		if !models.IsTerminalOrderStatus(order.Status) {
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
