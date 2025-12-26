// internal/market/manager.go (b-trading 项目)
package market

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/eyes2near/b-trading/internal/config"
	"github.com/eyes2near/b-trading/internal/models"
	"github.com/gorilla/websocket"
)

// StreamManager 管理市场数据流
type StreamManager struct {
	cfg             config.MarketStreamConfig
	transformCfg    TransformConfig
	publisherClient *PublisherClient

	// Broker 连接
	brokerMu   sync.Mutex
	brokerConn *websocket.Conn

	// 订阅管理
	subsMu        sync.RWMutex
	subscriptions map[string]*Subscription // key -> Subscription
	topicToKey    map[string]string        // topic -> key
	clients       map[string]map[*Client]struct{}

	// 生命周期
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewStreamManager 创建流管理器
func NewStreamManager(cfg config.MarketStreamConfig) *StreamManager {
	ctx, cancel := context.WithCancel(context.Background())

	if cfg.ReconnectDelay == 0 {
		cfg.ReconnectDelay = time.Second
	}
	if cfg.MaxReconnect == 0 {
		cfg.MaxReconnect = 30 * time.Second
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.DefaultLevels == 0 {
		cfg.DefaultLevels = 5
	}

	transformCfg := TransformConfig{
		MaxLevels: cfg.DefaultLevels,
	}

	return &StreamManager{
		cfg:             cfg,
		transformCfg:    transformCfg,
		publisherClient: NewPublisherClient(cfg.PublisherAPI),
		subscriptions:   make(map[string]*Subscription),
		topicToKey:      make(map[string]string),
		clients:         make(map[string]map[*Client]struct{}),
		ctx:             ctx,
		cancel:          cancel,
	}
}

// SetTransformConfig 设置转换配置
func (sm *StreamManager) SetTransformConfig(cfg TransformConfig) {
	sm.transformCfg = cfg
}

// Run 启动管理器
func (sm *StreamManager) Run() {
	sm.wg.Add(2)
	go sm.maintainBrokerConnection()
	go sm.keepAliveLoop()
}

// Stop 停止管理器
func (sm *StreamManager) Stop() {
	sm.cancel()
	sm.wg.Wait()

	sm.brokerMu.Lock()
	if sm.brokerConn != nil {
		sm.brokerConn.Close()
	}
	sm.brokerMu.Unlock()
}

// Subscribe 订阅（每个客户端只能有一个活跃订阅）
func (sm *StreamManager) Subscribe(c *Client, symbol, market string) error {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	market = normalizeMarket(market)

	if symbol == "" {
		return &SubscribeError{Code: 4001, Message: "symbol is required"}
	}
	if market != "spot" && market != "coin-m" {
		return &SubscribeError{Code: 4001, Message: "market must be 'spot' or 'coin-m'"}
	}

	newKey := subscriptionKey(symbol, market)

	// 检查客户端是否已有订阅
	currentSub := c.GetCurrentSubscription()
	if currentSub != nil {
		if currentSub.Key == newKey {
			log.Printf("[Market] Client already subscribed to %s, ignoring duplicate", newKey)
			return nil
		}
		log.Printf("[Market] Client switching subscription from %s to %s", currentSub.Key, newKey)
		sm.unsubscribeInternal(c, currentSub.Symbol, currentSub.Market)
	}

	return sm.subscribeInternal(c, symbol, market, newKey)
}

// subscribeInternal 内部订阅逻辑
func (sm *StreamManager) subscribeInternal(c *Client, symbol, market, key string) error {
	sm.subsMu.Lock()

	sub, exists := sm.subscriptions[key]
	if exists {
		if sm.clients[key] == nil {
			sm.clients[key] = make(map[*Client]struct{})
		}
		sm.clients[key][c] = struct{}{}
		sm.subsMu.Unlock()

		c.SetCurrentSubscription(&ClientSubscription{
			Symbol: symbol,
			Market: market,
			Key:    key,
		})

		log.Printf("[Market] Client added to existing subscription: %s", key)
		return nil
	}

	sm.subsMu.Unlock()

	ctx, cancel := context.WithTimeout(sm.ctx, 10*time.Second)
	defer cancel()

	resp, err := sm.publisherClient.StartStream(ctx, symbol, market)
	if err != nil {
		log.Printf("[Market] Publisher StartStream failed: %v", err)
		return &SubscribeError{Code: 5001, Message: "failed to start stream: " + err.Error()}
	}

	sub = &Subscription{
		OriginalSymbol: symbol,
		ResolvedSymbol: resp.Symbol,
		Market:         market,
		Topic:          resp.Topic,
	}

	sm.subsMu.Lock()
	defer sm.subsMu.Unlock()

	if existingSub, exists := sm.subscriptions[key]; exists {
		if sm.clients[key] == nil {
			sm.clients[key] = make(map[*Client]struct{})
		}
		sm.clients[key][c] = struct{}{}

		c.SetCurrentSubscription(&ClientSubscription{
			Symbol: symbol,
			Market: market,
			Key:    key,
		})

		_ = existingSub
		return nil
	}

	// 检查是否有相同 topic 的订阅
	if existingKey, exists := sm.topicToKey[sub.Topic]; exists && existingKey != key {
		if sm.clients[existingKey] == nil {
			sm.clients[existingKey] = make(map[*Client]struct{})
		}
		sm.clients[existingKey][c] = struct{}{}

		existingSub := sm.subscriptions[existingKey]
		c.SetCurrentSubscription(&ClientSubscription{
			Symbol: existingSub.OriginalSymbol,
			Market: existingSub.Market,
			Key:    existingKey,
		})

		log.Printf("[Market] Client added to existing subscription with same topic: %s -> %s", key, existingKey)
		return nil
	}

	sm.subscriptions[key] = sub
	sm.topicToKey[sub.Topic] = key
	if sm.clients[key] == nil {
		sm.clients[key] = make(map[*Client]struct{})
	}
	sm.clients[key][c] = struct{}{}

	c.SetCurrentSubscription(&ClientSubscription{
		Symbol: symbol,
		Market: market,
		Key:    key,
	})

	sm.sendToBroker("subscribe", sub.Topic)

	log.Printf("[Market] New subscription created: %s -> %s (topic: %s)", key, sub.ResolvedSymbol, sub.Topic)
	return nil
}

// Unsubscribe 取消订阅
func (sm *StreamManager) Unsubscribe(c *Client, symbol, market string) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	market = normalizeMarket(market)

	currentSub := c.GetCurrentSubscription()
	if currentSub == nil {
		log.Printf("[Market] Client has no active subscription to unsubscribe")
		return
	}

	key := subscriptionKey(symbol, market)
	if currentSub.Key != key {
		log.Printf("[Market] Client tried to unsubscribe %s but current subscription is %s", key, currentSub.Key)
		return
	}

	sm.unsubscribeInternal(c, symbol, market)
}

// unsubscribeInternal 内部取消订阅逻辑
func (sm *StreamManager) unsubscribeInternal(c *Client, symbol, market string) {
	key := subscriptionKey(symbol, market)

	sm.subsMu.Lock()

	clientSet, exists := sm.clients[key]
	if !exists {
		sm.subsMu.Unlock()
		c.ClearCurrentSubscription()
		return
	}

	delete(clientSet, c)

	if len(clientSet) == 0 {
		sub := sm.subscriptions[key]
		if sub != nil {
			sm.sendToBroker("unsubscribe", sub.Topic)
			delete(sm.topicToKey, sub.Topic)
			go sm.stopPublisherStream(symbol, market)
		}
		delete(sm.subscriptions, key)
		delete(sm.clients, key)
		log.Printf("[Market] Subscription removed: %s", key)
	}

	sm.subsMu.Unlock()

	c.ClearCurrentSubscription()
}

// RemoveClient 移除客户端的所有订阅
func (sm *StreamManager) RemoveClient(c *Client) {
	currentSub := c.GetCurrentSubscription()
	if currentSub != nil {
		sm.unsubscribeInternal(c, currentSub.Symbol, currentSub.Market)
	}
}

func (sm *StreamManager) stopPublisherStream(symbol, market string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := sm.publisherClient.StopStream(ctx, symbol, market)
	if err != nil {
		log.Printf("[Market] Publisher StopStream failed: %v", err)
	}
}

func (sm *StreamManager) keepAliveLoop() {
	defer sm.wg.Done()

	ticker := time.NewTicker(sm.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sm.ctx.Done():
			return
		case <-ticker.C:
			sm.renewSubscriptions()
		}
	}
}

