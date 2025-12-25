package database

import (
	"context"
	"time"

	// Pure-Go SQLite driver (no CGO)
	"gorm.io/gorm"

	"github.com/eyes2near/b-trading/internal/models"
)

// ============================================
// Repository接口定义
// ============================================

type TradingFlowRepository interface {
	Create(ctx context.Context, flow *models.TradingFlow) error
	// CreateFlowWithOrder atomically creates a new flow and its first order.
	CreateFlowWithOrder(ctx context.Context, flow *models.TradingFlow, order *models.Order) error
	GetByID(ctx context.Context, id uint) (*models.TradingFlow, error)
	GetByUUID(ctx context.Context, uuid string) (*models.TradingFlow, error)
	Update(ctx context.Context, flow *models.TradingFlow) error
	UpdateStatus(ctx context.Context, id uint, status models.FlowStatus, reason string) error
	ListActive(ctx context.Context, limit, offset int) ([]models.TradingFlow, error)
	GetActiveFlowsView(ctx context.Context, limit, offset int) ([]models.ActiveFlowView, error)
	CountPendingDerivatives(ctx context.Context, flowID uint) (int64, error)
}

type OrderRepository interface {
	Create(ctx context.Context, order *models.Order) error
	GetByID(ctx context.Context, id uint) (*models.Order, error)
	GetByUUID(ctx context.Context, uuid string) (*models.Order, error)
	GetByBinanceOrderID(ctx context.Context, binanceOrderID string) (*models.Order, error)
	Update(ctx context.Context, order *models.Order) error
	UpdateStatus(ctx context.Context, id uint, status models.OrderStatus, reason string) error
	UpdateFilledQuantity(ctx context.Context, id uint, filledQty string) error
	UpdateWithVersion(ctx context.Context, id uint, expectedVersion int, updates map[string]interface{}) (int64, error)
	UpdateOrderStatusAndCreateFill(ctx context.Context, order *models.Order, fill *models.FillEvent) error
	ListByFlowID(ctx context.Context, flowID uint) ([]models.Order, error)
	ListDerivativeOrders(ctx context.Context, parentOrderID uint) ([]models.Order, error)
	GetPrimaryOrder(ctx context.Context, flowID uint) (*models.Order, error)
	CountActiveDerivatives(ctx context.Context, flowID uint) (int64, error)
	GetOrderDetailsView(ctx context.Context, orderID uint) (*models.OrderDetailView, error)
	CreateStatusHistory(ctx context.Context, history *models.OrderStatusHistory) error
}

type FillEventRepository interface {
	Create(ctx context.Context, event *models.FillEvent) error
	GetByID(ctx context.Context, id uint) (*models.FillEvent, error)
	GetByUUID(ctx context.Context, uuid string) (*models.FillEvent, error)
	ListByOrderID(ctx context.Context, orderID uint) ([]models.FillEvent, error)
	ListUnprocessed(ctx context.Context, limit int) ([]models.FillEvent, error)
	MarkAsProcessed(ctx context.Context, id uint, derivativeOrderID uint) error
	GetTotalFilledQuantity(ctx context.Context, orderID uint) (string, error)
	Update(ctx context.Context, event *models.FillEvent) error // 新增
}

type AuditLogRepository interface {
	Create(ctx context.Context, log *models.AuditLog) error
	ListByFlowID(ctx context.Context, flowID uint, limit, offset int) ([]models.AuditLog, error)
	ListByOrderID(ctx context.Context, orderID uint, limit, offset int) ([]models.AuditLog, error)
	ListBySeverity(ctx context.Context, severity models.LogSeverity, limit, offset int) ([]models.AuditLog, error)
}

type MarketConfigRepository interface {
	Create(ctx context.Context, config *models.MarketConfig) error
	Get(ctx context.Context, marketType, symbol, key string) (*models.MarketConfig, error)
	Update(ctx context.Context, config *models.MarketConfig) error
	ListByMarket(ctx context.Context, marketType, symbol string) ([]models.MarketConfig, error)
}

// ============================================
// Repository实现
// ============================================

// TradingFlowRepo 交易流程仓储实现
type TradingFlowRepo struct {
	db *gorm.DB
}

func NewTradingFlowRepo(db *gorm.DB) TradingFlowRepository {
	return &TradingFlowRepo{db: db}
}

func (r *TradingFlowRepo) Create(ctx context.Context, flow *models.TradingFlow) error {
	return r.db.WithContext(ctx).Create(flow).Error
}

