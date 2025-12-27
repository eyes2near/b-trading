package binance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/eyes2near/b-trading/internal/config"
)

// Client defines the interface for Binance REST API operations
type Client interface {
	// Health check
	Health(ctx context.Context) (*HealthResponse, error)

	// Spot endpoints
	SpotCreateOrder(ctx context.Context, req SpotOrderRequest) (*OrderInfo, error)
	SpotCancelOrder(ctx context.Context, symbol string, orderID int64) (*OrderInfo, error)
	SpotGetOrder(ctx context.Context, symbol string, orderID int64) (*OrderInfo, error)
	SpotGetPrice(ctx context.Context, symbol string) (*PriceResponse, error)
	SpotTrackWebhook(ctx context.Context, req TrackWebhookRequest) (*TrackWebhookResponse, error)

	// Coin-M endpoints
	CoinMCreateOrder(ctx context.Context, req CoinMOrderRequest) (*OrderInfo, error)
	CoinMCancelOrder(ctx context.Context, symbol string, orderID int64) (*OrderInfo, error)
	CoinMGetOrder(ctx context.Context, symbol string, orderID int64) (*OrderInfo, error)
	CoinMGetPrice(ctx context.Context, symbol string) (*PriceResponse, error)
	CoinMGetQuarterSymbols(ctx context.Context, base string) (*QuarterSymbolsResponse, error)
	CoinMTrackWebhook(ctx context.Context, req TrackWebhookRequest) (*TrackWebhookResponse, error)

	// Track job management
	GetTrackJob(ctx context.Context, jobID string) (*TrackJobInfo, error)
	CancelTrackJob(ctx context.Context, jobID string) error
}

type client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

// NewClient creates a new Binance REST API client
func NewClient(cfg *config.BinanceRESTConfig) Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &client{
		baseURL:  cfg.BaseURL,
		apiToken: cfg.APIToken,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// doRequest executes an HTTP request and returns the response body
func (c *client) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	url := c.baseURL + path

	var reqBody io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if c.apiToken != "" {
		req.Header.Set("X-API-Token", c.apiToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp ErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Code != "" {
			return nil, &ClientError{
				StatusCode: resp.StatusCode,
				Code:       errResp.Error.Code,
				Message:    errResp.Error.Message,
			}
		}
		return nil, &ClientError{
			StatusCode: resp.StatusCode,
			Code:       "unknown",
			Message:    string(respBody),
		}
	}

	return respBody, nil
}

// Health checks the API service health
func (c *client) Health(ctx context.Context) (*HealthResponse, error) {
	body, err := c.doRequest(ctx, http.MethodGet, "/v1/health", nil)
	if err != nil {
		return nil, err
	}

	var resp HealthResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp, nil
}

// SpotCreateOrder creates a new spot order
func (c *client) SpotCreateOrder(ctx context.Context, req SpotOrderRequest) (*OrderInfo, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/v1/spot/orders", req)
	if err != nil {
		return nil, err
	}

	var resp OrderResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp.Order, nil
}

// SpotCancelOrder cancels a spot order
func (c *client) SpotCancelOrder(ctx context.Context, symbol string, orderID int64) (*OrderInfo, error) {
	req := CancelOrderRequest{Symbol: symbol, OrderID: orderID}
	body, err := c.doRequest(ctx, http.MethodPost, "/v1/spot/orders/cancel", req)
	if err != nil {
		return nil, err
	}

	var resp OrderResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp.Order, nil
}

// SpotGetOrder queries a spot order
func (c *client) SpotGetOrder(ctx context.Context, symbol string, orderID int64) (*OrderInfo, error) {
	path := fmt.Sprintf("/v1/spot/orders?symbol=%s&order_id=%d", symbol, orderID)
	body, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var resp OrderResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp.Order, nil
}

// SpotGetPrice gets the current spot price for a symbol
func (c *client) SpotGetPrice(ctx context.Context, symbol string) (*PriceResponse, error) {
	path := fmt.Sprintf("/v1/spot/price?symbol=%s", symbol)
	body, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var resp PriceResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp, nil
}

// SpotTrackWebhook creates a webhook track job for a spot order
func (c *client) SpotTrackWebhook(ctx context.Context, req TrackWebhookRequest) (*TrackWebhookResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/v1/spot/orders/track-webhook", req)
	if err != nil {
		return nil, err
	}
	fmt.Println("spot track webhook->", string(body))
	var resp TrackWebhookResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp, nil
}

// CoinMCreateOrder creates a new Coin-M futures order
func (c *client) CoinMCreateOrder(ctx context.Context, req CoinMOrderRequest) (*OrderInfo, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/v1/coinm/orders", req)
	if err != nil {
		return nil, err
	}

	var resp OrderResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp.Order, nil
}

// CoinMCancelOrder cancels a Coin-M futures order
func (c *client) CoinMCancelOrder(ctx context.Context, symbol string, orderID int64) (*OrderInfo, error) {
	req := CancelOrderRequest{Symbol: symbol, OrderID: orderID}
	body, err := c.doRequest(ctx, http.MethodPost, "/v1/coinm/orders/cancel", req)
	if err != nil {
		return nil, err
	}

	var resp OrderResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp.Order, nil
}

// CoinMGetOrder queries a Coin-M futures order
func (c *client) CoinMGetOrder(ctx context.Context, symbol string, orderID int64) (*OrderInfo, error) {
	path := fmt.Sprintf("/v1/coinm/orders?symbol=%s&order_id=%d", symbol, orderID)
	body, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var resp OrderResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp.Order, nil
}

// CoinMGetPrice gets the current Coin-M futures price for a symbol
func (c *client) CoinMGetPrice(ctx context.Context, symbol string) (*PriceResponse, error) {
	path := fmt.Sprintf("/v1/coinm/price?symbol=%s", symbol)
	body, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var resp PriceResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp, nil
}

// CoinMGetQuarterSymbols gets the current and next quarter contract symbols
func (c *client) CoinMGetQuarterSymbols(ctx context.Context, base string) (*QuarterSymbolsResponse, error) {
	path := fmt.Sprintf("/v1/coinm/quarter-symbols?base=%s", base)
	body, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var resp QuarterSymbolsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp, nil
}

// CoinMTrackWebhook creates a webhook track job for a Coin-M futures order
func (c *client) CoinMTrackWebhook(ctx context.Context, req TrackWebhookRequest) (*TrackWebhookResponse, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/v1/coinm/orders/track-webhook", req)
	if err != nil {
		return nil, err
	}
	var resp TrackWebhookResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp, nil
}

// GetTrackJob gets information about a track job
func (c *client) GetTrackJob(ctx context.Context, jobID string) (*TrackJobInfo, error) {
	path := fmt.Sprintf("/v1/track-jobs/%s", jobID)
	body, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var resp TrackJobInfo
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp, nil
}

// CancelTrackJob cancels a track job
func (c *client) CancelTrackJob(ctx context.Context, jobID string) error {
	path := fmt.Sprintf("/v1/track-jobs/%s/cancel", jobID)
	_, err := c.doRequest(ctx, http.MethodPost, path, nil)
	return err
}
