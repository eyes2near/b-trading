// internal/service/flow_service.go

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/eyes2near/b-trading/internal/binance"
	"github.com/eyes2near/b-trading/internal/config"
	"github.com/eyes2near/b-trading/internal/database"
	"github.com/eyes2near/b-trading/internal/models"
	"github.com/eyes2near/b-trading/internal/notify"
	"github.com/google/uuid"
)

type FlowService interface {
	CreateFlow(ctx context.Context, req CreateFlowRequest) (*models.TradingFlow, error)
	CancelFlow(ctx context.Context, flowID uint) error
	CheckFlowCompletion(ctx context.Context, flowID uint) error
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

type flowService struct {
	cfg           *config.Config
	binanceClient binance.Client
	flowRepo      database.TradingFlowRepository
	orderRepo     database.OrderRepository
	fillRepo      database.FillEventRepository
	auditRepo     database.AuditLogRepository
	notifier      notify.Notifier
}

func NewFlowService(
	cfg *config.Config,
	binanceClient binance.Client,
	flowRepo database.TradingFlowRepository,
	orderRepo database.OrderRepository,
	fillRepo database.FillEventRepository,
	auditRepo database.AuditLogRepository,
) FlowService {
	return &flowService{
		cfg:           cfg,
		binanceClient: binanceClient,
		flowRepo:      flowRepo,
		orderRepo:     orderRepo,
		fillRepo:      fillRepo,
		auditRepo:     auditRepo,
		notifier:      *notify.NewNotifier(),
	}
}

func (s *flowService) CreateFlow(ctx context.Context, req CreateFlowRequest) (*models.TradingFlow, error) {
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

	if err := s.flowRepo.CreateFlowWithOrder(ctx, flow, order); err != nil {
		s.notifier.NotifyPrimaryOrderFailed(flowUUID, req.Symbol, "创建订单记录: "+err.Error())
		return nil, fmt.Errorf("failed to create flow: %w", err)
	}

	s.createAuditLog(ctx, flow.ID, order.ID, models.LogTypeOrderCreate, models.LogSeverityInfo,
		"Created flow and primary order", map[string]interface{}{
			"flow_uuid":  flowUUID,
			"order_uuid": orderUUID,
			"symbol":     req.Symbol,
			"direction":  req.Direction,
			"quantity":   req.Quantity,
		})

	// 提交订单到 Binance
	binanceOrder, err := s.submitOrderToBinance(ctx, order)
	if err != nil {
		s.orderRepo.UpdateStatus(ctx, order.ID, models.OrderStatusRejected, err.Error())
		s.flowRepo.UpdateStatus(ctx, flow.ID, models.FlowStatusCancelled, "Primary order rejected")
		s.notifier.NotifyPrimaryOrderFailed(flowUUID, req.Symbol, "提交到Binance: "+err.Error())
		s.createAuditLog(ctx, flow.ID, order.ID, models.LogTypeError, models.LogSeverityError,
			"Failed to submit order to Binance", map[string]interface{}{
				"error": err.Error(),
			})

		return nil, fmt.Errorf("failed to submit order: %w", err)
	}

	// 可选：验证返回的 client_order_id 与我们发送的一致
	if binanceOrder.ClientOrderID != order.OrderUUID {
		log.Printf("Warning: client_order_id mismatch, sent=%s, received=%s",
			order.OrderUUID, binanceOrder.ClientOrderID)
		order.BinanceClientOrderID = binanceOrder.ClientOrderID
	}

	// 使用 snake_case 字段更新订单信息
	order.BinanceOrderID = strconv.FormatInt(binanceOrder.OrderID, 10)
	order.BinanceClientOrderID = binanceOrder.ClientOrderID
	order.Status = s.mapBinanceStatus(binanceOrder.Status)
	order.FilledQuantity = binanceOrder.ExecutedQty
	order.SubmittedAt = sql.NullTime{Time: time.Now(), Valid: true}

	rawResp, _ := json.Marshal(binanceOrder)
	order.BinanceRawResponse = string(rawResp)

	if err := s.orderRepo.Update(ctx, order); err != nil {
		log.Printf("Failed to update order after submission: %v", err)
	}

	s.createAuditLog(ctx, flow.ID, order.ID, models.LogTypeOrderSubmit, models.LogSeverityInfo,
		"Order submitted to Binance", map[string]interface{}{
			"binance_order_id": binanceOrder.OrderID,
			"client_order_id":  binanceOrder.ClientOrderID,
			"status":           binanceOrder.Status,
			"executed_qty":     binanceOrder.ExecutedQty,
		})

	// 如果订单未立即完成，创建 Track Job
	if !s.isTerminalStatus(binanceOrder.Status) {
		if err := s.createTrackJob(ctx, order); err != nil {
			log.Printf("Warning: Failed to create track job: %v", err)
			s.createAuditLog(ctx, flow.ID, order.ID, models.LogTypeError, models.LogSeverityWarning,
				"Failed to create track job", map[string]interface{}{
					"error": err.Error(),
				})
		}
	} else {
		order.CompletedAt = sql.NullTime{Time: time.Now(), Valid: true}
		s.orderRepo.Update(ctx, order)

		if binanceOrder.Status == "FILLED" && s.cfg.Derivative.Enabled {
			log.Printf("Primary order filled immediately, derivatives should be handled via webhook or direct call")
		}
	}

	return s.flowRepo.GetByID(ctx, flow.ID)
}

func (s *flowService) submitOrderToBinance(ctx context.Context, order *models.Order) (*binance.OrderInfo, error) {
	if order.MarketType == models.MarketTypeSpot {
		req := binance.SpotOrderRequest{
			Symbol:        order.FullSymbol,
			Side:          s.directionToSide(order.Direction),
			Type:          s.orderTypeToString(order.OrderType),
			Quantity:      order.Quantity,
			ClientOrderID: order.OrderUUID,
		}
		if order.OrderType == models.OrderTypeLimit && order.Price.Valid {
			req.Price = order.Price.String
		}
		return s.binanceClient.SpotCreateOrder(ctx, req)
	} else {
		req := binance.CoinMOrderRequest{
			Symbol:        order.FullSymbol,
			Side:          s.directionToSide(order.Direction),
			Type:          s.orderTypeToString(order.OrderType),
			Quantity:      order.Quantity,
			PositionSide:  s.directionToPositionSide(order.Direction),
			ClientOrderID: order.OrderUUID,
		}
		if order.OrderType == models.OrderTypeLimit && order.Price.Valid {
			req.Price = order.Price.String
		}
		return s.binanceClient.CoinMCreateOrder(ctx, req)
	}
}

func (s *flowService) createTrackJob(ctx context.Context, order *models.Order) error {
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

	order.TrackJobID = resp.JobID
	order.TrackJobStatus = resp.Status
	order.WebhookSecret = resp.WebhookSecret

	if err := s.orderRepo.Update(ctx, order); err != nil {
		return fmt.Errorf("failed to update order with track job: %w", err)
	}

	s.createAuditLog(ctx, order.FlowID, order.ID, models.LogTypeSystem, models.LogSeverityInfo,
		"Track job created", map[string]interface{}{
			"job_id": resp.JobID,
			"status": resp.Status,
		})

	return nil
}

// executeTrackJobWithRetry 执行 Track Job 创建，带有错误处理和重试逻辑
func (s *flowService) executeTrackJobWithRetry(ctx context.Context, order *models.Order, req binance.TrackWebhookRequest) (*binance.TrackWebhookResponse, error) {
	const (
		maxRetries       = 5
		baseDelay        = 1 * time.Second
		maxDelay         = 30 * time.Second
		initialJitterMax = 2 * time.Second // 第一次重试前的随机延迟上限
	)

	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// 执行请求
		var resp *binance.TrackWebhookResponse
		var err error

		if order.MarketType == models.MarketTypeSpot {
			resp, err = s.binanceClient.SpotTrackWebhook(ctx, req)
		} else {
			resp, err = s.binanceClient.CoinMTrackWebhook(ctx, req)
		}

		// 成功则返回
		if err == nil {
			return resp, nil
		}

		lastErr = err

		// 检查是否为 ClientError
		var clientErr *binance.ClientError
		if !errors.As(err, &clientErr) {
			// 非 ClientError（如网络错误），记录并通知，不重试
			s.logTrackJobFailure(ctx, order, req, 0, err.Error())
			s.notifyAdminTrackJobFailed(0)
			return nil, err
		}

		statusCode := clientErr.StatusCode

		// 400/401: 客户端错误，记录请求数据，不重试
		if statusCode == 400 || statusCode == 401 {
			s.logTrackJobFailure(ctx, order, req, statusCode, clientErr.Message)
			s.notifyAdminTrackJobFailed(statusCode)
			return nil, err
		}

		// 5xx: 服务端错误，进行重试
		if statusCode >= 500 {
			// 已达最大重试次数
			if attempt == maxRetries {
				log.Printf("Track job creation failed after %d retries: %v", maxRetries, err)
				s.logTrackJobFailure(ctx, order, req, statusCode, clientErr.Message)
				s.notifyAdminTrackJobFailed(statusCode)
				return nil, err
			}

			// 计算退避延迟
			delay := s.calculateBackoffDelay(attempt, baseDelay, maxDelay, initialJitterMax)
			log.Printf("Track job creation failed (attempt %d/%d), retrying in %v: %v",
				attempt+1, maxRetries+1, delay, err)

			// 等待后重试
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
				continue
			}
		}

		// 其他状态码（如 403, 404 等），不重试
		s.logTrackJobFailure(ctx, order, req, statusCode, clientErr.Message)
		s.notifyAdminTrackJobFailed(statusCode)
		return nil, err
	}

	return nil, lastErr
}

