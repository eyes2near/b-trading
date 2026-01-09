// internal/service/order_service.go

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/eyes2near/b-trading/internal/binance"
	"github.com/eyes2near/b-trading/internal/config"
	"github.com/eyes2near/b-trading/internal/database"
	"github.com/eyes2near/b-trading/internal/derivative"
	"github.com/eyes2near/b-trading/internal/models"
	"github.com/eyes2near/b-trading/internal/notify"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ErrOptimisticLock 乐观锁冲突错误
var ErrOptimisticLock = fmt.Errorf("optimistic lock conflict")

// FlowCompletionChecker 流程完成检查接口（避免循环依赖）
type FlowCompletionChecker interface {
	CheckFlowCompletion(ctx context.Context, flowID uint) error
}

// OrderService 订单服务接口
type OrderService interface {
	// SubmitOrder 提交订单到 Binance
	SubmitOrder(ctx context.Context, order *models.Order) (*binance.OrderInfo, error)

	// CreateTrackJob 创建订单追踪任务（带重试）
	CreateTrackJob(ctx context.Context, order *models.Order) error

	// SubmitDerivativeOrder 创建并提交衍生订单
	SubmitDerivativeOrder(ctx context.Context, primaryOrder *models.Order, fillEvent *models.FillEvent, params *derivative.DerivativeOrderParams) error

	// CancelOrder 取消订单
	CancelOrder(ctx context.Context, order *models.Order) error

	// SetFlowCompletionChecker 设置流程完成检查器（用于打破循环依赖）
	SetFlowCompletionChecker(checker FlowCompletionChecker)

	// HandleImmediateFill 处理订单立即成交的情况
	HandleImmediateFill(ctx context.Context, order *models.Order, binanceOrder *binance.OrderInfo) error
}

type orderServiceImpl struct {
	cfg              *config.Config
	binanceClient    binance.Client
	orderRepo        database.OrderRepository
	fillRepo         database.FillEventRepository
	ruleRepo         database.DerivativeRuleRepository
	flowRepo         database.TradingFlowRepository
	fillProcessor    FillProcessor
	audit            AuditService
	notifier         *notify.Notifier
	derivativeEngine derivative.Engine
	flowCompletion   FlowCompletionChecker
}

// NewOrderService 创建订单服务
func NewOrderService(
	cfg *config.Config,
	binanceClient binance.Client,
	orderRepo database.OrderRepository,
	fillRepo database.FillEventRepository,
	ruleRepo database.DerivativeRuleRepository,
	flowRepo database.TradingFlowRepository,
	fillProcessor FillProcessor,
	audit AuditService,
	notifier *notify.Notifier,
	derivativeEngine derivative.Engine,
) OrderService {
	return &orderServiceImpl{
		cfg:              cfg,
		binanceClient:    binanceClient,
		orderRepo:        orderRepo,
		fillRepo:         fillRepo,
		ruleRepo:         ruleRepo,
		flowRepo:         flowRepo,
		fillProcessor:    fillProcessor,
		audit:            audit,
		notifier:         notifier,
		derivativeEngine: derivativeEngine,
	}
}

// SetFlowCompletionChecker 设置流程完成检查器
func (s *orderServiceImpl) SetFlowCompletionChecker(checker FlowCompletionChecker) {
	s.flowCompletion = checker
}