func (r *TradingFlowRepo) GetByID(ctx context.Context, id uint) (*models.TradingFlow, error) {
	var flow models.TradingFlow
	err := r.db.WithContext(ctx).
		Preload("Orders").
		Preload("Orders.FillEvents").
		First(&flow, id).Error
	return &flow, err
}

func (r *TradingFlowRepo) GetByUUID(ctx context.Context, uuid string) (*models.TradingFlow, error) {
	var flow models.TradingFlow
	err := r.db.WithContext(ctx).
		Preload("Orders").
		Preload("Orders.FillEvents").
		Where("flow_uuid = ?", uuid).
		First(&flow).Error
	return &flow, err
}

func (r *TradingFlowRepo) CreateFlowWithOrder(ctx context.Context, flow *models.TradingFlow, order *models.Order) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Create the TradingFlow
		if err := tx.Create(flow).Error; err != nil {
			return err
		}

		// 2. Associate the Order with the new Flow's ID
		order.FlowID = flow.ID

		// 3. Create the Order
		if err := tx.Create(order).Error; err != nil {
			return err
		}

		return nil
	})
}

func (r *TradingFlowRepo) Update(ctx context.Context, flow *models.TradingFlow) error {
	return r.db.WithContext(ctx).Save(flow).Error
}

func (r *TradingFlowRepo) UpdateStatus(ctx context.Context, id uint, status models.FlowStatus, reason string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 获取当前状态
		var flow models.TradingFlow
		if err := tx.First(&flow, id).Error; err != nil {
			return err
		}

		oldStatus := flow.Status

		// 更新状态
		updates := map[string]interface{}{
			"status": status,
		}
		if status == models.FlowStatusCompleted || status == models.FlowStatusCancelled {
			updates["completed_at"] = time.Now()
		}

		if err := tx.Model(&models.TradingFlow{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}

		// 记录状态历史
		pendingCount, _ := r.CountPendingDerivatives(ctx, id)
		history := &models.FlowStatusHistory{
			FlowID:                  id,
			FromStatus:              string(oldStatus),
			ToStatus:                string(status),
			ChangedAt:               time.Now(),
			Reason:                  reason,
			PendingDerivativesCount: int(pendingCount),
		}
		return tx.Create(history).Error
	})
}

func (r *TradingFlowRepo) ListActive(ctx context.Context, limit, offset int) ([]models.TradingFlow, error) {
	var flows []models.TradingFlow
	err := r.db.WithContext(ctx).
		Where("status = ?", models.FlowStatusActive).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&flows).Error
	return flows, err
}

func (r *TradingFlowRepo) GetActiveFlowsView(ctx context.Context, limit, offset int) ([]models.ActiveFlowView, error) {
	var views []models.ActiveFlowView
	err := r.db.WithContext(ctx).
		Table("v_active_flows").
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&views).Error
	return views, err
}

func (r *TradingFlowRepo) CountPendingDerivatives(ctx context.Context, flowID uint) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.Order{}).
		Where("flow_id = ? AND order_role = ? AND status IN (?)",
			flowID,
			models.OrderRoleDerivative,
			[]models.OrderStatus{
				models.OrderStatusPending,
				models.OrderStatusSubmitted,
				models.OrderStatusPartiallyFilled,
			}).
		Count(&count).Error
	return count, err
}

// OrderRepo 订单仓储实现
type OrderRepo struct {
	db *gorm.DB
}

func NewOrderRepo(db *gorm.DB) OrderRepository {
	return &OrderRepo{db: db}
}

func (r *OrderRepo) Create(ctx context.Context, order *models.Order) error {
	return r.db.WithContext(ctx).Create(order).Error
}

func (r *OrderRepo) GetByID(ctx context.Context, id uint) (*models.Order, error) {
	var order models.Order
	err := r.db.WithContext(ctx).
		Preload("Flow").
		Preload("ParentOrder").
		Preload("FillEvents").
		First(&order, id).Error
	return &order, err
}

func (r *OrderRepo) GetByUUID(ctx context.Context, uuid string) (*models.Order, error) {
	var order models.Order
	err := r.db.WithContext(ctx).
		Preload("Flow").
		Preload("FillEvents").
		Where("order_uuid = ?", uuid).
		First(&order).Error
	return &order, err
}

func (r *OrderRepo) GetByBinanceOrderID(ctx context.Context, binanceOrderID string) (*models.Order, error) {
	var order models.Order
	err := r.db.WithContext(ctx).
		Where("binance_order_id = ?", binanceOrderID).
		First(&order).Error
	return &order, err
}

func (r *OrderRepo) Update(ctx context.Context, order *models.Order) error {
	return r.db.WithContext(ctx).Save(order).Error
}

