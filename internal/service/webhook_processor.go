// internal/service/webhook_processor.go (重构后)
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
		//事件乱序，正常现象，直接幂等不处理
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

	// 检查流程完成
	if models.IsTerminalOrderStatus(string(order.Status)) {
		_ = p.flowService.CheckFlowCompletion(ctx, order.FlowID)
	}

	return nil
}

func (p *webhookProcessor) handleTerminal(ctx context.Context, order *models.Order, event *binance.WebhookEvent, delivery *models.WebhookDelivery) error {
	// 若已终态，忽略
	if models.IsTerminalOrderStatus(string(order.Status)) {
		return nil
	}

	orderData := event.Data.Order
	newCum, err := decimal.NewFromString(orderData.ExecutedQty)
	if err != nil {
		lastCum, _ := decimal.NewFromString(order.FilledQuantity)
		newCum = lastCum
	}

	fillPrice, qty := CalculateFillPriceAndQty(order, orderData.ExecutedQty, orderData.AvgPrice)
	if qty.GreaterThan(decimal.Zero) {
		// 处理可能遗漏的成交
		fill := &FillData{
			Price:         fillPrice,
			Quantity:      qty,
			AvgPrice:      orderData.AvgPrice,
			CumulativeQty: newCum,
			FillTime:      time.Now(),
			BinanceStatus: event.Data.Status,
			Source:        "terminal",
			RawData:       delivery.RawPayload,
		}
		fillEvent, err := p.fillProcessor.ProcessFill(ctx, order, fill)
		if err != nil && err != ErrOptimisticLock {
			// ProcessFill 可能返回 nil fillEvent（无新增成交），但仍需更新终态
			log.Printf("Fill processing in terminal: %v", err)
		}

		// 若有新增成交，触发衍生订单
		if fillEvent != nil && order.OrderRole == models.OrderRolePrimary && p.cfg.Derivative.Enabled && p.derivativeEngine != nil {
			p.triggerDerivative(ctx, order, fillEvent)
		}
	}

	// 检查流程完成
	_ = p.flowService.CheckFlowCompletion(ctx, order.FlowID)

	return nil
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