func (s *orderServiceImpl) SubmitOrder(ctx context.Context, order *models.Order) (*binance.OrderInfo, error) {
	var binanceOrder *binance.OrderInfo
	var err error

	if order.MarketType == models.MarketTypeSpot {
		req := binance.SpotOrderRequest{
			Symbol:        order.FullSymbol,
			Side:          models.DirectionToSide(order.Direction),
			Type:          models.OrderTypeToString(order.OrderType),
			Quantity:      order.Quantity,
			ClientOrderID: order.OrderUUID,
		}
		if order.OrderType == models.OrderTypeLimit && order.Price.Valid {
			req.Price = order.Price.String
		}
		binanceOrder, err = s.binanceClient.SpotCreateOrder(ctx, req)
	} else {
		req := binance.CoinMOrderRequest{
			Symbol:        order.FullSymbol,
			Side:          models.DirectionToSide(order.Direction),
			Type:          models.OrderTypeToString(order.OrderType),
			Quantity:      order.Quantity,
			PositionSide:  models.DirectionToPositionSide(order.Direction),
			ClientOrderID: order.OrderUUID,
		}
		if order.OrderType == models.OrderTypeLimit && order.Price.Valid {
			req.Price = order.Price.String
		}
		binanceOrder, err = s.binanceClient.CoinMCreateOrder(ctx, req)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to submit order to binance: %w", err)
	}

	// 更新订单信息
	order.BinanceOrderID = strconv.FormatInt(binanceOrder.OrderID, 10)
	order.BinanceClientOrderID = binanceOrder.ClientOrderID
	order.Status = models.MapBinanceStatus(binanceOrder.Status)
	order.FilledQuantity = binanceOrder.ExecutedQty
	order.SubmittedAt = sql.NullTime{Time: time.Now(), Valid: true}

	if binanceOrder.ExecutedQty != "" {
		order.AvgFillPrice = binanceOrder.AvgPrice
	}

	rawResp, _ := json.Marshal(binanceOrder)
	order.BinanceRawResponse = string(rawResp)

	// 若立即终态
	if models.IsTerminalOrderStatus(binanceOrder.Status) {
		order.CompletedAt = sql.NullTime{Time: time.Now(), Valid: true}
		order.TrackJobStatus = "terminal"
	}

	if err := s.orderRepo.Update(ctx, order); err != nil {
		log.Printf("Failed to update order after submission: %v", err)
	}

	s.audit.LogEvent(ctx, order.FlowID, order.ID, models.LogTypeOrderSubmit, models.LogSeverityInfo,
		"Order submitted to Binance", map[string]interface{}{
			"binance_order_id": binanceOrder.OrderID,
			"client_order_id":  binanceOrder.ClientOrderID,
			"status":           binanceOrder.Status,
			"executed_qty":     binanceOrder.ExecutedQty,
		})

	return binanceOrder, nil
}

func (s *orderServiceImpl) CreateTrackJob(ctx context.Context, order *models.Order) error {
	binanceOrderID, err := strconv.ParseInt(order.BinanceOrderID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid binance order id: %w", err)
	}

	req := binance.TrackWebhookRequest{
		Symbol:     order.FullSymbol,
		OrderID:    binanceOrderID,
		WebhookURL: s.cfg.GetWebhookURL(),
	}

	resp, err := s.executeTrackJobWithRetry(ctx, order, req)
	if err != nil {
		return err
	}

	// 处理 snapshot
	if err := s.applyTrackWebhookResponse(ctx, order, resp); err != nil {
		return fmt.Errorf("failed to apply track webhook response: %w", err)
	}

	s.audit.LogEvent(ctx, order.FlowID, order.ID, models.LogTypeSystem, models.LogSeverityInfo,
		"Track job created", map[string]interface{}{
			"job_id":   resp.JobID,
			"status":   resp.Status,
			"snap_len": 1,
			"note":     resp.Note,
		})

	return nil
}

