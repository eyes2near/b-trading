// internal/service/audit_service.go
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"time"

	"github.com/eyes2near/b-trading/internal/database"
	"github.com/eyes2near/b-trading/internal/models"
)

// AuditService 审计服务接口
type AuditService interface {
	// LogEvent 记录审计日志
	LogEvent(ctx context.Context, flowID, orderID uint, logType models.LogType, severity models.LogSeverity, message string, detail map[string]interface{})

	// RecordOrderStatusChange 记录订单状态变更
	RecordOrderStatusChange(ctx context.Context, orderID uint, fromStatus, toStatus models.OrderStatus, reason string, binanceResponse string)

	// RecordFlowStatusChange 记录流程状态变更
	RecordFlowStatusChange(ctx context.Context, flowID uint, fromStatus, toStatus models.FlowStatus, reason string, pendingDerivatives int)
}

type auditService struct {
	auditRepo database.AuditLogRepository
	orderRepo database.OrderRepository
}

// NewAuditService 创建审计服务
func NewAuditService(auditRepo database.AuditLogRepository, orderRepo database.OrderRepository) AuditService {
	return &auditService{
		auditRepo: auditRepo,
		orderRepo: orderRepo,
	}
}

func (s *auditService) LogEvent(ctx context.Context, flowID, orderID uint, logType models.LogType, severity models.LogSeverity, message string, detail map[string]interface{}) {
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

func (s *auditService) RecordOrderStatusChange(ctx context.Context, orderID uint, fromStatus, toStatus models.OrderStatus, reason string, binanceResponse string) {
	history := &models.OrderStatusHistory{
		OrderID:         orderID,
		FromStatus:      string(fromStatus),
		ToStatus:        string(toStatus),
		ChangedAt:       time.Now(),
		Reason:          reason,
		BinanceResponse: binanceResponse,
	}

	if err := s.orderRepo.CreateStatusHistory(ctx, history); err != nil {
		log.Printf("Failed to create order status history: %v", err)
	}
}

func (s *auditService) RecordFlowStatusChange(ctx context.Context, flowID uint, fromStatus, toStatus models.FlowStatus, reason string, pendingDerivatives int) {
	// 这个可以通过 FlowRepository 实现，这里简化处理
	log.Printf("Flow %d status changed: %s -> %s, reason: %s", flowID, fromStatus, toStatus, reason)
}
