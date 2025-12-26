// internal/server/server.go

package server

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/eyes2near/b-trading/internal/api"
	"github.com/eyes2near/b-trading/internal/binance"
	"github.com/eyes2near/b-trading/internal/config"
	"github.com/eyes2near/b-trading/internal/database"
	"github.com/eyes2near/b-trading/internal/derivative"
	"github.com/eyes2near/b-trading/internal/market"
	"github.com/eyes2near/b-trading/internal/notify"
	"github.com/eyes2near/b-trading/internal/service"
	"github.com/gin-gonic/gin"
)

type Server struct {
	engine    *gin.Engine
	cfg       *config.Config
	streamMgr *market.StreamManager // 添加字段以便关闭时清理
}

func NewServer(
	cfg *config.Config,
	binanceClient binance.Client,
	flowRepo database.TradingFlowRepository,
	orderRepo database.OrderRepository,
	fillRepo database.FillEventRepository,
	deliveryRepo database.WebhookDeliveryRepository,
	auditRepo database.AuditLogRepository,
	ruleRepo database.DerivativeRuleRepository,
) *Server {
	r := gin.Default()

	// =========================================================
	// CORS 中间件配置
	// =========================================================
	r.Use(CORSMiddleware())

	// 初始化通知服务
	notifier := notify.NewNotifier()

	// 初始化衍生订单引擎
	derivativeEngine := derivative.NewEngine(binanceClient, ruleRepo, notifier)
	derivativeEngine.StartAutoRefresh(context.Background(), 5*time.Minute)

	// 初始化服务层
	flowService := service.NewFlowService(cfg, binanceClient, flowRepo, orderRepo, fillRepo, auditRepo, notifier, derivativeEngine)
	webhookProcessor := service.NewWebhookProcessor(
		cfg, binanceClient, flowRepo, orderRepo, fillRepo,
		deliveryRepo, auditRepo, flowService, derivativeEngine, ruleRepo, notifier,
	)

	// 初始化 Handler
	h := api.NewHandler(flowRepo, orderRepo, cfg, binanceClient, flowService, ruleRepo, derivativeEngine)
	webhookHandler := api.NewWebhookHandler(webhookProcessor)

	// =========================================================
	// WebSocket 模块初始化
	// =========================================================
	streamMgr := market.NewStreamManager(cfg.MarketStream)
	streamMgr.Run()

	// -------------------------------------------------------------------------
	// 路由注册
	// -------------------------------------------------------------------------

	// WebSocket 路由 - 市场数据流
	r.GET("/ws/market", gin.WrapF(api.MarketStreamHandler(streamMgr)))

	// 内部 Webhook 路由
	r.POST("/internal/webhook/binance", webhookHandler.HandleBinanceWebhook)

	// API 路由组
	apiGroup := r.Group("/api")
	{
		// 1. 交易流程管理 (Trading Flows)
		apiGroup.GET("/flows", h.GetActiveFlows)
		apiGroup.GET("/flows/:id", h.GetFlowDetail)
		apiGroup.POST("/flows", h.CreateFlow)
		apiGroup.POST("/flows/:id/cancel", h.CancelFlow)

		// 2. 市场数据查询 (Market Data)
		apiGroup.GET("/prices/spot/:symbol", h.GetSpotPrice)
		apiGroup.GET("/prices/coinm/:symbol", h.GetCoinMPrice)
		apiGroup.GET("/coinm/quarter-symbols/:base", h.GetQuarterSymbols)

		// 3. 衍生订单规则管理
		rulesGroup := apiGroup.Group("/derivative-rules")
		{
			rulesGroup.GET("", h.ListDerivativeRules)
			rulesGroup.POST("", h.CreateDerivativeRule)
			rulesGroup.GET("/:id", h.GetDerivativeRule)
			rulesGroup.PUT("/:id", h.UpdateDerivativeRule)
			rulesGroup.DELETE("/:id", h.DeleteDerivativeRule)
			rulesGroup.POST("/refresh", h.RefreshRuleCache)
			rulesGroup.GET("/conflicts", h.CheckRuleConflicts)
		}
	}

	return &Server{
		engine:    r,
		cfg:       cfg,
		streamMgr: streamMgr,
	}
}

func (s *Server) Run(addr string) error {
	certFile := s.cfg.Server.CertFile
	keyFile := s.cfg.Server.KeyFile

	if certFile != "" && keyFile != "" {
		log.Printf("🔒 Starting HTTPS/WSS server on %s", addr)
		log.Printf("   Cert: %s", certFile)
		log.Printf("   Key:  %s", keyFile)
		return s.engine.RunTLS(addr, certFile, keyFile)
	}

	log.Printf("🔓 Starting HTTP/WS server on %s", addr)
	return s.engine.Run(addr)
}

// Shutdown 优雅关闭服务器
func (s *Server) Shutdown() {
	if s.streamMgr != nil {
		log.Println("Stopping market stream manager...")
		s.streamMgr.Stop()
	}
}

// =========================================================
// CORS 中间件实现
// =========================================================
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
