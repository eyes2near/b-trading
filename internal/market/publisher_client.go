// internal/market/publisher_client.go
package market

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/eyes2near/b-trading/internal/models"
)

// PublisherClient 与 Publisher 服务通信的客户端
type PublisherClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewPublisherClient 创建 Publisher 客户端
func NewPublisherClient(baseURL string) *PublisherClient {
	return &PublisherClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// StartStream 启动一个新流（不带 expected_topic）
func (pc *PublisherClient) StartStream(ctx context.Context, symbol, market string) (*models.PublisherResponse, error) {
	return pc.doRequest(ctx, http.MethodPost, symbol, market, "")
}

// RenewStream 续约流（带期望的 topic）
func (pc *PublisherClient) RenewStream(ctx context.Context, symbol, market, expectedTopic string) (*models.PublisherResponse, error) {
	return pc.doRequest(ctx, http.MethodPost, symbol, market, expectedTopic)
}

// StopStream 停止一个流
func (pc *PublisherClient) StopStream(ctx context.Context, symbol, market string) (*models.PublisherResponse, error) {
	return pc.doRequest(ctx, http.MethodDelete, symbol, market, "")
}

func (pc *PublisherClient) doRequest(ctx context.Context, method, symbol, market, expectedTopic string) (*models.PublisherResponse, error) {
	reqBody := models.PublisherRequest{
		Symbol:        symbol,
		Market:        market,
		ExpectedTopic: expectedTopic,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := pc.baseURL + "/v1/streams"
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := pc.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var result models.PublisherResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	// 根据 HTTP 状态码返回对应的错误类型
	switch resp.StatusCode {
	case http.StatusOK:
		return &result, nil

	case http.StatusConflict: // 409 - topic mismatch
		return nil, &TopicMismatchError{
			Expected: expectedTopic,
			Actual:   result.Topic,
		}

	case http.StatusNotFound: // 404 - stream not found
		return nil, &StreamNotFoundError{
			ExpectedTopic: expectedTopic,
		}

	default:
		if result.Error != "" {
			return nil, fmt.Errorf("publisher error: %s", result.Error)
		}
		return nil, fmt.Errorf("publisher returned status %d", resp.StatusCode)
	}
}

// TopicMismatchError topic 不匹配错误（请求到了错误的 Publisher 实例）
type TopicMismatchError struct {
	Expected string
	Actual   string
}

func (e *TopicMismatchError) Error() string {
	return fmt.Sprintf("topic mismatch: expected %s, got %s", e.Expected, e.Actual)
}

// StreamNotFoundError 流不存在错误（Publisher 重启过或流已过期）
type StreamNotFoundError struct {
	ExpectedTopic string
}

func (e *StreamNotFoundError) Error() string {
	return fmt.Sprintf("stream not found for topic: %s", e.ExpectedTopic)
}

// IsTopicMismatchError 判断是否是 topic 不匹配错误
func IsTopicMismatchError(err error) bool {
	_, ok := err.(*TopicMismatchError)
	return ok
}

// IsStreamNotFoundError 判断是否是流不存在错误
func IsStreamNotFoundError(err error) bool {
	_, ok := err.(*StreamNotFoundError)
	return ok
}

// NeedsResubscribe 判断错误是否需要重新订阅
func NeedsResubscribe(err error) bool {
	return IsTopicMismatchError(err) || IsStreamNotFoundError(err)
}
