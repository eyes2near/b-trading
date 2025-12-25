// internal/api/webhook_handler.go

package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/eyes2near/b-trading/internal/binance"
	"github.com/eyes2near/b-trading/internal/service"
	"github.com/gin-gonic/gin"
)

type WebhookHandler struct {
	processor service.WebhookProcessor
}

func NewWebhookHandler(processor service.WebhookProcessor) *WebhookHandler {
	return &WebhookHandler{processor: processor}
}

// HandleBinanceWebhook 处理 Binance REST API 服务的 Webhook 回调
func (h *WebhookHandler) HandleBinanceWebhook(c *gin.Context) {
	// 1. 读取原始 body
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		log.Printf("Webhook: Failed to read body: %v", err)
		c.Status(http.StatusBadRequest)
		return
	}

	// 2. 获取 Header
	deliveryID := c.GetHeader("X-Webhook-Delivery-ID")
	jobID := c.GetHeader("X-Webhook-Job-ID")
	timestamp := c.GetHeader("X-Webhook-Timestamp")
	signature := c.GetHeader("X-Webhook-Signature")

	if deliveryID == "" {
		log.Printf("Webhook: Missing X-Webhook-Delivery-ID header")
		c.Status(http.StatusBadRequest)
		return
	}

	log.Printf("Webhook received: delivery_id=%s, job_id=%s, size=%d bytes", deliveryID, jobID, len(body))

	// 3. 检查重复投递（幂等）
	isDuplicate, err := h.processor.CheckDuplicate(c.Request.Context(), deliveryID)
	if err != nil {
		log.Printf("Webhook: Failed to check duplicate: %v", err)
		// 继续处理，避免因检查失败导致丢失事件
	}
	if isDuplicate {
		log.Printf("Webhook: Duplicate delivery ignored: %s", deliveryID)
		c.Status(http.StatusOK) // 返回 200 避免重试
		return
	}

	// 4. 解析事件
	var event binance.WebhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		log.Printf("Webhook: Failed to parse event: %v, body: %s", err, string(body))
		c.Status(http.StatusBadRequest)
		return
	}

	log.Printf("Webhook event parsed: type=%s, order_id=%d, symbol=%s, market=%s",
		event.EventType, event.OrderID, event.Symbol, event.Market)

	// 5. 签名验证（如果有）
	// 注意：实际应该根据订单查找对应的 webhook_secret 进行验证
	if signature != "" && timestamp != "" {
		// 这里需要从数据库获取订单的 webhook_secret
		// 暂时使用简化验证，实际生产需要完善
		log.Printf("Webhook: Signature present, validation skipped (TODO: implement full validation)")
	}

	// 6. 快速返回 200，异步处理业务逻辑
	// 为了确保不丢失事件，我们先落库再返回
	// 但处理失败不影响返回 200（已落库可以后续重试处理）

	// 处理事件
	if err := h.processor.ProcessEvent(c.Request.Context(), deliveryID, jobID, &event, body); err != nil {
		log.Printf("Webhook: Failed to process event: %v", err)
		// 仍然返回 200，因为我们已经记录了事件，可以后续处理
	}

	c.Status(http.StatusOK)
}
