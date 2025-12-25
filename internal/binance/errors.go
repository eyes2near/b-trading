// internal/binance/errors.go

package binance

import "fmt"

// ClientError API 客户端错误
type ClientError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *ClientError) Error() string {
	return fmt.Sprintf("binance API error [%d] %s: %s", e.StatusCode, e.Code, e.Message)
}

// IsNotFound 检查是否为 404 错误
func (e *ClientError) IsNotFound() bool {
	return e.StatusCode == 404
}

// IsUnauthorized 检查是否为 401 错误
func (e *ClientError) IsUnauthorized() bool {
	return e.StatusCode == 401
}

// IsBinanceError 检查是否为 Binance 上游错误 (502)
func (e *ClientError) IsBinanceError() bool {
	return e.StatusCode == 502
}
