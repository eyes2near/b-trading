// internal/service/fill_processor.go
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/eyes2near/b-trading/internal/binance"
	"github.com/eyes2near/b-trading/internal/database"
	"github.com/eyes2near/b-trading/internal/models"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// FillData 成交数据
type FillData struct {
	AvgPrice      string          // 平均成交价
	Price         string          // 当次Fill成交价格
	Quantity      decimal.Decimal // 当次成交数量
	CumulativeQty decimal.Decimal // 累计成交量
	FillTime      time.Time       // 成交时间
	BinanceStatus string          // Binance 订单状态
	Source        string          // 来源: "webhook", "track_snapshot", "terminal"
	RawData       string          // 原始数据 JSON
}

// FillProcessor 成交处理器接口
type FillProcessor interface {
	// ProcessFill 处理成交事件，返回是否有新增成交
	ProcessFill(ctx context.Context, order *models.Order, fill *FillData) (*models.FillEvent, error)

	// ProcessSnapshot 处理 Track Job 返回的快照
	ProcessSnapshot(ctx context.Context, order *models.Order, resp *binance.TrackWebhookResponse) (*models.FillEvent, error)
}

type fillProcessor struct {
	orderRepo database.OrderRepository
	fillRepo  database.FillEventRepository
	audit     AuditService
}

// NewFillProcessor 创建成交处理器
func NewFillProcessor(
	orderRepo database.OrderRepository,
	fillRepo database.FillEventRepository,
	audit AuditService,
) FillProcessor {
	return &fillProcessor{
		orderRepo: orderRepo,
		fillRepo:  fillRepo,
		audit:     audit,
	}
}

func (p *fillProcessor) ProcessFill(ctx context.Context, order *models.Order, fill *FillData) (*models.FillEvent, error) {
	// 1. 幂等检查：基于累计成交量
	lastCum, err := decimal.NewFromString(order.FilledQuantity)
	if err != nil {
		lastCum = decimal.Zero
	}

	// 累计量未增加，忽略（重复/乱序）
	if fill.CumulativeQty.LessThanOrEqual(lastCum) {
		log.Printf("Fill ignored: cumulative_qty=%s <= last_cum=%s (order_id=%d, source=%s)",
			fill.CumulativeQty.String(), lastCum.String(), order.ID, fill.Source)

		p.audit.LogEvent(ctx, order.FlowID, order.ID, models.LogTypeSystem, models.LogSeverityInfo,
			"Fill ignored (duplicate/out-of-order)", map[string]interface{}{
				"actual_delta":    fill.Quantity.String(),
				"new_cumulative":  fill.CumulativeQty.String(),
				"last_cumulative": lastCum.String(),
				"source":          fill.Source,
				"rawData":         fill.RawData,
			})
		return nil, nil
	}

	// 2. 创建 FillEvent
	fillEvent := &models.FillEvent{
		FillUUID:       uuid.New().String(),
		OrderID:        order.ID,
		FlowID:         order.FlowID,
		FillPrice:      fill.Price,
		FillQuantity:   fill.Quantity.String(),
		FillTime:       fill.FillTime,
		BinanceOrderID: order.BinanceOrderID,
		ReceivedAt:     time.Now(),
		RawData:        fill.RawData,
	}

	if err := p.fillRepo.Create(ctx, fillEvent); err != nil {
		return nil, fmt.Errorf("failed to create fill event: %w", err)
	}

	// 4. 更新订单（使用乐观锁）
	oldStatus := order.Status
	newStatus := models.MapBinanceStatus(fill.BinanceStatus)

	updates := map[string]interface{}{
		"filled_quantity": fill.CumulativeQty.String(),
		"status":          newStatus,
		"avg_fill_price":  fill.AvgPrice,
		"version":         order.Version + 1,
	}

	if models.IsTerminalOrderStatus(string(newStatus)) {
		updates["completed_at"] = sql.NullTime{Time: time.Now(), Valid: true}
		updates["track_job_status"] = "terminal"
	}

	rowsAffected, err := p.orderRepo.UpdateWithVersion(ctx, order.ID, order.Version, updates)
	if err != nil {
		return nil, fmt.Errorf("failed to update order: %w", err)
	}
	if rowsAffected == 0 {
		return nil, ErrOptimisticLock
	}

	// 6. 同步内存状态
	order.Version++
	order.FilledQuantity = fill.CumulativeQty.String()
	order.Status = newStatus
	order.AvgFillPrice = fill.AvgPrice

	// 7. 记录状态变更
	if oldStatus != newStatus {
		p.audit.RecordOrderStatusChange(ctx, order.ID, oldStatus, newStatus, fill.Source, fill.RawData)
	}

	// 8. 审计日志
	p.audit.LogEvent(ctx, order.FlowID, order.ID, models.LogTypeFillReceived, models.LogSeverityInfo,
		"Fill received", map[string]interface{}{
			"actual_delta":    fill.Quantity.String(),
			"new_cumulative":  fill.CumulativeQty.String(),
			"last_cumulative": lastCum.String(),
			"status":          fill.BinanceStatus,
			"avg_fill_price":  fill.AvgPrice,
			"source":          fill.Source,
			"raw_data":        fill.RawData,
		})

	return fillEvent, nil
}

