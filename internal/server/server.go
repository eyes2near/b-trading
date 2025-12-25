// internal/server/server.go

package server

import (
	"context"
	"embed"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/eyes2near/b-trading/internal/api"
	"github.com/eyes2near/b-trading/internal/binance"
	"github.com/eyes2near/b-trading/internal/config"
	"github.com/eyes2near/b-trading/internal/database"
	"github.com/eyes2near/b-trading/internal/derivative"
	"github.com/eyes2near/b-trading/internal/notify"
	"github.com/eyes2near/b-trading/internal/service"
	"github.com/gin-gonic/gin"
)

//go:embed web/templates/*
var contentFS embed.FS

type Server struct {
	engine *gin.Engine
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

	// 模板加载
	tmpl := template.Must(template.ParseFS(
		contentFS,
		"web/templates/*.html",
		"web/templates/partials/*.html",
	))

	log.Println("=========== TEMPLATE DEBUG ===========")
	for _, t := range tmpl.Templates() {
		log.Printf("Loaded template: [%s]", t.Name())
	}
	log.Println("======================================")

	r.SetHTMLTemplate(tmpl)

	// 初始化通知服务
	notifier := notify.NewNotifier()

	// 初始化衍生订单引擎
	derivativeEngine := derivative.NewEngine(binanceClient, ruleRepo, notifier)
	// 启动规则自动刷新（每5分钟）
	derivativeEngine.StartAutoRefresh(context.Background(), 5*time.Minute)

	// 初始化服务层
	flowService := service.NewFlowService(cfg, binanceClient, flowRepo, orderRepo, fillRepo, auditRepo)
	webhookProcessor := service.NewWebhookProcessor(
		cfg, binanceClient, flowRepo, orderRepo, fillRepo,
		deliveryRepo, auditRepo, flowService, derivativeEngine, ruleRepo, notifier,
	)

	// 初始化 Handler
	h := api.NewHandler(flowRepo, orderRepo, cfg, binanceClient, flowService, ruleRepo, derivativeEngine)
	webhookHandler := api.NewWebhookHandler(webhookProcessor)

	// 页面路由
	r.GET("/", h.RenderDashboard)

	// API 路由
	apiGroup := r.Group("/api")
	{
		apiGroup.POST("/flows", h.CreateFlow)
		apiGroup.GET("/flows", h.GetActiveFlows)
		apiGroup.GET("/flows/:id", h.GetFlowDetail)
		apiGroup.POST("/flows/:id/cancel", h.CancelFlow)

		// 价格查询
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

	// Webhook 接收端点（内部路径）
	r.POST("/internal/webhook/binance", webhookHandler.HandleBinanceWebhook)

	// 静态资源
	r.GET("/static/*filepath", func(c *gin.Context) {
		c.FileFromFS("web/static/"+c.Param("filepath"), http.FS(contentFS))
	})

	return &Server{engine: r}
}

func (s *Server) Run(addr string) error {
	return s.engine.Run(addr)
}