// renewSubscriptions 续约订阅，并处理 topic 变化
func (sm *StreamManager) renewSubscriptions() {
	sm.subsMu.RLock()
	subs := make([]*Subscription, 0, len(sm.subscriptions))
	for _, sub := range sm.subscriptions {
		subs = append(subs, sub)
	}
	sm.subsMu.RUnlock()

	for _, sub := range subs {
		go func(s *Subscription) {
			ctx, cancel := context.WithTimeout(sm.ctx, 5*time.Second)
			defer cancel()

			// 续约时带上期望的 topic
			resp, err := sm.publisherClient.RenewStream(ctx, s.OriginalSymbol, s.Market, s.Topic)
			if err != nil {
				// 检查是否需要重新订阅
				if NeedsResubscribe(err) {
					log.Printf("[Market] Stream %s needs re-subscribe: %v", s.OriginalSymbol, err)
					sm.handleResubscribe(s)
					return
				}
				log.Printf("[Market] Renew subscription failed for %s: %v", s.OriginalSymbol, err)
				return
			}

			// 检查 topic 是否变化（正常情况下不应该变化，但以防万一）
			if resp.Topic != s.Topic {
				log.Printf("[Market] Unexpected topic change for %s: %s -> %s", s.OriginalSymbol, s.Topic, resp.Topic)
				sm.handleTopicChange(s, resp.Topic, resp.Symbol)
			}
		}(sub)
	}
}