func (s *orderServiceImpl) executeTrackJobWithRetry(ctx context.Context, order *models.Order, req binance.TrackWebhookRequest) (*binance.TrackWebhookResponse, error) {
	const (
		maxRetries       = 5
		baseDelay        = 1 * time.Second
		maxDelay         = 30 * time.Second
		initialJitterMax = 2 * time.Second
	)

	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		var resp *binance.TrackWebhookResponse
		var err error

		if order.MarketType == models.MarketTypeSpot {
			resp, err = s.binanceClient.SpotTrackWebhook(ctx, req)
		} else {
			resp, err = s.binanceClient.CoinMTrackWebhook(ctx, req)
		}

		if err == nil {
			return resp, nil
		}

		lastErr = err

		var clientErr *binance.ClientError
		if !errors.As(err, &clientErr) {
			s.notifier.NotifyTrackJobFailed(0)
			return nil, err
		}

		statusCode := clientErr.StatusCode

		// 400/401: 客户端错误，不重试
		if statusCode == 400 || statusCode == 401 {
			s.notifier.NotifyTrackJobFailed(statusCode)
			return nil, err
		}

		// 5xx: 服务端错误，重试
		if statusCode >= 500 {
			if attempt == maxRetries {
				s.notifier.NotifyTrackJobFailed(statusCode)
				return nil, err
			}

			delay := s.calculateBackoffDelay(attempt, baseDelay, maxDelay, initialJitterMax)
			log.Printf("Track job creation failed (attempt %d/%d), retrying in %v: %v",
				attempt+1, maxRetries+1, delay, err)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
				continue
			}
		}

		s.notifier.NotifyTrackJobFailed(statusCode)
		return nil, err
	}

	return nil, lastErr
}

func (s *orderServiceImpl) calculateBackoffDelay(attempt int, baseDelay, maxDelay, initialJitterMax time.Duration) time.Duration {
	delay := baseDelay * time.Duration(1<<attempt)
	if delay > maxDelay {
		delay = maxDelay
	}

	jitter := time.Duration(rand.Float64() * float64(delay) * 0.3)
	delay += jitter

	if attempt == 0 {
		initialJitter := time.Duration(rand.Float64() * float64(initialJitterMax))
		delay += initialJitter
	}

	return delay
}

const maxTrackApplyRetries = 3

func (s *orderServiceImpl) applyTrackWebhookResponse(ctx context.Context, order *models.Order, resp *binance.TrackWebhookResponse) error {
	// 1. 处理 snapshot
	executedQty, err := decimal.NewFromString(strings.TrimSpace(resp.Order.ExecutedQty))
	if err == nil && executedQty.GreaterThan(decimal.Zero) {
		for range maxTrackApplyRetries {
			cur, err := s.orderRepo.GetByID(ctx, order.ID)
			if err != nil {
				return err
			}

			fillEvent, err := s.fillProcessor.ProcessSnapshot(ctx, cur, resp)
			if err == ErrOptimisticLock {
				continue
			} else if err != nil {
				return err
			}

			// 若有新增成交且为主订单，触发衍生订单
			if fillEvent != nil && cur.OrderRole == models.OrderRolePrimary && s.cfg.Derivative.Enabled && s.derivativeEngine != nil {
				s.triggerDerivativeFromFill(ctx, cur, fillEvent)
			}

			// 同步内存状态
			*order = *cur
			break
		}
	}
	// 2. 更新 track job 字段
	for range maxTrackApplyRetries {
		cur, err := s.orderRepo.GetByID(ctx, order.ID)
		if err != nil {
			return err
		}

		updates := map[string]interface{}{
			"track_job_id":   resp.JobID,
			"webhook_secret": resp.WebhookSecret,
			"version":        cur.Version + 1,
		}

		if cur.TrackJobStatus == "terminal" {
			updates["track_job_status"] = "terminal"
		} else {
			updates["track_job_status"] = resp.Status
		}

		rows, err := s.orderRepo.UpdateWithVersion(ctx, cur.ID, cur.Version, updates)
		if err != nil {
			return err
		}
		if rows == 0 {
			continue
		}

		// 同步内存状态
		order.TrackJobID = resp.JobID
		order.WebhookSecret = resp.WebhookSecret
		order.TrackJobStatus = updates["track_job_status"].(string)
		order.Version = cur.Version + 1
		return nil
	}

	return fmt.Errorf("applyTrackWebhookResponse: max retries exceeded for order_id=%d", order.ID)
}