func (r *OrderRepo) UpdateStatus(ctx context.Context, id uint, status models.OrderStatus, reason string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 获取当前状态
		var order models.Order
		if err := tx.First(&order, id).Error; err != nil {
			return err
		}

		oldStatus := order.Status

		// 更新状态
		updates := map[string]interface{}{
			"status": status,
		}
		if status == models.OrderStatusFilled || status == models.OrderStatusCancelled {
			updates["completed_at"] = time.Now()
		}

		if err := tx.Model(&models.Order{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}

		// 记录状态历史
		history := &models.OrderStatusHistory{
			OrderID:    id,
			FromStatus: string(oldStatus),
			ToStatus:   string(status),
			ChangedAt:  time.Now(),
			Reason:     reason,
		}
		return tx.Create(history).Error
	})
}

func (r *OrderRepo) UpdateFilledQuantity(ctx context.Context, id uint, filledQty string) error {
	return r.db.WithContext(ctx).
		Model(&models.Order{}).
		Where("id = ?", id).
		Update("filled_quantity", filledQty).Error
}

// UpdateWithVersion 使用乐观锁更新订单
func (r *OrderRepo) UpdateWithVersion(ctx context.Context, id uint, expectedVersion int, updates map[string]interface{}) (int64, error) {
	result := r.db.WithContext(ctx).
		Model(&models.Order{}).
		Where("id = ? AND version = ?", id, expectedVersion).
		Updates(updates)

	return result.RowsAffected, result.Error
}

func (r *OrderRepo) CreateStatusHistory(ctx context.Context, history *models.OrderStatusHistory) error {
	return r.db.WithContext(ctx).Create(history).Error
}

func (r *OrderRepo) ListByFlowID(ctx context.Context, flowID uint) ([]models.Order, error) {
	var orders []models.Order
	err := r.db.WithContext(ctx).
		Where("flow_id = ?", flowID).
		Order("created_at ASC").
		Find(&orders).Error
	return orders, err
}

func (r *OrderRepo) ListDerivativeOrders(ctx context.Context, parentOrderID uint) ([]models.Order, error) {
	var orders []models.Order
	err := r.db.WithContext(ctx).
		Where("parent_order_id = ?", parentOrderID).
		Order("created_at ASC").
		Find(&orders).Error
	return orders, err
}

func (r *OrderRepo) GetPrimaryOrder(ctx context.Context, flowID uint) (*models.Order, error) {
	var order models.Order
	err := r.db.WithContext(ctx).
		Where("flow_id = ? AND order_role = ?", flowID, models.OrderRolePrimary).
		First(&order).Error
	return &order, err
}

func (r *OrderRepo) CountActiveDerivatives(ctx context.Context, flowID uint) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.Order{}).
		Where("flow_id = ? AND order_role = ? AND status IN (?)",
			flowID,
			models.OrderRoleDerivative,
			[]models.OrderStatus{
				models.OrderStatusPending,
				models.OrderStatusSubmitted,
				models.OrderStatusPartiallyFilled,
			}).
		Count(&count).Error
	return count, err
}

func (r *OrderRepo) GetOrderDetailsView(ctx context.Context, orderID uint) (*models.OrderDetailView, error) {
	var view models.OrderDetailView
	err := r.db.WithContext(ctx).
		Table("v_order_details").
		Where("id = ?", orderID).
		First(&view).Error
	return &view, err
}

func (r *OrderRepo) UpdateOrderStatusAndCreateFill(ctx context.Context, order *models.Order, fill *models.FillEvent) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Update the Order status and filled quantity by saving the whole object
		if err := tx.Save(order).Error; err != nil {
			return err
		}

		// 2. Create the FillEvent
		if err := tx.Create(fill).Error; err != nil {
			return err
		}

		// 3. Create an OrderStatusHistory record
		history := &models.OrderStatusHistory{
			OrderID:         order.ID,
			ToStatus:        string(order.Status),
			ChangedAt:       time.Now(),
			Reason:          "Order update from execution report",
			BinanceResponse: fill.RawData, // Store the raw event for auditing
		}
		if err := tx.Create(history).Error; err != nil {
			return err
		}

		return nil
	})
}

// FillEventRepo 成交事件仓储实现
type FillEventRepo struct {
	db *gorm.DB
}

func NewFillEventRepo(db *gorm.DB) FillEventRepository {
	return &FillEventRepo{db: db}
}

func (r *FillEventRepo) Create(ctx context.Context, event *models.FillEvent) error {
	return r.db.WithContext(ctx).Create(event).Error
}

func (r *FillEventRepo) GetByID(ctx context.Context, id uint) (*models.FillEvent, error) {
	var event models.FillEvent
	err := r.db.WithContext(ctx).
		Preload("Order").
		Preload("Flow").
		First(&event, id).Error
	return &event, err
}

