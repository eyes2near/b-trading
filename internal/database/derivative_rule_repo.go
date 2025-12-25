// internal/database/derivative_rule_repo.go

package database

import (
	"context"

	"github.com/eyes2near/b-trading/internal/models"
	"gorm.io/gorm"
)

type DerivativeRuleRepository interface {
	// CRUD
	Create(ctx context.Context, rule *models.DerivativeRule) error
	Update(ctx context.Context, rule *models.DerivativeRule) error
	Delete(ctx context.Context, id uint) error
	GetByID(ctx context.Context, id uint) (*models.DerivativeRule, error)
	GetByName(ctx context.Context, name string) (*models.DerivativeRule, error)

	// 查询
	ListEnabled(ctx context.Context) ([]models.DerivativeRule, error)
	ListAll(ctx context.Context) ([]models.DerivativeRule, error)

	// 执行日志
	CreateLog(ctx context.Context, log *models.DerivativeRuleLog) error
	ListLogsByOrderID(ctx context.Context, orderID uint, limit int) ([]models.DerivativeRuleLog, error)
	UpdateLogDerivativeOrderID(ctx context.Context, logID uint, derivativeOrderID uint) error
}

type derivativeRuleRepo struct {
	db *gorm.DB
}

func NewDerivativeRuleRepo(db *gorm.DB) DerivativeRuleRepository {
	return &derivativeRuleRepo{db: db}
}

func (r *derivativeRuleRepo) Create(ctx context.Context, rule *models.DerivativeRule) error {
	return r.db.WithContext(ctx).Create(rule).Error
}

func (r *derivativeRuleRepo) Update(ctx context.Context, rule *models.DerivativeRule) error {
	return r.db.WithContext(ctx).Save(rule).Error
}

func (r *derivativeRuleRepo) Delete(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Delete(&models.DerivativeRule{}, id).Error
}

func (r *derivativeRuleRepo) GetByID(ctx context.Context, id uint) (*models.DerivativeRule, error) {
	var rule models.DerivativeRule
	err := r.db.WithContext(ctx).First(&rule, id).Error
	return &rule, err
}

func (r *derivativeRuleRepo) GetByName(ctx context.Context, name string) (*models.DerivativeRule, error) {
	var rule models.DerivativeRule
	err := r.db.WithContext(ctx).Where("name = ?", name).First(&rule).Error
	return &rule, err
}

func (r *derivativeRuleRepo) ListEnabled(ctx context.Context) ([]models.DerivativeRule, error) {
	var rules []models.DerivativeRule
	err := r.db.WithContext(ctx).
		Where("enabled = ?", true).
		Order("priority ASC, id ASC").
		Find(&rules).Error
	return rules, err
}

func (r *derivativeRuleRepo) ListAll(ctx context.Context) ([]models.DerivativeRule, error) {
	var rules []models.DerivativeRule
	err := r.db.WithContext(ctx).
		Order("priority ASC, id ASC").
		Find(&rules).Error
	return rules, err
}

func (r *derivativeRuleRepo) CreateLog(ctx context.Context, log *models.DerivativeRuleLog) error {
	return r.db.WithContext(ctx).Create(log).Error
}

func (r *derivativeRuleRepo) ListLogsByOrderID(ctx context.Context, orderID uint, limit int) ([]models.DerivativeRuleLog, error) {
	var logs []models.DerivativeRuleLog
	err := r.db.WithContext(ctx).
		Where("primary_order_id = ?", orderID).
		Order("executed_at DESC").
		Limit(limit).
		Find(&logs).Error
	return logs, err
}

func (r *derivativeRuleRepo) UpdateLogDerivativeOrderID(ctx context.Context, logID uint, derivativeOrderID uint) error {
	return r.db.WithContext(ctx).
		Model(&models.DerivativeRuleLog{}).
		Where("id = ?", logID).
		Update("derivative_order_id", derivativeOrderID).Error
}