// calculateBackoffDelay 计算指数退避延迟
func (s *flowService) calculateBackoffDelay(attempt int, baseDelay, maxDelay, initialJitterMax time.Duration) time.Duration {
	// 指数退避: baseDelay * 2^attempt
	delay := baseDelay * time.Duration(1<<attempt)
	if delay > maxDelay {
		delay = maxDelay
	}

	// 添加 30% 抖动
	jitter := time.Duration(rand.Float64() * float64(delay) * 0.3)
	delay += jitter

	// 第一次重试前添加额外随机延迟
	if attempt == 0 {
		initialJitter := time.Duration(rand.Float64() * float64(initialJitterMax))
		delay += initialJitter
	}

	return delay
}

// logTrackJobFailure 记录 Track Job 创建失败的详细信息
func (s *flowService) logTrackJobFailure(ctx context.Context, order *models.Order, req binance.TrackWebhookRequest, statusCode int, errMessage string) {
	reqData, _ := json.Marshal(req)

	s.createAuditLog(ctx, order.FlowID, order.ID, models.LogTypeError, models.LogSeverityError,
		"Track job creation failed", map[string]interface{}{
			"status_code":      statusCode,
			"error_message":    errMessage,
			"request_data":     string(reqData),
			"order_uuid":       order.OrderUUID,
			"binance_order_id": order.BinanceOrderID,
			"symbol":           order.FullSymbol,
			"market_type":      string(order.MarketType),
			"webhook_url":      req.WebhookURL,
		})
}

