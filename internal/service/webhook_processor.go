// internal/service/webhook_processor.go

package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
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

// WebhookProcessor handles webhook events from the REST API service
type WebhookProcessor interface {
	ValidateSignature(timestamp, signature string, body []byte, secret string) bool
	CheckDuplicate(ctx context.Context, deliveryID string) (bool, error)
	ProcessEvent(ctx context.Context, deliveryID, jobID string, event *binance.WebhookEvent, rawBody []byte) error
}

// ErrOptimisticLock 乐观锁冲突错误
var ErrOptimisticLock = fmt.Errorf("optimistic lock conflict")

const maxOptimisticRetries = 3

type webhookProcessor struct {
	cfg              *config.Config
	binanceClient    binance.Client
	flowRepo         database.TradingFlowRepository
	orderRepo        database.OrderRepository
	fillRepo         database.FillEventRepository
	deliveryRepo     database.WebhookDeliveryRepository
	auditRepo        database.AuditLogRepository
	flowService      FlowService
	lockManager      *OrderLockManager
	derivativeEngine derivative.Engine
	ruleRepo         database.DerivativeRuleRepository
	notifier         *notify.Notifier
}

func NewWebhookProcessor(
	cfg *config.Config,
	binanceClient binance.Client,
	flowRepo database.TradingFlowRepository,
	orderRepo database.OrderRepository,
	fillRepo database.FillEventRepository,
	deliveryRepo database.WebhookDeliveryRepository,
	auditRepo database.AuditLogRepository,
	flowService FlowService,
	derivativeEngine derivative.Engine,
	ruleRepo database.DerivativeRuleRepository,
	notifier *notify.Notifier,
) WebhookProcessor {
	return &webhookProcessor{
		cfg:              cfg,
		binanceClient:    binanceClient,
		flowRepo:         flowRepo,
		orderRepo:        orderRepo,
		fillRepo:         fillRepo,
		deliveryRepo:     deliveryRepo,
		auditRepo:        auditRepo,
		flowService:      flowService,
		lockManager:      NewOrderLockManager(),
		derivativeEngine: derivativeEngine,
		ruleRepo:         ruleRepo,
		notifier:         notifier,
	}
}

func (p *webhookProcessor) ValidateSignature(timestamp, signature string, body []byte, secret string) bool {
	if secret == "" {
		return true
	}

	payload := timestamp + "." + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}

func (p *webhookProcessor) CheckDuplicate(ctx context.Context, deliveryID string) (bool, error) {
	return p.deliveryRepo.Exists(ctx, deliveryID)
}

func (p *webhookProcessor) ProcessEvent(ctx context.Context, deliveryID, jobID string, event *binance.WebhookEvent, rawBody []byte) error {
	// 1. Record the delivery
	delivery := &models.WebhookDelivery{
		DeliveryID: deliveryID,
		JobID:      jobID,
		EventID:    event.EventID,
		EventType:  event.EventType,
		Market:     event.Market,
		Symbol:     event.Symbol,
		OrderID:    event.OrderID,
		RawPayload: string(rawBody),
		ReceivedAt: time.Now(),
	}

	if err := p.deliveryRepo.Create(ctx, delivery); err != nil {
		return fmt.Errorf("failed to record delivery: %w", err)
	}

	// 2. Find the associated order
	order, err := p.orderRepo.GetByBinanceOrderID(ctx, strconv.FormatInt(event.OrderID, 10))
	if err != nil {
		delivery.ProcessErr = "order not found: " + err.Error()
		p.deliveryRepo.Update(ctx, delivery)
		return fmt.Errorf("order not found for binance_order_id=%d: %w", event.OrderID, err)
	}

	// 3. 使用订单级别锁 + 乐观锁处理事件
	var processErr error

	p.lockManager.WithLock(order.ID, func() error {
		// 在锁内重试处理（处理乐观锁冲突）
		for retry := 0; retry < maxOptimisticRetries; retry++ {
			// 重新获取最新的订单状态
			order, err = p.orderRepo.GetByBinanceOrderID(ctx, strconv.FormatInt(event.OrderID, 10))
			if err != nil {
				processErr = err
				return err
			}

			// 处理事件
			switch event.EventType {
			case "fill_update":
				processErr = p.handleFillUpdate(ctx, order, event, delivery)
			case "terminal":
				processErr = p.handleTerminal(ctx, order, event, delivery)
			default:
				processErr = fmt.Errorf("unknown event type: %s", event.EventType)
			}

			// 如果不是乐观锁冲突，退出重试
			if processErr != ErrOptimisticLock {
				return processErr
			}

			log.Printf("Optimistic lock conflict, retrying (%d/%d) for order %d",
				retry+1, maxOptimisticRetries, order.ID)
		}

		processErr = fmt.Errorf("max optimistic lock retries exceeded for order %d", order.ID)
		return processErr
	})

	// 4. Update delivery status
	delivery.ProcessedAt = sql.NullTime{Time: time.Now(), Valid: true}
	if processErr != nil {
		delivery.ProcessErr = processErr.Error()
	}
	p.deliveryRepo.Update(ctx, delivery)

	return processErr
}

