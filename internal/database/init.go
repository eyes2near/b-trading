// internal/database/init.go

package database

import (
	"fmt"
	"log"
	"time"

	"github.com/glebarez/sqlite" // Pure-Go SQLite driver (no CGO)
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/eyes2near/b-trading/internal/models"
)

// ============================================
// 数据库初始化
// ============================================

type DBConfig struct {
	DSN             string        // 数据源名称
	MaxIdleConns    int           // 最大空闲连接数
	MaxOpenConns    int           // 最大打开连接数
	ConnMaxLifetime time.Duration // 连接最大生存时间
	ConnMaxIdleTime time.Duration // 连接最大空闲时间
	LogLevel        logger.LogLevel
}

// DefaultConfig 返回默认配置
func DefaultConfig(dbPath string) *DBConfig {
	return &DBConfig{
		DSN:             dbPath,
		MaxIdleConns:    10,
		MaxOpenConns:    100,
		ConnMaxLifetime: time.Hour,
		ConnMaxIdleTime: 10 * time.Minute,
		LogLevel:        logger.Info,
	}
}

// InitDB 初始化数据库连接
func InitDB(config *DBConfig) (*gorm.DB, error) {
	// 配置日志
	newLogger := logger.New(
		log.New(log.Writer(), "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  config.LogLevel,
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		},
	)

	// 启用外键约束的DSN (SQLite默认禁用外键)
	dsnWithForeignKeys := config.DSN
	if config.DSN != ":memory:" && config.DSN != "" {
		dsnWithForeignKeys = config.DSN + "?_pragma=foreign_keys(1)"
	} else {
		dsnWithForeignKeys = ":memory:?_pragma=foreign_keys(1)"
	}

	// 打开数据库连接 (Pure-Go driver, no CGO required)
	db, err := gorm.Open(sqlite.Open(dsnWithForeignKeys), &gorm.Config{
		Logger:                                   newLogger,
		DisableForeignKeyConstraintWhenMigrating: false,
		PrepareStmt:                              true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect database: %w", err)
	}

	// 获取底层sql.DB
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get sql.DB: %w", err)
	}

	// 配置连接池
	sqlDB.SetMaxIdleConns(config.MaxIdleConns)
	sqlDB.SetMaxOpenConns(config.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(config.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(config.ConnMaxIdleTime)

	// 执行迁移
	if err := MigrateDB(db); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	// 创建视图
	if err := CreateViews(db); err != nil {
		return nil, fmt.Errorf("failed to create views: %w", err)
	}

	log.Println("Database initialized successfully")
	return db, nil
}

// MigrateDB 执行数据库迁移
func MigrateDB(db *gorm.DB) error {
	log.Println("Running database migrations...")

	err := db.AutoMigrate(
		// 核心业务模型
		&models.TradingFlow{},
		&models.Order{},
		&models.FillEvent{},

		// 状态历史
		&models.OrderStatusHistory{},
		&models.FlowStatusHistory{},

		// 审计与配置
		&models.AuditLog{},
		&models.MarketConfig{},

		// Webhook 幂等去重 (新增)
		&models.WebhookDelivery{},

		// 衍生订单规则
		&models.DerivativeRule{},
		&models.DerivativeRuleLog{},
	)

	if err != nil {
		return fmt.Errorf("auto migrate failed: %w", err)
	}

	log.Println("Database migrations completed")
	return nil
}

// SeedDerivativeRules 初始化示例规则（可选）
func SeedDerivativeRules(db *gorm.DB) error {
	return seedDefaultDerivativeRules(db)
}

// CreateViews 创建数据库视图
func CreateViews(db *gorm.DB) error {
	log.Println("Creating database views...")

	// 删除旧视图（确保视图定义更新生效）
	if err := db.Exec("DROP VIEW IF EXISTS v_active_flows").Error; err != nil {
		return fmt.Errorf("drop v_active_flows failed: %w", err)
	}
	if err := db.Exec("DROP VIEW IF EXISTS v_order_details").Error; err != nil {
		return fmt.Errorf("drop v_order_details failed: %w", err)
	}

	// 创建活跃流程视图
	activeFlowsView := `
	CREATE VIEW v_active_flows AS
	SELECT 
		f.id,
		f.flow_uuid,
		f.status,
		f.created_at,
		COUNT(DISTINCT o.id) as total_orders,
		COUNT(DISTINCT CASE WHEN o.order_role = 'primary' THEN o.id END) as primary_orders,
		COUNT(DISTINCT CASE WHEN o.order_role = 'derivative' THEN o.id END) as derivative_orders,
		COUNT(DISTINCT CASE WHEN o.status IN ('pending', 'submitted', 'partially_filled') THEN o.id END) as active_orders,
		COALESCE(SUM(CAST(fe.fill_quantity AS REAL)), 0) as total_filled_quantity
	FROM trading_flows f
	LEFT JOIN orders o ON f.id = o.flow_id AND o.deleted_at IS NULL
	LEFT JOIN fill_events fe ON o.id = fe.order_id
	WHERE f.status = 'active' AND f.deleted_at IS NULL
	GROUP BY f.id`

	if err := db.Exec(activeFlowsView).Error; err != nil {
		return fmt.Errorf("create v_active_flows failed: %w", err)
	}

	// 创建订单详情视图
	orderDetailsView := `
	CREATE VIEW v_order_details AS
	SELECT 
		o.id,
		o.order_uuid,
		o.order_role,
		o.market_type,
		o.full_symbol,
		o.direction,
		o.order_type,
		o.price,
		o.quantity,
		o.filled_quantity,
		o.status,
		o.binance_order_id,
		o.track_job_id,
		o.track_job_status,
		f.flow_uuid,
		p.order_uuid as parent_order_uuid,
		COUNT(fe.id) as fill_count,
		COALESCE(SUM(CAST(fe.fill_quantity AS REAL) * CAST(fe.fill_price AS REAL)), 0) as total_fill_value
	FROM orders o
	JOIN trading_flows f ON o.flow_id = f.id
	LEFT JOIN orders p ON o.parent_order_id = p.id
	LEFT JOIN fill_events fe ON o.id = fe.order_id
	WHERE o.deleted_at IS NULL
	GROUP BY o.id`

	if err := db.Exec(orderDetailsView).Error; err != nil {
		return fmt.Errorf("create v_order_details failed: %w", err)
	}

	log.Println("Database views created")
	return nil
}

// CloseDB 关闭数据库连接
func CloseDB(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