// notifyAdminTrackJobFailed 推送 Track Job 失败通知到管理员手机
func (s *flowService) notifyAdminTrackJobFailed(statusCode int) {
	// 构建通知 URL
	title := url.QueryEscape(fmt.Sprintf("追踪订单失败code=%d", statusCode))
	notifyURL := fmt.Sprintf("https://api.day.app/bYQj8CHN5zJWimDzkmYZ33/%s", title)

	// 使用独立的 context，避免被取消
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, notifyURL, nil)
	if err != nil {
		log.Printf("Failed to create admin notification request: %v", err)
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Failed to send admin notification: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Admin notification returned non-200 status: %d", resp.StatusCode)
	} else {
		log.Printf("Admin notification sent successfully for status code: %d", statusCode)
	}
}

func (s *flowService) CancelFlow(ctx context.Context, flowID uint) error {
	flow, err := s.flowRepo.GetByID(ctx, flowID)
	if err != nil {
		return fmt.Errorf("flow not found: %w", err)
	}

	for _, order := range flow.Orders {
		if s.isActiveOrderStatus(order.Status) {
			if order.BinanceOrderID != "" {
				binanceOrderID, _ := strconv.ParseInt(order.BinanceOrderID, 10, 64)
				if order.MarketType == models.MarketTypeSpot {
					s.binanceClient.SpotCancelOrder(ctx, order.FullSymbol, binanceOrderID)
				} else {
					s.binanceClient.CoinMCancelOrder(ctx, order.FullSymbol, binanceOrderID)
				}
			}

			if order.TrackJobID != "" {
				s.binanceClient.CancelTrackJob(ctx, order.TrackJobID)
			}

			s.orderRepo.UpdateStatus(ctx, order.ID, models.OrderStatusCancelled, "Flow cancelled")
		}
	}

	if err := s.flowRepo.UpdateStatus(ctx, flowID, models.FlowStatusCancelled, "User cancelled"); err != nil {
		return err
	}

	s.createAuditLog(ctx, flowID, 0, models.LogTypeFlowComplete, models.LogSeverityInfo,
		"Flow cancelled", nil)

	return nil
}

