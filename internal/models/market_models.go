// internal/models/market_models.go
package models

import "encoding/json"

// ================= Broker 原始数据 =================

// BandSnapshot 对应 Publisher 发送的 snapshot 内层结构
type BandSnapshot struct {
	Symbol   string     `json:"symbol"`
	UpdateID int64      `json:"updateId"`
	MidPrice float64    `json:"midPrice"`
	BandPct  float64    `json:"bandPct"`
	Low      float64    `json:"low"`
	High     float64    `json:"high"`
	Bids     [][]string `json:"bids"`
	Asks     [][]string `json:"asks"`
}

// PublishedBandEvent 对应 Publisher 发送到 Broker 的完整结构
type PublishedBandEvent struct {
	Snapshot    BandSnapshot `json:"snapshot"`
	PublishedAt int64        `json:"publishedAt"`
	Source      string       `json:"source"`
}

// BrokerMessage 用于解析 Broker 推送的外层消息
type BrokerMessage struct {
	Type  string          `json:"type"`
	Topic string          `json:"topic"`
	Event json.RawMessage `json:"event"`
}

// ================= Publisher API =================

// PublisherRequest 发送给 Publisher 的请求
type PublisherRequest struct {
	Symbol        string `json:"symbol"`
	Market        string `json:"market"`
	ExpectedTopic string `json:"expected_topic,omitempty"` // 续约时带上期望的 topic
}

// 响应增加状态
type PublisherResponse struct {
	Status        string `json:"status"` // "started" | "renewed" | "topic_changed"
	Symbol        string `json:"symbol,omitempty"`
	Topic         string `json:"topic,omitempty"`
	PreviousTopic string `json:"previous_topic,omitempty"` // 如果 topic 变化，返回旧 topic
	Error         string `json:"error,omitempty"`
}

// ================= 前端交互协议 =================

// ClientAction 上行: 客户端 -> 服务端
type ClientAction struct {
	Action   string `json:"action"`   // "subscribe" | "unsubscribe" | "ping"
	Stream   string `json:"stream"`   // "depth"
	Symbol   string `json:"symbol"`   // spot: "BTCUSDT", coin-m: "BTC"
	Market   string `json:"market"`   // "spot" | "coin-m" | "coinm"
	Interval int64  `json:"interval"` // 限流间隔（毫秒），0 表示不限流
}

// DepthUpdateMsg 下行: 服务端 -> 客户端
// 严格按照文档格式，不包含 market 字段
type DepthUpdateMsg struct {
	Type   string       `json:"type"`   // "depthUpdate"
	Symbol string       `json:"symbol"` // e.g. "BTCUSDT"
	Ts     int64        `json:"ts"`     // 时间戳（毫秒）
	Asks   []OrderLevel `json:"asks"`
	Bids   []OrderLevel `json:"bids"`
}

// OrderLevel 单个价格档位
type OrderLevel struct {
	Price    string  `json:"price"`
	Quantity string  `json:"quantity"`
	Total    float64 `json:"total"`
}

// PongMsg Pong 响应
type PongMsg struct {
	Type string `json:"type"` // "pong"
	Ts   int64  `json:"ts"`
}

// ErrorMsg 错误消息
type ErrorMsg struct {
	Type    string `json:"type"` // "error"
	Code    int    `json:"code"`
	Message string `json:"message"`
}