func (r *FillEventRepo) GetByUUID(ctx context.Context, uuid string) (*models.FillEvent, error) {
	var event models.FillEvent
	err := r.db.WithContext(ctx).
		Preload("Order").
		Where("fill_uuid = ?", uuid).
		First(&event).Error
	return &event, err
}

func (r *FillEventRepo) ListByOrderID(ctx context.Context, orderID uint) ([]models.FillEvent, error) {
	var events []models.FillEvent
	err := r.db.WithContext(ctx).
		Where("order_id = ?", orderID).
		Order("fill_time ASC").
		Find(&events).Error
	return events, err
}

func (r *FillEventRepo) ListUnprocessed(ctx context.Context, limit int) ([]models.FillEvent, error) {
	var events []models.FillEvent
	err := r.db.WithContext(ctx).
		Preload("Order").
		Where("has_triggered_derivative = ?", false).
		Order("fill_time ASC").
		Limit(limit).
		Find(&events).Error
	return events, err
}

func (r *FillEventRepo) MarkAsProcessed(ctx context.Context, id uint, derivativeOrderID uint) error {
	return r.db.WithContext(ctx).
		Model(&models.FillEvent{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"has_triggered_derivative": true,
			"derivative_order_id":      derivativeOrderID,
			"processed_at":             time.Now(),
		}).Error
}

func (r *FillEventRepo) GetTotalFilledQuantity(ctx context.Context, orderID uint) (string, error) {
	var result struct {
		Total string
	}
	err := r.db.WithContext(ctx).
		Model(&models.FillEvent{}).
		Select("COALESCE(SUM(CAST(fill_quantity AS REAL)), 0) as total").
		Where("order_id = ?", orderID).
		Scan(&result).Error
	return result.Total, err
}

func (r *FillEventRepo) Update(ctx context.Context, event *models.FillEvent) error {
	return r.db.WithContext(ctx).Save(event).Error
}

// AuditLogRepo 审计日志仓储实现
type AuditLogRepo struct {
	db *gorm.DB
}

func NewAuditLogRepo(db *gorm.DB) AuditLogRepository {
	return &AuditLogRepo{db: db}
}

func (r *AuditLogRepo) Create(ctx context.Context, log *models.AuditLog) error {
	return r.db.WithContext(ctx).Create(log).Error
}

func (r *AuditLogRepo) ListByFlowID(ctx context.Context, flowID uint, limit, offset int) ([]models.AuditLog, error) {
	var logs []models.AuditLog
	err := r.db.WithContext(ctx).
		Where("flow_id = ?", flowID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&logs).Error
	return logs, err
}

func (r *AuditLogRepo) ListByOrderID(ctx context.Context, orderID uint, limit, offset int) ([]models.AuditLog, error) {
	var logs []models.AuditLog
	err := r.db.WithContext(ctx).
		Where("order_id = ?", orderID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&logs).Error
	return logs, err
}

func (r *AuditLogRepo) ListBySeverity(ctx context.Context, severity models.LogSeverity, limit, offset int) ([]models.AuditLog, error) {
	var logs []models.AuditLog
	err := r.db.WithContext(ctx).
		Where("severity = ?", severity).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&logs).Error
	return logs, err
}

// MarketConfigRepo 市场配置仓储实现
type MarketConfigRepo struct {
	db *gorm.DB
}

func NewMarketConfigRepo(db *gorm.DB) MarketConfigRepository {
	return &MarketConfigRepo{db: db}
}

func (r *MarketConfigRepo) Create(ctx context.Context, config *models.MarketConfig) error {
	return r.db.WithContext(ctx).Create(config).Error
}

func (r *MarketConfigRepo) Get(ctx context.Context, marketType, symbol, key string) (*models.MarketConfig, error) {
	var config models.MarketConfig
	err := r.db.WithContext(ctx).
		Where("market_type = ? AND symbol = ? AND config_key = ? AND is_active = ?",
			marketType, symbol, key, true).
		First(&config).Error
	return &config, err
}

func (r *MarketConfigRepo) Update(ctx context.Context, config *models.MarketConfig) error {
	return r.db.WithContext(ctx).Save(config).Error
}

func (r *MarketConfigRepo) ListByMarket(ctx context.Context, marketType, symbol string) ([]models.MarketConfig, error) {
	var configs []models.MarketConfig
	err := r.db.WithContext(ctx).
		Where("market_type = ? AND symbol = ? AND is_active = ?", marketType, symbol, true).
		Find(&configs).Error
	return configs, err
}