func (s *flowService) CheckFlowCompletion(ctx context.Context, flowID uint) error {
	flow, err := s.flowRepo.GetByID(ctx, flowID)
	if err != nil {
		return err
	}

	if flow.Status != models.FlowStatusActive {
		return nil
	}

	allTerminal := true
	for _, order := range flow.Orders {
		if !s.isTerminalOrderStatus(order.Status) {
			allTerminal = false
			break
		}
	}

	if allTerminal {
		if err := s.flowRepo.UpdateStatus(ctx, flowID, models.FlowStatusCompleted, "All orders completed"); err != nil {
			return err
		}

		s.createAuditLog(ctx, flowID, 0, models.LogTypeFlowComplete, models.LogSeverityInfo,
			"Flow completed - all orders in terminal state", nil)
	}

	return nil
}

// ============================================
// 辅助方法
// ============================================

func (s *flowService) directionToSide(d models.Direction) string {
	switch d {
	case models.DirectionLong, models.DirectionCloseShort:
		return "BUY"
	case models.DirectionShort, models.DirectionCloseLong:
		return "SELL"
	default:
		return "BUY"
	}
}

func (s *flowService) directionToPositionSide(d models.Direction) string {
	switch d {
	case models.DirectionLong, models.DirectionCloseLong:
		return "LONG"
	case models.DirectionShort, models.DirectionCloseShort:
		return "SHORT"
	default:
		return "BOTH"
	}
}

func (s *flowService) orderTypeToString(t models.OrderType) string {
	switch t {
	case models.OrderTypeLimit:
		return "LIMIT"
	case models.OrderTypeMarket:
		return "MARKET"
	default:
		return "LIMIT"
	}
}

func (s *flowService) mapBinanceStatus(status string) models.OrderStatus {
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

func (s *flowService) isTerminalStatus(status string) bool {
	return status == "FILLED" || status == "CANCELED" || status == "REJECTED" || status == "EXPIRED"
}

func (s *flowService) isTerminalOrderStatus(status models.OrderStatus) bool {
	return status == models.OrderStatusFilled ||
		status == models.OrderStatusCancelled ||
		status == models.OrderStatusRejected ||
		status == models.OrderStatusExpired
}

func (s *flowService) isActiveOrderStatus(status models.OrderStatus) bool {
	return status == models.OrderStatusPending ||
		status == models.OrderStatusSubmitted ||
		status == models.OrderStatusPartiallyFilled
}

func (s *flowService) createAuditLog(ctx context.Context, flowID uint, orderID uint, logType models.LogType, severity models.LogSeverity, message string, detail map[string]interface{}) {
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

	if err := s.auditRepo.Create(ctx, auditLog); err != nil {
		log.Printf("Failed to create audit log: %v", err)
	}
}
