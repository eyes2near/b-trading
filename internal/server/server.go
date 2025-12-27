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
	streamMgr *market.StreamManager
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

	// CORS 中间件
	r.Use(CORSMiddleware())

	// 初始化通知服务
	notifier := notify.NewNotifier()

	// 初始化衍生订单引擎
	derivativeEngine := derivative.NewEngine(binanceClient, ruleRepo, notifier)
	derivativeEngine.StartAutoRefresh(context.Background(), 5*time.Minute)

	// =========================================================
	// 按依赖顺序初始化服务
	// =========================================================

	// 1. 审计服务（无依赖）
	auditService := service.NewAuditService(auditRepo, orderRepo)

	// 2. 成交处理器（依赖：orderRepo, fillRepo, auditService）
	fillProcessor := service.NewFillProcessor(orderRepo, fillRepo, auditService)

	// 3. 订单服务（依赖：fillProcessor, auditService, derivativeEngine）
	orderService := service.NewOrderService(
		cfg,
		binanceClient,
		orderRepo,
		fillRepo,
		ruleRepo,
		fillProcessor,
		auditService,
		notifier,
		derivativeEngine,
	)

	// 4. 流程服务（依赖：orderService, auditService）
	//    内部会调用 orderService.SetFlowCompletionChecker(flowService)
	flowService := service.NewFlowService(
		cfg,
		flowRepo,
		orderService,
		auditService,
		notifier,
		derivativeEngine,
	)

	// 5. Webhook 处理器
	webhookProcessor := service.NewWebhookProcessor(
		cfg,
		orderRepo,
		deliveryRepo,
		orderService,
		fillProcessor,
		flowService,
		auditService,
		derivativeEngine,
		notifier,
	)

	// 初始化 Handler
	h := api.NewHandler(flowRepo, orderRepo, cfg, binanceClient, flowService, ruleRepo, derivativeEngine)
	webhookHandler := api.NewWebhookHandler(webhookProcessor)

	// WebSocket 模块
	streamMgr := market.NewStreamManager(cfg.MarketStream)
	streamMgr.Run()

	// -------------------------------------------------------------------------
	// 路由注册
	// -------------------------------------------------------------------------

	// WebSocket 路由
	r.GET("/ws/market", gin.WrapF(api.MarketStreamHandler(streamMgr)))

	// 内部 Webhook 路由
	r.POST("/internal/webhook/binance", webhookHandler.HandleBinanceWebhook)

	// API 路由组
	apiGroup := r.Group("/api")
	{
		// 交易流程管理
		apiGroup.GET("/flows", h.GetActiveFlows)
		apiGroup.GET("/flows/:id", h.GetFlowDetail)
		apiGroup.POST("/flows", h.CreateFlow)
		apiGroup.POST("/flows/:id/cancel", h.CancelFlow)

		// 市场数据查询
		apiGroup.GET("/prices/spot/:symbol", h.GetSpotPrice)
		apiGroup.GET("/prices/coinm/:symbol", h.GetCoinMPrice)
		apiGroup.GET("/coinm/quarter-symbols/:base", h.GetQuarterSymbols)

		// 衍生订单规则管理
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
		return s.engine.RunTLS(addr, certFile, keyFile)
	}

	log.Printf("🔓 Starting HTTP/WS server on %s", addr)
	return s.engine.Run(addr)
}

func (s *Server) Shutdown() {
	if s.streamMgr != nil {
		log.Println("Stopping market stream manager...")
		s.streamMgr.Stop()
	}
}

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