func (p *webhookProcessor) handleFillUpdate(ctx context.Context, order *models.Order, event *binance.WebhookEvent, delivery *models.WebhookDelivery) error {
	log.Printf("Processing fill_update for order %d (binance_id=%d), delta=%s, cumulative=%s",
		order.ID, event.OrderID, event.Data.DeltaExecutedQty, event.Data.CumulativeExecutedQty)

	// ============================================
	// 业务幂等/顺序判定：基于 cumulative_executed_qty
	// ============================================
	newCum, err := decimal.NewFromString(event.Data.CumulativeExecutedQty)
	if err != nil {
		return fmt.Errorf("invalid cumulative_executed_qty: %s", event.Data.CumulativeExecutedQty)
	}

	lastCum, err := decimal.NewFromString(order.FilledQuantity)
	if err != nil {
		lastCum = decimal.Zero
	}

	// cum <= last_cum：忽略（重复/乱序/重入队）
	if newCum.LessThanOrEqual(lastCum) {
		log.Printf("fill_update ignored: cumulative_qty=%s <= last_cum=%s (order_id=%d, delivery_id=%s)",
			newCum.String(), lastCum.String(), order.ID, delivery.DeliveryID)

		p.createAuditLog(ctx, order.FlowID, order.ID, models.LogTypeSystem, models.LogSeverityInfo,
			"Fill update ignored (duplicate/out-of-order)", map[string]interface{}{
				"new_cumulative":  newCum.String(),
				"last_cumulative": lastCum.String(),
				"delivery_id":     delivery.DeliveryID,
				"event_id":        event.EventID,
			})
		return nil
	}

	// cum > last_cum：处理，delta = cum - last_cum
	actualDelta := newCum.Sub(lastCum)
	orderData := event.Data.Order

	// 1. Create FillEvent（使用计算出的 actualDelta）
	fillEvent := &models.FillEvent{
		FillUUID:       uuid.New().String(),
		OrderID:        order.ID,
		FlowID:         order.FlowID,
		FillPrice:      orderData.Price,
		FillQuantity:   actualDelta.String(),
		FillTime:       time.Now(),
		BinanceOrderID: strconv.FormatInt(orderData.OrderID, 10),
		ReceivedAt:     time.Now(),
		RawData:        delivery.RawPayload,
	}

	if err := p.fillRepo.Create(ctx, fillEvent); err != nil {
		return fmt.Errorf("failed to create fill event: %w", err)
	}

	// 2. Update order status（使用乐观锁）
	oldStatus := order.Status
	newStatus := p.mapBinanceStatus(event.Data.Status)
	oldVersion := order.Version

	updates := map[string]interface{}{
		"filled_quantity": newCum.String(),
		"status":          newStatus,
		"version":         oldVersion + 1,
	}

	// 如果是终态，设置 completed_at
	if p.isTerminalOrderStatus(newStatus) {
		updates["completed_at"] = sql.NullTime{Time: time.Now(), Valid: true}
		updates["track_job_status"] = "terminal"
	}

	rowsAffected, err := p.orderRepo.UpdateWithVersion(ctx, order.ID, oldVersion, updates)
	if err != nil {
		return fmt.Errorf("failed to update order: %w", err)
	}
	if rowsAffected == 0 {
		return ErrOptimisticLock
	}
	order.Version = oldVersion + 1
	order.FilledQuantity = newCum.String()
	order.Status = newStatus

	// 记录状态变更历史
	if oldStatus != newStatus {
		p.createOrderStatusHistory(ctx, order.ID, oldStatus, newStatus, "fill_update", delivery.RawPayload)
	}
	// 3. Create audit log
	p.createAuditLog(ctx, order.FlowID, order.ID, models.LogTypeFillReceived, models.LogSeverityInfo,
		"Fill received", map[string]interface{}{
			"reported_delta":   event.Data.DeltaExecutedQty,
			"actual_delta":     actualDelta.String(),
			"new_cumulative":   newCum.String(),
			"last_cumulative":  lastCum.String(),
			"status":           event.Data.Status,
			"binance_order_id": orderData.OrderID,
			"delivery_id":      delivery.DeliveryID, // 记录 delivery_id 便于追溯
		})

	// 4. Trigger derivative order creation if this is a primary order fill
	if order.OrderRole == models.OrderRolePrimary && p.cfg.Derivative.Enabled && p.derivativeEngine != nil && actualDelta.GreaterThan(decimal.Zero) {
		derivativeParams, err := p.derivativeEngine.GenerateDerivativeOrder(ctx, derivative.FillParams{
			PrimaryOrder:  order,
			FillPrice:     orderData.Price,
			Delta:         actualDelta.String(),
			CumulativeQty: newCum.String(),
		})
		if err != nil {
			// 引擎已通知管理员（表达式错误等），记录日志后终止
			log.Printf("Failed to create derivative order: %v", err)
			return err // 终止流程
		}

		// 检查是否匹配到规则
		if derivativeParams == nil {
			log.Printf("No matching derivative rule for primary order %d (market=%s, symbol=%s)",
				order.ID, order.MarketType, order.FullSymbol)
			p.notifier.NotifyNoMatchingRule(order.ID, string(order.MarketType), order.FullSymbol)
			// 继续流程，不阻断
		}

		if derivativeParams != nil {
			// 同步提交衍生订单，确保创建完成后再检查 flow 状态
			p.submitDerivativeOrderFromParams(ctx, order, fillEvent, derivativeParams)
		}
	}

	// 5. 如果是终态，检查 flow 是否完成
	if p.isTerminalOrderStatus(newStatus) {
		if err := p.flowService.CheckFlowCompletion(ctx, order.FlowID); err != nil {
			log.Printf("Failed to check flow completion: %v", err)
		}
	}

	return nil
}

