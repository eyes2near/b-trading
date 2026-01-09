// internal/service/webhook_processor.go
package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	"github.com/shopspring/decimal"
)

type WebhookProcessor interface {
	ValidateSignature(timestamp, signature string, body []byte, secret string) bool
	CheckDuplicate(ctx context.Context, deliveryID string) (bool, error)
	ProcessEvent(ctx context.Context, deliveryID, jobID string, event *binance.WebhookEvent, rawBody []byte) error
}

const maxOptimisticRetries = 3

type webhookProcessor struct {
	cfg              *config.Config
	orderRepo        database.OrderRepository
	flowRepo         database.TradingFlowRepository
	deliveryRepo     database.WebhookDeliveryRepository
	lockManager      *OrderLockManager
	orderService     OrderService
	fillProcessor    FillProcessor
	flowService      FlowService
	audit            AuditService
	derivativeEngine derivative.Engine
	notifier         *notify.Notifier
}

func NewWebhookProcessor(
	cfg *config.Config,
	orderRepo database.OrderRepository,
	flowRepo database.TradingFlowRepository,
	deliveryRepo database.WebhookDeliveryRepository,
	orderService OrderService,
	fillProcessor FillProcessor,
	flowService FlowService,
	audit AuditService,
	derivativeEngine derivative.Engine,
	notifier *notify.Notifier,
) WebhookProcessor {
	return &webhookProcessor{
		cfg:              cfg,
		orderRepo:        orderRepo,
		flowRepo:         flowRepo,
		deliveryRepo:     deliveryRepo,
		lockManager:      NewOrderLockManager(),
		orderService:     orderService,
		fillProcessor:    fillProcessor,
		flowService:      flowService,
		audit:            audit,
		derivativeEngine: derivativeEngine,
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
	// 1. 记录投递
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

	// 2. 查找订单
	order, err := p.orderRepo.GetByBinanceOrderID(ctx, strconv.FormatInt(event.OrderID, 10))
	if err != nil {
		delivery.ProcessErr = "order not found: " + err.Error()
		p.deliveryRepo.Update(ctx, delivery)
		return fmt.Errorf("order not found for binance_order_id=%d: %w", event.OrderID, err)
	}

	// 3. 内存锁 + 乐观锁处理
	var processErr error

	p.lockManager.WithLock(order.ID, func() error {
		for retry := 0; retry < maxOptimisticRetries; retry++ {
			order, err = p.orderRepo.GetByBinanceOrderID(ctx, strconv.FormatInt(event.OrderID, 10))
			if err != nil {
				processErr = err
				return err
			}

			switch event.EventType {
			case "fill_update":
				processErr = p.handleFillUpdate(ctx, order, event, delivery)
			case "terminal":
				processErr = p.handleTerminal(ctx, order, event, delivery)
			default:
				processErr = fmt.Errorf("unknown event type: %s", event.EventType)
			}

			if processErr != ErrOptimisticLock {
				return processErr
			}

			log.Printf("Optimistic lock conflict, retrying (%d/%d) for order %d",
				retry+1, maxOptimisticRetries, order.ID)
		}

		processErr = fmt.Errorf("max optimistic lock retries exceeded for order %d", order.ID)
		return processErr
	})

	// 4. 更新投递状态
	delivery.ProcessedAt = sql.NullTime{Time: time.Now(), Valid: true}
	if processErr != nil {
		delivery.ProcessErr = processErr.Error()
	}
	p.deliveryRepo.Update(ctx, delivery)

	return processErr
}

func (p *webhookProcessor) handleFillUpdate(ctx context.Context, order *models.Order, event *binance.WebhookEvent, delivery *models.WebhookDelivery) error {
	log.Printf("Processing fill_update for order %d, delta=%s, cumulative=%s",
		order.ID, event.Data.DeltaExecutedQty, event.Data.Order.ExecutedQty)

	newCum, err := decimal.NewFromString(event.Data.Order.ExecutedQty)
	if err != nil {
		return fmt.Errorf("invalid cumulative_executed_qty: %s", event.Data.Order.ExecutedQty)
	}

	orderData := event.Data.Order
	fillPrice, qty := CalculateFillPriceAndQty(order, orderData.ExecutedQty, orderData.AvgPrice)

	if qty.IsZero() {
		// 事件乱序，正常现象，直接幂等不处理
		return nil
	}

	// 使用 FillProcessor 处理成交
	fill := &FillData{
		Price:         fillPrice,
		AvgPrice:      orderData.AvgPrice,
		Quantity:      qty,
		CumulativeQty: newCum,
		FillTime:      time.Now(),
		BinanceStatus: event.Data.Status,
		Source:        "fill_update",
		RawData:       delivery.RawPayload,
	}

	fillEvent, err := p.fillProcessor.ProcessFill(ctx, order, fill)
	if err != nil {
		return err
	}

	// 若无新增成交（幂等忽略），直接返回
	if fillEvent == nil {
		return nil
	}

	// 触发衍生订单
	if order.OrderRole == models.OrderRolePrimary && p.cfg.Derivative.Enabled && p.derivativeEngine != nil {
		p.triggerDerivative(ctx, order, fillEvent)
	}

	// 如果 Binance 状态是终态，更新订单状态并检查流程完成
	if models.IsTerminalOrderStatus(event.Data.Status) {
		if err := p.updateOrderToTerminal(ctx, order, event.Data.Status, orderData.ExecutedQty); err != nil {
			log.Printf("Failed to update order to terminal in fill_update: %v", err)
		}
		_ = p.flowService.CheckFlowCompletion(ctx, order.FlowID)
	} else {
		// 非终态时也要更新订单状态（如 PARTIALLY_FILLED）
		p.updateOrderStatus(ctx, order, event.Data.Status)
	}

	return nil
}

func (p *webhookProcessor) handleTerminal(ctx context.Context, order *models.Order, event *binance.WebhookEvent, delivery *models.WebhookDelivery) error {
	// 若已终态，忽略
	if models.IsTerminalOrderStatus(string(order.Status)) {
		log.Printf("Order %d already in terminal state %s, skipping", order.ID, order.Status)
		return nil
	}

	orderData := event.Data.Order
	binanceStatus := event.Data.Status

	log.Printf("Processing terminal event for order %d, binance_status=%s, executed_qty=%s",
		order.ID, binanceStatus, orderData.ExecutedQty)

	// 解析成交量
	newCum, err := decimal.NewFromString(orderData.ExecutedQty)
	if err != nil {
		newCum = decimal.Zero
	}

	// 计算是否有新增成交
	fillPrice, qty := CalculateFillPriceAndQty(order, orderData.ExecutedQty, orderData.AvgPrice)

	// 处理可能遗漏的成交
	var fillEvent *models.FillEvent
	if qty.GreaterThan(decimal.Zero) {
		fill := &FillData{
			Price:         fillPrice,
			Quantity:      qty,
			AvgPrice:      orderData.AvgPrice,
			CumulativeQty: newCum,
			FillTime:      time.Now(),
			BinanceStatus: binanceStatus,
			Source:        "terminal",
			RawData:       delivery.RawPayload,
		}

		fillEvent, err = p.fillProcessor.ProcessFill(ctx, order, fill)
		if err != nil && err != ErrOptimisticLock {
			log.Printf("Fill processing in terminal event: %v", err)
		}

		// 若有新增成交，触发衍生订单
		if fillEvent != nil && order.OrderRole == models.OrderRolePrimary && p.cfg.Derivative.Enabled && p.derivativeEngine != nil {
			p.triggerDerivative(ctx, order, fillEvent)
		}
	}

	// 更新订单状态为终态
	if err := p.updateOrderToTerminal(ctx, order, binanceStatus, orderData.ExecutedQty); err != nil {
		log.Printf("Failed to update order %d to terminal state: %v", order.ID, err)
		return err
	}

	// 审计日志
	p.audit.LogEvent(ctx, order.FlowID, order.ID, models.LogTypeOrderComplete, models.LogSeverityInfo,
		"Order reached terminal state", map[string]interface{}{
			"binance_status": binanceStatus,
			"executed_qty":   orderData.ExecutedQty,
			"had_new_fill":   fillEvent != nil,
			"source":         "terminal_webhook",
		})

	// 检查流程完成
	if err := p.flowService.CheckFlowCompletion(ctx, order.FlowID); err != nil {
		log.Printf("Failed to check flow completion: %v", err)
	}

	return nil
}

// updateOrderToTerminal 将订单更新为终态（带乐观锁重试）
func (p *webhookProcessor) updateOrderToTerminal(ctx context.Context, order *models.Order, binanceStatus string, executedQty string) error {
	newStatus := models.MapBinanceStatus(binanceStatus)
	reason := fmt.Sprintf("Terminal webhook received: %s", binanceStatus)

	for retry := 0; retry < maxOptimisticRetries; retry++ {
		// 重新获取最新订单状态
		currentOrder, err := p.orderRepo.GetByID(ctx, order.ID)
		if err != nil {
			return fmt.Errorf("failed to get order: %w", err)
		}

		// 如果已经是终态，无需更新
		if models.IsTerminalOrderStatus(string(currentOrder.Status)) {
			log.Printf("Order %d already terminal (status=%s), skip update", order.ID, currentOrder.Status)
			return nil
		}

		// 构建更新字段
		updates := map[string]interface{}{
			"status":           newStatus,
			"track_job_status": "terminal",
			"completed_at":     time.Now(),
			"version":          currentOrder.Version + 1,
		}

		// 更新成交量（如果有变化）
		if executedQty != "" && executedQty != currentOrder.FilledQuantity {
			updates["filled_quantity"] = executedQty
		}

		// 使用乐观锁更新
		rows, err := p.orderRepo.UpdateWithVersion(ctx, currentOrder.ID, currentOrder.Version, updates)
		if err != nil {
			return fmt.Errorf("failed to update order: %w", err)
		}

		if rows > 0 {
			// 更新成功，记录状态历史
			history := &models.OrderStatusHistory{
				OrderID:    order.ID,
				FromStatus: string(currentOrder.Status),
				ToStatus:   string(newStatus),
				ChangedAt:  time.Now(),
				Reason:     reason,
			}
			if err := p.orderRepo.CreateStatusHistory(ctx, history); err != nil {
				log.Printf("Failed to create status history: %v", err)
			}

			log.Printf("Order %d updated to terminal status %s", order.ID, newStatus)
			return nil
		}

		// 乐观锁冲突，重试
		log.Printf("Optimistic lock conflict updating order %d to terminal, retry %d/%d",
			order.ID, retry+1, maxOptimisticRetries)
	}

	return fmt.Errorf("max retries exceeded updating order %d to terminal state", order.ID)
}

// updateOrderStatus 更新订单状态（非终态情况）
func (p *webhookProcessor) updateOrderStatus(ctx context.Context, order *models.Order, binanceStatus string) {
	newStatus := models.MapBinanceStatus(binanceStatus)

	// 如果状态没有变化，跳过
	if order.Status == newStatus {
		return
	}

	for retry := 0; retry < maxOptimisticRetries; retry++ {
		currentOrder, err := p.orderRepo.GetByID(ctx, order.ID)
		if err != nil {
			log.Printf("Failed to get order for status update: %v", err)
			return
		}

		// 如果已经是终态，不应该回退
		if models.IsTerminalOrderStatus(string(currentOrder.Status)) {
			return
		}

		updates := map[string]interface{}{
			"status":  newStatus,
			"version": currentOrder.Version + 1,
		}

		rows, err := p.orderRepo.UpdateWithVersion(ctx, currentOrder.ID, currentOrder.Version, updates)
		if err != nil {
			log.Printf("Failed to update order status: %v", err)
			return
		}

		if rows > 0 {
			log.Printf("Order %d status updated to %s", order.ID, newStatus)
			return
		}
	}
}

func (p *webhookProcessor) triggerDerivative(ctx context.Context, order *models.Order, fillEvent *models.FillEvent) {
	delta, _ := decimal.NewFromString(fillEvent.FillQuantity)
	if delta.LessThanOrEqual(decimal.Zero) {
		return
	}

	// 获取 Flow 信息
	flow, err := p.flowRepo.GetByID(ctx, order.FlowID)
	if err != nil {
		log.Printf("Failed to get flow for derivative trigger: %v", err)
		return
	}

	// 使用 Flow 级别规则（优先）或全局规则
	derivativeParams, err := p.derivativeEngine.GenerateDerivativeOrderForFlow(ctx, derivative.FillParams{
		PrimaryOrder:  order,
		FillPrice:     fillEvent.FillPrice,
		Delta:         fillEvent.FillQuantity,
		CumulativeQty: order.FilledQuantity,
	}, flow)

	if err != nil {
		log.Printf("Failed to generate derivative order: %v", err)
		p.notifier.NotifyDerivativeFailure(order.ID, order.FullSymbol, "", "生成衍生订单", err.Error())
		return
	}

	if derivativeParams == nil {
		p.notifier.NotifyNoMatchingRule(order.ID, string(order.MarketType), order.FullSymbol)
		return
	}

	if err := p.orderService.SubmitDerivativeOrder(ctx, order, fillEvent, derivativeParams); err != nil {
		log.Printf("Failed to submit derivative order: %v", err)
	}
}