func (p *fillProcessor) ProcessSnapshot(ctx context.Context, order *models.Order, resp *binance.TrackWebhookResponse) (*models.FillEvent, error) {
	if resp == nil {
		return nil, nil
	}

	latest := resp.Order
	if latest.ExecutedQty == "" {
		return nil, nil
	}

	newCum, err := decimal.NewFromString(latest.ExecutedQty)
	if err != nil {
		return nil, fmt.Errorf("invalid snapshot executed_qty: %s", latest.ExecutedQty)
	}

	// 估算快照均价
	fillPrice, qty := CalculateFillPriceAndQty(order, resp.Order.ExecutedQty, resp.Order.AvgPrice)

	// 解析成交时间
	fillTime := latest.UpdateTime

	// 构建原始数据
	rawData, _ := json.Marshal(map[string]interface{}{
		"source":   "track_snapshot",
		"response": resp,
	})

	// 使用稳定 UUID 避免重试重复插入
	stableUUID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("track_snapshot:%d:%s", order.ID, newCum.String()))).String()

	fill := &FillData{
		Price:         fillPrice,
		AvgPrice:      latest.AvgPrice,
		Quantity:      qty,
		CumulativeQty: newCum,
		FillTime:      fillTime,
		BinanceStatus: latest.Status,
		Source:        "track_snapshot",
		RawData:       string(rawData),
	}

	// 临时覆盖 FillUUID
	fillEvent, err := p.ProcessFill(ctx, order, fill)
	if err != nil {
		return nil, err
	}

	// 如果成功创建了 fillEvent，更新为稳定 UUID（用于幂等）
	if fillEvent != nil {
		fillEvent.FillUUID = stableUUID
		_ = p.fillRepo.Update(ctx, fillEvent)
	}

	return fillEvent, nil
}

func CalculateFillPriceAndQty(
	order *models.Order,
	executedQty, avgPrice string,
) (string, decimal.Decimal) {
	filledQty := order.FilledQuantity
	if strings.TrimSpace(filledQty) == "" {
		filledQty = "0"
	}
	if strings.TrimSpace(executedQty) == "" || strings.TrimSpace(avgPrice) == "" {
		return "0", decimal.Zero
	}

	// 解析新旧成交量
	oldFilledQty, err := decimal.NewFromString(filledQty)
	if err != nil {
		return "0", decimal.Zero
	}

	newFilledQty, err := decimal.NewFromString(executedQty)
	if err != nil {
		return "0", decimal.Zero
	}

	// 没有新增成交
	if newFilledQty.LessThanOrEqual(oldFilledQty) {
		return "0", decimal.Zero
	}

	deltaQty := newFilledQty.Sub(oldFilledQty)

	newAvgPrice, err := decimal.NewFromString(avgPrice)
	if err != nil {
		//兜底
		_, err := decimal.NewFromString(order.AvgFillPrice)
		if err != nil {
			if order.Price.Valid {
				return order.Price.String, deltaQty
			}
			//这不合理
			return "-1", deltaQty
		}
		return order.AvgFillPrice, deltaQty
	}

	// 旧订单累计均价（本地）
	oldAvgPrice := decimal.Zero
	if order.AvgFillPrice != "" {
		oldAvgPrice, _ = decimal.NewFromString(order.AvgFillPrice)
	}

	// 累计成交额
	oldTotalAmount := oldFilledQty.Mul(oldAvgPrice)
	newTotalAmount := newFilledQty.Mul(newAvgPrice)

	// 本次新增成交额
	deltaAmount := newTotalAmount.Sub(oldTotalAmount)

	// 本次成交均价
	fillPrice := deltaAmount.Div(deltaQty)

	// 保留 8 位（与 Binance 精度一致）
	return fillPrice.StringFixed(8), deltaQty
}
