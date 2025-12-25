// cmd/server/main.go

package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eyes2near/b-trading/internal/binance"
	"github.com/eyes2near/b-trading/internal/config"
	"github.com/eyes2near/b-trading/internal/database"
	"github.com/eyes2near/b-trading/internal/server"
	"gorm.io/gorm/logger"
)

func main() {
	configPath := flag.String("config", "./configs/config.yaml", "path to config file")
	flag.Parse()

	// 1. Load Config
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Starting %s v%s in %s mode", cfg.App.Name, cfg.App.Version, cfg.App.Env)

	// 2. Init Binance REST API Client
	binanceClient := binance.NewClient(&cfg.BinanceREST)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	health, err := binanceClient.Health(ctx)
	if err != nil {
		log.Printf("Warning: Binance REST API health check failed: %v", err)
	} else {
		log.Printf("Binance REST API connected: %s (ok=%v)", health.Service, health.OK)
	}

	// 3. Init Database
	dbConfig := &database.DBConfig{
		DSN:             cfg.Database.DSN,
		MaxIdleConns:    cfg.Database.MaxIdleConns,
		MaxOpenConns:    cfg.Database.MaxOpenConns,
		ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
		ConnMaxIdleTime: cfg.Database.ConnMaxIdleTime,
		LogLevel:        logger.LogLevel(cfg.Database.LogLevel),
	}
	db, err := database.InitDB(dbConfig)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	log.Println("Database initialized successfully")

	// 初始化默认衍生订单规则
	if err := database.SeedDerivativeRules(db); err != nil {
		log.Printf("Warning: Failed to seed derivative rules: %v", err)
		// 不中断启动，只记录警告
	}

	// 4. Init Repositories
	flowRepo := database.NewTradingFlowRepo(db)
	orderRepo := database.NewOrderRepo(db)
	fillRepo := database.NewFillEventRepo(db)
	deliveryRepo := database.NewWebhookDeliveryRepo(db)
	auditRepo := database.NewAuditLogRepo(db)
	derivativeRuleRepo := database.NewDerivativeRuleRepo(db)

	// 5. Init Server
	srv := server.NewServer(cfg, binanceClient, flowRepo, orderRepo, fillRepo, deliveryRepo, auditRepo, derivativeRuleRepo)

	go func() {
		addr := ":" + cfg.Server.Port
		if cfg.Server.Port == "" {
			addr = ":8080"
		}
		log.Printf("Server listening on %s", addr)
		log.Printf("Webhook endpoint: %s", cfg.GetWebhookURL())
		if err := srv.Run(addr); err != nil {
			log.Fatalf("Server run failed: %v", err)
		}
	}()

	// 6. Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	if err := database.CloseDB(db); err != nil {
		log.Fatalf("Database close failed: %+v", err)
	}

	log.Println("Server stopped gracefully")
}
