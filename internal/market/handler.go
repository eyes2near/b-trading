// internal/market/handler.go
package market

import (
	"encoding/json"
	"log"
	"time"

	"github.com/eyes2near/b-trading/internal/models"
	"github.com/gorilla/websocket"
)

// HandleClientConnection 处理单个客户端 WebSocket 连接
func (sm *StreamManager) HandleClientConnection(conn *websocket.Conn) {
	client := NewClient(sm, conn, 256)

	// 启动写入协程
	go sm.clientWritePump(client)

	// 读取协程（阻塞）
	sm.clientReadPump(client)

	// 清理
	sm.RemoveClient(client)
	client.Close()
}

func (sm *StreamManager) clientWritePump(c *Client) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				log.Printf("[Market] Write to client failed: %v", err)
				return
			}

		case <-ticker.C:
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (sm *StreamManager) clientReadPump(c *Client) {
	c.conn.SetReadLimit(4096)
	c.conn.SetPongHandler(func(string) error {
		return nil
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("[Market] Client read error: %v", err)
			}
			return
		}

		var action models.ClientAction
		if err := json.Unmarshal(data, &action); err != nil {
			sm.sendError(c, 4000, "Invalid message format")
			continue
		}

		sm.handleClientAction(c, action)
	}
}

func (sm *StreamManager) handleClientAction(c *Client, action models.ClientAction) {
	switch action.Action {
	case "subscribe":
		if action.Stream != "depth" {
			sm.sendError(c, 4001, "Unsupported stream type: "+action.Stream)
			return
		}

		// 设置客户端限流
		if action.Interval > 0 {
			c.SetMinInterval(time.Duration(action.Interval) * time.Millisecond)
		}

		err := sm.Subscribe(c, action.Symbol, action.Market)
		if err != nil {
			if subErr, ok := err.(*SubscribeError); ok {
				sm.sendError(c, subErr.Code, subErr.Message)
			} else {
				sm.sendError(c, 5000, err.Error())
			}
			return
		}

		// 文档中没有要求订阅成功通知，这里不发送

	case "unsubscribe":
		sm.Unsubscribe(c, action.Symbol, action.Market)
		// 文档中没有要求取消订阅通知，这里不发送

	case "ping":
		sm.sendPong(c)

	default:
		sm.sendError(c, 4000, "Unknown action: "+action.Action)
	}
}

func (sm *StreamManager) sendError(c *Client, code int, message string) {
	msg := models.ErrorMsg{
		Type:    "error",
		Code:    code,
		Message: message,
	}
	data, _ := json.Marshal(msg)
	select {
	case c.send <- data:
	default:
	}
}

func (sm *StreamManager) sendPong(c *Client) {
	msg := models.PongMsg{
		Type: "pong",
		Ts:   time.Now().UnixMilli(),
	}
	data, _ := json.Marshal(msg)
	select {
	case c.send <- data:
	default:
	}
}
