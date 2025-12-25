// internal/service/notifier.go

package notify

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBarkKey = "bYQj8CHN5zJWimDzkmYZ33"

// Notifier 通知服务
type Notifier struct {
	client  *http.Client
	barkKey string
}

// NewNotifier 创建通知服务
func NewNotifier() *Notifier {
	return &Notifier{
		client:  &http.Client{Timeout: 10 * time.Second},
		barkKey: defaultBarkKey,
	}
}

// NewNotifierWithKey 使用指定 key 创建通知服务
func NewNotifierWithKey(barkKey string) *Notifier {
	return &Notifier{
		client:  &http.Client{Timeout: 10 * time.Second},
		barkKey: barkKey,
	}
}

// NotifyTrackJobFailed 通知 Track Job 创建失败
func (n *Notifier) NotifyTrackJobFailed(statusCode int) {
	title := fmt.Sprintf("追踪订单失败code=%d", statusCode)
	n.sendSimple(title)
}

// NotifyRuleConflict 通知规则冲突
func (n *Notifier) NotifyRuleConflict(conflicts []RuleConflict) {
	title := fmt.Sprintf("衍生订单规则冲突(%d条)", len(conflicts))

	var bodyParts []string
	for _, c := range conflicts {
		bodyParts = append(bodyParts, fmt.Sprintf("[%s] vs [%s]: %s",
			c.Rule1Name, c.Rule2Name, c.ConflictType))
	}
	body := strings.Join(bodyParts, "\n")

	n.send(title, body)
}

// NotifyDerivativeFailure 通知衍生订单失败（统一入口）
func (n *Notifier) NotifyDerivativeFailure(primaryOrderID uint, symbol string, ruleName string, stage string, errMsg string) {
	title := "衍生订单失败"
	body := fmt.Sprintf("主订单ID=%d\nSymbol=%s\n规则=%s\n阶段=%s\n错误=%s",
		primaryOrderID, symbol, ruleName, stage, errMsg)
	n.send(title, body)
}

// NotifyNoMatchingRule 通知主订单未匹配到衍生规则
func (n *Notifier) NotifyNoMatchingRule(orderID uint, market, symbol string) {
	title := "主订单未匹配衍生规则"
	body := fmt.Sprintf("订单ID=%d\n市场=%s\nSymbol=%s\n请检查规则配置", orderID, market, symbol)
	n.send(title, body)
}

// NotifyPrimaryOrderFailed 通知主订单创建/提交失败
func (n *Notifier) NotifyPrimaryOrderFailed(flowUUID string, symbol string, errMsg string) {
	title := "主订单创建失败"
	body := fmt.Sprintf("流程=%s\nSymbol=%s\n错误=%s", flowUUID, symbol, errMsg)
	n.send(title, body)
}

// RuleConflict 规则冲突信息
type RuleConflict struct {
	Rule1ID      uint
	Rule1Name    string
	Rule2ID      uint
	Rule2Name    string
	ConflictType string
	Description  string
}

// sendSimple 发送简单通知（只有标题）
func (n *Notifier) sendSimple(title string) {
	encodedTitle := url.QueryEscape(title)
	notifyURL := fmt.Sprintf("https://api.day.app/%s/%s", n.barkKey, encodedTitle)
	n.doSend(notifyURL)
}

// send 发送通知（标题+内容）
func (n *Notifier) send(title, body string) {
	encodedTitle := url.QueryEscape(title)
	encodedBody := url.QueryEscape(body)
	notifyURL := fmt.Sprintf("https://api.day.app/%s/%s/%s", n.barkKey, encodedTitle, encodedBody)
	n.doSend(notifyURL)
}

// doSend 执行发送
func (n *Notifier) doSend(notifyURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, notifyURL, nil)
	if err != nil {
		log.Printf("Failed to create notification request: %v", err)
		return
	}

	resp, err := n.client.Do(req)
	if err != nil {
		log.Printf("Failed to send notification: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Notification returned non-200 status: %d", resp.StatusCode)
	} else {
		log.Printf("Notification sent successfully")
	}
}
