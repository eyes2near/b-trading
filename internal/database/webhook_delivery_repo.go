// internal/database/webhook_delivery_repo.go

package database

import (
	"context"

	"github.com/eyes2near/b-trading/internal/models"
	"gorm.io/gorm"
)

type WebhookDeliveryRepository interface {
	Create(ctx context.Context, delivery *models.WebhookDelivery) error
	Exists(ctx context.Context, deliveryID string) (bool, error)
	Update(ctx context.Context, delivery *models.WebhookDelivery) error
	GetByDeliveryID(ctx context.Context, deliveryID string) (*models.WebhookDelivery, error)
}

type webhookDeliveryRepo struct {
	db *gorm.DB
}

func NewWebhookDeliveryRepo(db *gorm.DB) WebhookDeliveryRepository {
	return &webhookDeliveryRepo{db: db}
}

func (r *webhookDeliveryRepo) Create(ctx context.Context, delivery *models.WebhookDelivery) error {
	return r.db.WithContext(ctx).Create(delivery).Error
}

func (r *webhookDeliveryRepo) Exists(ctx context.Context, deliveryID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.WebhookDelivery{}).
		Where("delivery_id = ?", deliveryID).
		Count(&count).Error
	return count > 0, err
}

func (r *webhookDeliveryRepo) Update(ctx context.Context, delivery *models.WebhookDelivery) error {
	return r.db.WithContext(ctx).Save(delivery).Error
}

func (r *webhookDeliveryRepo) GetByDeliveryID(ctx context.Context, deliveryID string) (*models.WebhookDelivery, error) {
	var delivery models.WebhookDelivery
	err := r.db.WithContext(ctx).
		Where("delivery_id = ?", deliveryID).
		First(&delivery).Error
	return &delivery, err
}