// submitDerivativeOrderFromParams 根据引擎生成的参数提交衍生订单
func (p *webhookProcessor) submitDerivativeOrderFromParams(
	ctx context.Context,
	primaryOrder *models.Order,
	fillEvent *models.FillEvent,
	params *derivative.DerivativeOrderParams,
) {
	// 创建衍生订单记录
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

	if err := p.orderRepo.Create(ctx, derivativeOrder); err != nil {
		log.Printf("Failed to create derivative order record: %v", err)
		p.notifier.NotifyDerivativeFailure(primaryOrder.ID, params.Symbol, params.RuleName, "创建订单记录", err.Error())
		return
	}

	// 回填执行日志中的衍生订单 ID
	if params.LogID > 0 && p.ruleRepo != nil {
		if err := p.ruleRepo.UpdateLogDerivativeOrderID(ctx, params.LogID, derivativeOrder.ID); err != nil {
			log.Printf("Failed to update derivative rule log: %v", err)
		}
	}

	p.createAuditLog(ctx, primaryOrder.FlowID, derivativeOrder.ID, models.LogTypeDerivativeTrigger, models.LogSeverityInfo,
		"Derivative order created via DSL engine", map[string]interface{}{
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

	// 标记 fill 事件已触发衍生订单
	fillEvent.HasTriggeredDerivative = true
	fillEvent.DerivativeOrderID = sql.NullInt64{Int64: int64(derivativeOrder.ID), Valid: true}
	p.fillRepo.Update(ctx, fillEvent)

	// 提交到 Binance
	p.submitDerivativeOrder(ctx, derivativeOrder)
}

func (p *webhookProcessor) handleTerminal(ctx context.Context, order *models.Order, event *binance.WebhookEvent, delivery *models.WebhookDelivery) error {
	log.Printf("Processing terminal for order %d (binance_id=%d), status=%s",
		order.ID, event.OrderID, event.Data.Status)

	orderData := event.Data.Order

	// 获取本地当前的 filled_quantity
	lastCum, err := decimal.NewFromString(order.FilledQuantity)
	if err != nil {
		lastCum = decimal.Zero
	}

	// 从 event.Data.Order.ExecutedQty 获取累计成交量
	// terminal 事件的 Order 数据是完整的终态订单数据
	newCum, err := decimal.NewFromString(orderData.ExecutedQty)
	if err != nil {
		log.Printf("Warning: failed to parse executed_qty from terminal order data: %s, using local value", orderData.ExecutedQty)
		newCum = lastCum
	}

	// 如果订单已经是终态，忽略重复的 terminal 事件
	if p.isTerminalOrderStatus(order.Status) {
		log.Printf("terminal ignored: order already in terminal state %s (order_id=%d, delivery_id=%s)",
			order.Status, order.ID, delivery.DeliveryID)

		p.createAuditLog(ctx, order.FlowID, order.ID, models.LogTypeSystem, models.LogSeverityInfo,
			"Terminal event ignored (order already terminal)", map[string]interface{}{
				"current_status": string(order.Status),
				"event_status":   event.Data.Status,
				"delivery_id":    delivery.DeliveryID,
			})
		return nil
	}

	// ============================================
	// 检查是否有未处理的成交增量（处理乱序情况）
	// terminal 可能先于 fill_update 到达
	// ============================================
	var fillEvent *models.FillEvent
	if newCum.GreaterThan(lastCum) {
		actualDelta := newCum.Sub(lastCum)
		log.Printf("Terminal event contains unprocessed fill delta: %s (order_id=%d)",
			actualDelta.String(), order.ID)

		// 创建 FillEvent 记录这笔增量
		fillEvent = &models.FillEvent{
			FillUUID:       uuid.New().String(),
			OrderID:        order.ID,
			FlowID:         order.FlowID,
			FillPrice:      orderData.Price,
			FillQuantity:   actualDelta.String(),
			FillTime:       time.Now(),
			BinanceOrderID: strconv.FormatInt(orderData.OrderID, 10),
			ReceivedAt:     time.Now(),
			RawData:        delivery.RawPayload,
		}

		if err := p.fillRepo.Create(ctx, fillEvent); err != nil {
			log.Printf("Failed to create fill event in terminal: %v", err)
			fillEvent = nil // 创建失败，不触发衍生订单
		} else {
			p.createAuditLog(ctx, order.FlowID, order.ID, models.LogTypeFillReceived, models.LogSeverityInfo,
				"Fill received (from terminal event)", map[string]interface{}{
					"actual_delta":    actualDelta.String(),
					"new_cumulative":  newCum.String(),
					"last_cumulative": lastCum.String(),
					"delivery_id":     delivery.DeliveryID,
				})
		}
	}

	// 1. Update order status（使用乐观锁）
	oldStatus := order.Status
	newStatus := p.mapBinanceStatus(event.Data.Status)
	oldVersion := order.Version

	rowsAffected, err := p.orderRepo.UpdateWithVersion(ctx, order.ID, oldVersion, map[string]interface{}{
		"filled_quantity":  newCum.String(),
		"status":           newStatus,
		"track_job_status": "terminal",
		"completed_at":     sql.NullTime{Time: time.Now(), Valid: true},
		"version":          oldVersion + 1,
	})
	if err != nil {
		return fmt.Errorf("failed to update order: %w", err)
	}
	if rowsAffected == 0 {
		return ErrOptimisticLock
	}
	order.Version = oldVersion + 1
	order.FilledQuantity = newCum.String()
	order.Status = newStatus

	// 2. 记录状态变更历史
	if oldStatus != newStatus {
		p.createOrderStatusHistory(ctx, order.ID, oldStatus, newStatus, "terminal", delivery.RawPayload)
	}

	// 3. Create audit log
	p.createAuditLog(ctx, order.FlowID, order.ID, models.LogTypeSystem, models.LogSeverityInfo,
		"Order reached terminal state", map[string]interface{}{
			"status":           event.Data.Status,
			"final_cumulative": newCum.String(),
			"last_cumulative":  lastCum.String(),
			"binance_order_id": orderData.OrderID,
			"delivery_id":      delivery.DeliveryID,
		})

	// 4. 如果有未处理的增量且是 primary order，触发衍生订单
	if fillEvent != nil && order.OrderRole == models.OrderRolePrimary && p.cfg.Derivative.Enabled && p.derivativeEngine != nil {
		derivativeParams, err := p.derivativeEngine.GenerateDerivativeOrder(ctx, derivative.FillParams{
			PrimaryOrder:  order,
			FillPrice:     orderData.Price,
			Delta:         fillEvent.FillQuantity,
			CumulativeQty: newCum.String(),
		})
		if err != nil {
			log.Printf("Failed to generate derivative order from terminal: %v", err)
			p.createAuditLog(ctx, order.FlowID, order.ID, models.LogTypeError, models.LogSeverityError,
				"Failed to generate derivative order from terminal", map[string]interface{}{
					"error":       err.Error(),
					"delivery_id": delivery.DeliveryID,
				})
		} else if derivativeParams != nil {
			p.submitDerivativeOrderFromParams(ctx, order, fillEvent, derivativeParams)
		}
	}

	// 5. Check if flow is complete
	if err := p.flowService.CheckFlowCompletion(ctx, order.FlowID); err != nil {
		log.Printf("Failed to check flow completion: %v", err)
	}

	return nil
}

// createOrderStatusHistory 创建订单状态变更历史
func (p *webhookProcessor) createOrderStatusHistory(ctx context.Context, orderID uint, fromStatus, toStatus models.OrderStatus, reason string, binanceResponse string) {
	history := &models.OrderStatusHistory{
		OrderID:         orderID,
		FromStatus:      string(fromStatus),
		ToStatus:        string(toStatus),
		ChangedAt:       time.Now(),
		Reason:          reason,
		BinanceResponse: binanceResponse,
	}

	if err := p.orderRepo.CreateStatusHistory(ctx, history); err != nil {
		log.Printf("Failed to create order status history: %v", err)
	}
}

// isTerminalOrderStatus 检查订单状态是否为终态
func (p *webhookProcessor) isTerminalOrderStatus(status models.OrderStatus) bool {
	return status == models.OrderStatusFilled ||
		status == models.OrderStatusCancelled ||
		status == models.OrderStatusRejected ||
		status == models.OrderStatusExpired
}

func (p *webhookProcessor) submitDerivativeOrder(ctx context.Context, order *models.Order) {
	var binanceOrder *binance.OrderInfo
	var err error

	if order.MarketType == models.MarketTypeSpot {
		req := binance.SpotOrderRequest{
			Symbol:        order.FullSymbol,
			Side:          p.directionToSide(order.Direction),
			Type:          "LIMIT",
			Quantity:      order.Quantity,
			Price:         order.Price.String,
			ClientOrderID: order.OrderUUID,
		}
		binanceOrder, err = p.binanceClient.SpotCreateOrder(ctx, req)
	} else {
		req := binance.CoinMOrderRequest{
			Symbol:        order.FullSymbol,
			Side:          p.directionToSide(order.Direction),
			Type:          "LIMIT",
			Quantity:      order.Quantity,
			Price:         order.Price.String,
			PositionSide:  p.directionToPositionSide(order.Direction),
			ClientOrderID: order.OrderUUID,
		}
		binanceOrder, err = p.binanceClient.CoinMCreateOrder(ctx, req)
	}

	// 可选：验证返回的 client_order_id 与我们发送的一致
	if binanceOrder.ClientOrderID != order.OrderUUID {
		log.Printf("Warning: client_order_id mismatch, sent=%s, received=%s",
			order.OrderUUID, binanceOrder.ClientOrderID)
		order.BinanceClientOrderID = binanceOrder.ClientOrderID
	}

	if err != nil {
		log.Printf("Failed to submit derivative order %d: %v", order.ID, err)
		p.orderRepo.UpdateStatus(ctx, order.ID, models.OrderStatusRejected, err.Error())
		p.notifier.NotifyDerivativeFailure(uint(order.ParentOrderID.Int64), order.FullSymbol, "", "提交到Binance", err.Error())
		p.createAuditLog(ctx, order.FlowID, order.ID, models.LogTypeError, models.LogSeverityError,
			"Failed to submit derivative order", map[string]interface{}{
				"error": err.Error(),
			})
		return
	}

	// Update order with Binance response
	order.BinanceOrderID = strconv.FormatInt(binanceOrder.OrderID, 10)
	order.BinanceClientOrderID = binanceOrder.ClientOrderID
	order.Status = p.mapBinanceStatus(binanceOrder.Status)
	order.FilledQuantity = binanceOrder.ExecutedQty
	order.SubmittedAt = sql.NullTime{Time: time.Now(), Valid: true}

	rawResp, _ := json.Marshal(binanceOrder)
	order.BinanceRawResponse = string(rawResp)

	if err := p.orderRepo.Update(ctx, order); err != nil {
		log.Printf("Failed to update derivative order: %v", err)
	}

	p.createAuditLog(ctx, order.FlowID, order.ID, models.LogTypeOrderSubmit, models.LogSeverityInfo,
		"Derivative order submitted", map[string]interface{}{
			"binance_order_id": binanceOrder.OrderID,
			"status":           binanceOrder.Status,
			"executed_qty":     binanceOrder.ExecutedQty,
		})

	// Create track job if order is not immediately terminal
	if !p.isTerminalStatus(binanceOrder.Status) {
		p.createTrackJobForOrder(ctx, order)
	} else if binanceOrder.Status == "FILLED" {
		order.CompletedAt = sql.NullTime{Time: time.Now(), Valid: true}
		p.orderRepo.Update(ctx, order)
		p.flowService.CheckFlowCompletion(ctx, order.FlowID)
	}
}

func (p *webhookProcessor) createTrackJobForOrder(ctx context.Context, order *models.Order) {
	binanceOrderID, _ := strconv.ParseInt(order.BinanceOrderID, 10, 64)

	req := binance.TrackWebhookRequest{
		Symbol:     order.FullSymbol,
		OrderID:    binanceOrderID,
		WebhookURL: p.cfg.GetWebhookURL(),
	}

	var resp *binance.TrackWebhookResponse
	var err error

	if order.MarketType == models.MarketTypeSpot {
		resp, err = p.binanceClient.SpotTrackWebhook(ctx, req)
	} else {
		resp, err = p.binanceClient.CoinMTrackWebhook(ctx, req)
	}

	if err != nil {
		log.Printf("Failed to create track job for derivative order %d: %v", order.ID, err)
		return
	}

	order.TrackJobID = resp.JobID
	order.TrackJobStatus = resp.Status
	order.WebhookSecret = resp.WebhookSecret
	p.orderRepo.Update(ctx, order)

	p.createAuditLog(ctx, order.FlowID, order.ID, models.LogTypeSystem, models.LogSeverityInfo,
		"Track job created for derivative order", map[string]interface{}{
			"job_id": resp.JobID,
			"status": resp.Status,
		})
}

// ============================================
// Helper methods
// ============================================

func (p *webhookProcessor) mapBinanceStatus(status string) models.OrderStatus {
	switch status {
	case "NEW":
		return models.OrderStatusSubmitted
	case "PARTIALLY_FILLED":
		return models.OrderStatusPartiallyFilled
	case "FILLED":
		return models.OrderStatusFilled
	case "CANCELED":
		return models.OrderStatusCancelled
	case "REJECTED":
		return models.OrderStatusRejected
	case "EXPIRED":
		return models.OrderStatusExpired
	default:
		return models.OrderStatusPending
	}
}

func (p *webhookProcessor) isTerminalStatus(status string) bool {
	return status == "FILLED" || status == "CANCELED" || status == "REJECTED" || status == "EXPIRED"
}

func (p *webhookProcessor) directionToSide(d models.Direction) string {
	switch d {
	case models.DirectionLong, models.DirectionCloseShort:
		return "BUY"
	case models.DirectionShort, models.DirectionCloseLong:
		return "SELL"
	default:
		return "BUY"
	}
}

func (p *webhookProcessor) directionToPositionSide(d models.Direction) string {
	switch d {
	case models.DirectionLong, models.DirectionCloseLong:
		return "LONG"
	case models.DirectionShort, models.DirectionCloseShort:
		return "SHORT"
	default:
		return "BOTH"
	}
}

func (p *webhookProcessor) createAuditLog(ctx context.Context, flowID uint, orderID uint, logType models.LogType, severity models.LogSeverity, message string, detail map[string]interface{}) {
	var detailJSON string
	if detail != nil {
		b, _ := json.Marshal(detail)
		detailJSON = string(b)
	}

	auditLog := &models.AuditLog{
		LogType:  logType,
		Severity: severity,
		Message:  message,
		Detail:   detailJSON,
	}

	if flowID > 0 {
		auditLog.FlowID = sql.NullInt64{Int64: int64(flowID), Valid: true}
	}
	if orderID > 0 {
		auditLog.OrderID = sql.NullInt64{Int64: int64(orderID), Valid: true}
	}

	if err := p.auditRepo.Create(ctx, auditLog); err != nil {
		log.Printf("Failed to create audit log: %v", err)
	}
}
