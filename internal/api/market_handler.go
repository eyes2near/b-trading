// internal/api/market_handler.go
package api

import (
	"net/http"

	"github.com/eyes2near/b-trading/internal/market"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许所有来源，生产环境建议配置具体域名
	},
}

// MarketStreamHandler 返回处理 WebSocket 连接的 HTTP 处理函数
func MarketStreamHandler(sm *market.StreamManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, "Failed to upgrade connection", http.StatusBadRequest)
			return
		}

		// HandleClientConnection 会阻塞直到连接关闭
		sm.HandleClientConnection(conn)
	}
}
