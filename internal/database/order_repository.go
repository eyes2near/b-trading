package database

import (
	"context"

	"github.com/eyes2near/b-trading/internal/models"
	"gorm.io/gorm"
)

type orderRepository struct {
	db *gorm.DB
}

func NewOrderRepository(db *gorm.DB) *orderRepository {
	return &orderRepository{db: db}
}

func (r *orderRepository) CreateTradingFlow(ctx context.Context, flow *models.TradingFlow) error {
	return r.db.WithContext(ctx).Create(flow).Error
}

func (r *orderRepository) CreateOrder(ctx context.Context, order *models.Order) error {
	return r.db.WithContext(ctx).Create(order).Error
}
