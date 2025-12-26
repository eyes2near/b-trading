// internal/market/subscription.go
package market

import (
	"sync"
	"time"
)

// Subscription 表示一个订阅关系
type Subscription struct {
	OriginalSymbol string // 前端传入的 symbol: "BTC" 或 "BTCUSDT"
	ResolvedSymbol string // Publisher 返回的实际 symbol: "BTCUSD_250627" 或 "BTCUSDT"
	Market         string // "spot" | "coin-m"
	Topic          string // Broker topic: "orderbook.band.BTCUSD_250627"
}

// subscriptionKey 生成订阅唯一标识
func subscriptionKey(symbol, market string) string {
	return market + ":" + symbol
}

// WebSocketConn WebSocket 连接接口（便于测试）
type WebSocketConn interface {
	WriteMessage(messageType int, data []byte) error
	ReadMessage() (messageType int, p []byte, err error)
	Close() error
	SetReadLimit(limit int64)
	SetPongHandler(h func(appData string) error)
}

// ClientSubscription 客户端当前的订阅信息
type ClientSubscription struct {
	Symbol string
	Market string
	Key    string // subscriptionKey
}

// Client 代表一个前端 WebSocket 连接
type Client struct {
	mgr  *StreamManager
	conn WebSocketConn
	send chan []byte

	mu          sync.Mutex
	minInterval time.Duration
	lastSent    time.Time
	closed      bool

	// 当前活跃的订阅（每个连接只能有一个）
	currentSub *ClientSubscription
}

// NewClient 创建新的客户端
func NewClient(mgr *StreamManager, conn WebSocketConn, sendBufferSize int) *Client {
	return &Client{
		mgr:  mgr,
		conn: conn,
		send: make(chan []byte, sendBufferSize),
	}
}

// SetMinInterval 设置最小发送间隔
func (c *Client) SetMinInterval(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.minInterval = d
}

// GetCurrentSubscription 获取当前订阅信息
func (c *Client) GetCurrentSubscription() *ClientSubscription {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.currentSub
}

// SetCurrentSubscription 设置当前订阅信息
func (c *Client) SetCurrentSubscription(sub *ClientSubscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentSub = sub
}

// ClearCurrentSubscription 清除当前订阅信息
func (c *Client) ClearCurrentSubscription() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentSub = nil
}

// Send 返回发送通道
func (c *Client) Send() chan []byte {
	return c.send
}

// Conn 返回底层连接
func (c *Client) Conn() WebSocketConn {
	return c.conn
}

// Close 关闭客户端
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	close(c.send)
	c.conn.Close()
}

// shouldSend 检查是否应该发送（限流）
func (c *Client) shouldSend(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.minInterval <= 0 {
		return true
	}
	if now.Sub(c.lastSent) >= c.minInterval {
		c.lastSent = now
		return true
	}
	return false
}