// handleResubscribe 处理需要重新订阅的情况
func (sm *StreamManager) handleResubscribe(oldSub *Subscription) {
	key := subscriptionKey(oldSub.OriginalSymbol, oldSub.Market)

	sm.subsMu.Lock()

	// 获取当前订阅该流的所有客户端
	clientSet := sm.clients[key]
	if len(clientSet) == 0 {
		sm.subsMu.Unlock()
		return
	}

	clients := make([]*Client, 0, len(clientSet))
	for c := range clientSet {
		clients = append(clients, c)
	}

	// 清理旧订阅
	sm.sendToBroker("unsubscribe", oldSub.Topic)
	delete(sm.topicToKey, oldSub.Topic)
	delete(sm.subscriptions, key)
	delete(sm.clients, key)

	sm.subsMu.Unlock()

	// 重新订阅
	ctx, cancel := context.WithTimeout(sm.ctx, 10*time.Second)
	defer cancel()

	resp, err := sm.publisherClient.StartStream(ctx, oldSub.OriginalSymbol, oldSub.Market)
	if err != nil {
		log.Printf("[Market] Re-subscribe failed for %s: %v", oldSub.OriginalSymbol, err)
		// 通知所有客户端订阅失败
		for _, c := range clients {
			c.ClearCurrentSubscription()
		}
		return
	}

	// 创建新订阅
	newSub := &Subscription{
		OriginalSymbol: oldSub.OriginalSymbol,
		ResolvedSymbol: resp.Symbol,
		Market:         oldSub.Market,
		Topic:          resp.Topic,
	}

	sm.subsMu.Lock()
	sm.subscriptions[key] = newSub
	sm.topicToKey[resp.Topic] = key
	sm.clients[key] = make(map[*Client]struct{})
	for _, c := range clients {
		sm.clients[key][c] = struct{}{}
		c.SetCurrentSubscription(&ClientSubscription{
			Symbol: oldSub.OriginalSymbol,
			Market: oldSub.Market,
			Key:    key,
		})
	}
	sm.subsMu.Unlock()

	sm.sendToBroker("subscribe", resp.Topic)

	log.Printf("[Market] Re-subscribed %s with new topic: %s", key, resp.Topic)
}

// handleTopicChange 处理 topic 变化
func (sm *StreamManager) handleTopicChange(oldSub *Subscription, newTopic, newSymbol string) {
	key := subscriptionKey(oldSub.OriginalSymbol, oldSub.Market)

	sm.subsMu.Lock()
	defer sm.subsMu.Unlock()

	// 检查订阅是否还存在
	currentSub, exists := sm.subscriptions[key]
	if !exists {
		return
	}

	// 确保是同一个订阅
	if currentSub.Topic != oldSub.Topic {
		return
	}

	oldTopic := currentSub.Topic

	// 取消订阅旧 topic
	sm.sendToBroker("unsubscribe", oldTopic)
	delete(sm.topicToKey, oldTopic)

	// 更新订阅信息
	currentSub.Topic = newTopic
	currentSub.ResolvedSymbol = newSymbol
	sm.topicToKey[newTopic] = key

	// 订阅新 topic
	sm.sendToBroker("subscribe", newTopic)

	log.Printf("[Market] Subscription topic updated: %s (%s -> %s)", key, oldTopic, newTopic)
}