func (s *orderServiceImpl) triggerDerivativeFromFill(ctx context.Context, order *models.Order, fillEvent *models.FillEvent) {
	// 获取 Flow 信息（包含规则配置）
	flow, err := s.flowRepo.GetByID(ctx, order.FlowID)
	if err != nil {
		log.Printf("Failed to get flow for derivative trigger: %v", err)
		return
	}

	// 使用 Flow 级别规则（优先）或全局规则
	derivativeParams, err := s.derivativeEngine.GenerateDerivativeOrderForFlow(ctx, derivative.FillParams{
		PrimaryOrder:  order,
		FillPrice:     fillEvent.FillPrice,
		Delta:         fillEvent.FillQuantity,
		CumulativeQty: order.FilledQuantity,
	}, flow)

	if err != nil {
		log.Printf("Failed to generate derivative order: %v", err)
		s.notifier.NotifyDerivativeFailure(order.ID, order.FullSymbol, "", "生成衍生订单", err.Error())
		return
	}

	if derivativeParams == nil {
		s.notifier.NotifyNoMatchingRule(order.ID, string(order.MarketType), order.FullSymbol)
		return
	}

	if err := s.SubmitDerivativeOrder(ctx, order, fillEvent, derivativeParams); err != nil {
		log.Printf("Failed to submit derivative order: %v", err)
	}
}

func (s *orderServiceImpl) SubmitDerivativeOrder(ctx context.Context, primaryOrder *models.Order, fillEvent *models.FillEvent, params *derivative.DerivativeOrderParams) error {
	// 1. 创建衍生订单记录
	derivativeOrder := &models.Order{
		OrderUUID:             uuid.New().String(),
		FlowID:                primaryOrder.FlowID,
		ParentOrderID:         sql.NullInt64{Int64: int64(primaryOrder.ID), Valid: true},
		OrderRole:             models.OrderRoleDerivative,
		MarketType:            params.Market,
		SymbolType:            params.SymbolType,
		FullSymbol:            params.Symbol,
		Direction:             params.Direction,
		OrderType:             params.OrderType,
		Quantity:              params.Quantity,
		Price:                 sql.NullString{String: params.Price, Valid: params.OrderType == models.OrderTypeLimit},
		Status:                models.OrderStatusPending,
		FilledQuantity:        "0",
		TriggerFillEventID:    sql.NullInt64{Int64: int64(fillEvent.ID), Valid: true},
		PriceCalculationLogic: fmt.Sprintf("Rule: %s, Context: %v", params.RuleName, params.EvaluationContext),
	}

	if err := s.orderRepo.Create(ctx, derivativeOrder); err != nil {
		s.notifier.NotifyDerivativeFailure(primaryOrder.ID, params.Symbol, params.RuleName, "创建订单记录", err.Error())
		return fmt.Errorf("failed to create derivative order record: %w", err)
	}

	// 2. 更新规则日志
	if params.LogID > 0 && s.ruleRepo != nil {
		_ = s.ruleRepo.UpdateLogDerivativeOrderID(ctx, params.LogID, derivativeOrder.ID)
	}

	// 3. 审计日志
	s.audit.LogEvent(ctx, primaryOrder.FlowID, derivativeOrder.ID, models.LogTypeDerivativeTrigger, models.LogSeverityInfo,
		"Derivative order created", map[string]interface{}{
			"rule_name":       params.RuleName,
			"rule_id":         params.RuleID,
			"parent_order_id": primaryOrder.ID,
			"fill_event_id":   fillEvent.ID,
			"symbol":          params.Symbol,
			"direction":       string(params.Direction),
			"quantity":        params.Quantity,
			"price":           params.Price,
			"eval_context":    params.EvaluationContext,
		})

	// 4. 更新 FillEvent
	fillEvent.HasTriggeredDerivative = true
	fillEvent.DerivativeOrderID = sql.NullInt64{Int64: int64(derivativeOrder.ID), Valid: true}
	_ = s.fillRepo.Update(ctx, fillEvent)

	// 5. 提交到 Binance
	binanceOrder, err := s.SubmitOrder(ctx, derivativeOrder)
	if err != nil {
		s.orderRepo.UpdateStatus(ctx, derivativeOrder.ID, models.OrderStatusRejected, err.Error())
		s.notifier.NotifyDerivativeFailure(primaryOrder.ID, derivativeOrder.FullSymbol, params.RuleName, "提交到Binance", err.Error())
		return err
	}

	// 6. 若非立即终态，创建 Track Job
	if !models.IsTerminalOrderStatus(binanceOrder.Status) {
		if err := s.CreateTrackJob(ctx, derivativeOrder); err != nil {
			log.Printf("Failed to create track job for derivative order: %v", err)
		}
	} else if s.flowCompletion != nil {
		_ = s.flowCompletion.CheckFlowCompletion(ctx, derivativeOrder.FlowID)
	}

	return nil
}

func (s *orderServiceImpl) CancelOrder(ctx context.Context, order *models.Order) error {
	if order.BinanceOrderID == "" {
		return nil
	}

	binanceOrderID, _ := strconv.ParseInt(order.BinanceOrderID, 10, 64)

	var err error
	if order.MarketType == models.MarketTypeSpot {
		_, err = s.binanceClient.SpotCancelOrder(ctx, order.FullSymbol, binanceOrderID)
	} else {
		_, err = s.binanceClient.CoinMCancelOrder(ctx, order.FullSymbol, binanceOrderID)
	}

	if err != nil {
		log.Printf("Failed to cancel order on Binance: %v", err)
	}

	// 取消 Track Job
	if order.TrackJobID != "" {
		_ = s.binanceClient.CancelTrackJob(ctx, order.TrackJobID)
	}

	return s.orderRepo.UpdateStatus(ctx, order.ID, models.OrderStatusCancelled, "User cancelled")
}

func (s *orderServiceImpl) HandleImmediateFill(ctx context.Context, order *models.Order, binanceOrder *binance.OrderInfo) error {
	// 1. 计算成交价格
	fillPrice, deltaQty := CalculateFillPriceAndQty(order, binanceOrder.ExecutedQty, binanceOrder.AvgPrice)

	// 2. 解析成交数量
	executedQty, err := decimal.NewFromString(binanceOrder.ExecutedQty)
	if err != nil || executedQty.IsZero() {
		log.Printf("HandleImmediateFill: no executed qty for order %d", order.ID)
		return nil
	}

	// 3. 创建 FillEvent
	fillEvent := &models.FillEvent{
		FillUUID:       uuid.New().String(),
		OrderID:        order.ID,
		FlowID:         order.FlowID,
		FillPrice:      fillPrice,
		FillQuantity:   deltaQty.String(),
		ReceivedAt:     time.Now(),
		FillTime:       time.Now(),
		BinanceOrderID: order.BinanceOrderID,
		RawData:        order.BinanceRawResponse,
	}

	if err := s.fillRepo.Create(ctx, fillEvent); err != nil {
		return fmt.Errorf("failed to create fill event for immediate fill: %w", err)
	}

	// 4. 更新订单的平均成交价
	order.AvgFillPrice = binanceOrder.AvgPrice
	if err := s.orderRepo.Update(ctx, order); err != nil {
		log.Printf("Failed to update order avg_fill_price: %v", err)
	}

	// 5. 审计日志
	s.audit.LogEvent(ctx, order.FlowID, order.ID, models.LogTypeFillReceived, models.LogSeverityInfo,
		"Immediate fill received", map[string]interface{}{
			"fill_quantity":    executedQty.String(),
			"fill_price":       fillPrice,
			"binance_order_id": binanceOrder.OrderID,
			"status":           binanceOrder.Status,
			"source":           "immediate_fill",
		})

	// 6. 如果是主订单且启用衍生订单，触发衍生订单
	if order.OrderRole == models.OrderRolePrimary && s.cfg.Derivative.Enabled && s.derivativeEngine != nil {
		s.triggerDerivativeFromFill(ctx, order, fillEvent)
	}

	return nil
}