func (sm *StreamManager) maintainBrokerConnection() {
	defer sm.wg.Done()

	backoff := sm.cfg.ReconnectDelay
	maxBackoff := sm.cfg.MaxReconnect

	for {
		select {
		case <-sm.ctx.Done():
			return
		default:
		}

		log.Printf("[Market] Connecting to broker: %s", sm.cfg.BrokerURL)
		conn, _, err := websocket.DefaultDialer.Dial(sm.cfg.BrokerURL, nil)
		if err != nil {
			log.Printf("[Market] Dial broker failed: %v, retry in %v", err, backoff)
			sm.sleep(backoff)
			backoff = sm.incBackoff(backoff, maxBackoff)
			continue
		}

		backoff = sm.cfg.ReconnectDelay

		sm.brokerMu.Lock()
		sm.brokerConn = conn
		sm.brokerMu.Unlock()

		sm.resubscribeAll()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				log.Printf("[Market] Broker read error: %v", err)
				break
			}
			sm.handleBrokerMessage(data)
		}

		sm.brokerMu.Lock()
		sm.brokerConn = nil
		conn.Close()
		sm.brokerMu.Unlock()

		log.Printf("[Market] Broker disconnected, reconnecting...")
	}
}

func (sm *StreamManager) resubscribeAll() {
	sm.subsMu.RLock()
	topics := make([]string, 0, len(sm.topicToKey))
	for topic := range sm.topicToKey {
		topics = append(topics, topic)
	}
	sm.subsMu.RUnlock()

	for _, topic := range topics {
		sm.sendToBroker("subscribe", topic)
	}

	if len(topics) > 0 {
		log.Printf("[Market] Resubscribed %d topics", len(topics))
	}
}

func (sm *StreamManager) sendToBroker(action, topic string) {
	sm.brokerMu.Lock()
	defer sm.brokerMu.Unlock()

	if sm.brokerConn == nil {
		return
	}

	msg := map[string]string{"type": action, "topic": topic}
	if err := sm.brokerConn.WriteJSON(msg); err != nil {
		log.Printf("[Market] Send to broker failed: %v", err)
	}
}

func (sm *StreamManager) handleBrokerMessage(data []byte) {
	var msg models.BrokerMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[Market] Unmarshal broker message failed: %v", err)
		return
	}

	if msg.Type != "event" {
		return
	}

	sm.subsMu.RLock()
	key, exists := sm.topicToKey[msg.Topic]
	if !exists {
		sm.subsMu.RUnlock()
		return
	}

	sub := sm.subscriptions[key]
	clientSet := sm.clients[key]
	if sub == nil || len(clientSet) == 0 {
		sm.subsMu.RUnlock()
		return
	}

	clients := make([]*Client, 0, len(clientSet))
	for c := range clientSet {
		clients = append(clients, c)
	}
	sm.subsMu.RUnlock()

	var event models.PublishedBandEvent
	if err := json.Unmarshal(msg.Event, &event); err != nil {
		log.Printf("[Market] Unmarshal event failed: %v", err)
		return
	}

	update := Transform(event, sm.transformCfg)
	payload, err := json.Marshal(update)
	if err != nil {
		log.Printf("[Market] Marshal update failed: %v", err)
		return
	}

	now := time.Now()
	for _, c := range clients {
		if c.shouldSend(now) {
			select {
			case c.send <- payload:
			default:
			}
		}
	}
}

func (sm *StreamManager) sleep(d time.Duration) {
	select {
	case <-time.After(d):
	case <-sm.ctx.Done():
	}
}

func (sm *StreamManager) incBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next > max {
		return max
	}
	return next
}

func normalizeMarket(market string) string {
	market = strings.ToLower(strings.TrimSpace(market))
	if market == "coinm" {
		return "coin-m"
	}
	return market
}

// SubscribeError 订阅错误
type SubscribeError struct {
	Code    int
	Message string
}

func (e *SubscribeError) Error() string {
	return e.Message
}
