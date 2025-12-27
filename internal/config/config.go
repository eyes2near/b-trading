// internal/config/config.go
package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	App          AppConfig          `mapstructure:"app"`
	Server       ServerConfig       `mapstructure:"server"`
	Database     DatabaseConfig     `mapstructure:"database"`
	BinanceREST  BinanceRESTConfig  `mapstructure:"binance_rest"`
	Webhook      WebhookConfig      `mapstructure:"webhook"`
	Derivative   DerivativeConfig   `mapstructure:"derivative"`
	Log          LogConfig          `mapstructure:"log"`
	Markets      MarketsConfig      `mapstructure:"markets"`
	MarketStream MarketStreamConfig `mapstructure:"market_stream"`
}

type MarketStreamConfig struct {
	BrokerURL         string        `mapstructure:"broker_url"`
	PublisherAPI      string        `mapstructure:"publisher_api"`
	ReconnectDelay    time.Duration `mapstructure:"reconnect_delay"`
	MaxReconnect      time.Duration `mapstructure:"max_reconnect"`
	HeartbeatInterval time.Duration `mapstructure:"heartbeat_interval"`
	DefaultLevels     int           `mapstructure:"default_levels"`
}

type AppConfig struct {
	Name    string `mapstructure:"name"`
	Version string `mapstructure:"version"`
	Env     string `mapstructure:"env"`
}

type ServerConfig struct {
	Port     string `mapstructure:"port"`
	Host     string `mapstructure:"host"`
	CertFile string `mapstructure:"cert_file"`
	KeyFile  string `mapstructure:"key_file"`
}

type DatabaseConfig struct {
	DSN             string        `mapstructure:"dsn"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `mapstructure:"conn_max_idle_time"`
	LogLevel        int           `mapstructure:"log_level"`
}

type BinanceRESTConfig struct {
	BaseURL  string        `mapstructure:"base_url"`
	APIToken string        `mapstructure:"api_token"`
	Timeout  time.Duration `mapstructure:"timeout"`
}

type WebhookConfig struct {
	Enabled         bool   `mapstructure:"enabled"`
	CallbackBaseURL string `mapstructure:"callback_base_url"`
	CallbackPath    string `mapstructure:"callback_path"`
	Secret          string `mapstructure:"secret"`
}

type DerivativeConfig struct {
	Enabled             bool `mapstructure:"enabled"`
	RequireMatchingRule bool `mapstructure:"require_matching_rule"`
}

type LogConfig struct {
	Level      string `mapstructure:"level"`
	Output     string `mapstructure:"output"`
	MaxSize    int    `mapstructure:"max_size"`
	MaxBackups int    `mapstructure:"max_backups"`
	MaxAge     int    `mapstructure:"max_age"`
}

type MarketsConfig struct {
	SpotSymbols []string `mapstructure:"spot_symbols"`
	CoinmBases  []string `mapstructure:"coinm_bases"`
}

// GetWebhookURL returns the full webhook callback URL
func (c *Config) GetWebhookURL() string {
	return c.Webhook.CallbackBaseURL + c.Webhook.CallbackPath
}

// LoadConfig loads configuration from the specified file path
func LoadConfig(configPath string) (*Config, error) {
	viper.SetConfigFile(configPath)
	viper.SetConfigType("yaml")

	// Set default values
	viper.SetDefault("server.port", "8080")
	viper.SetDefault("server.host", "0.0.0.0")
	viper.SetDefault("binance_rest.base_url", "http://localhost:1088")
	viper.SetDefault("binance_rest.timeout", "30s")
	viper.SetDefault("webhook.enabled", true)
	viper.SetDefault("webhook.callback_path", "/internal/webhook/binance")
	viper.SetDefault("derivative.enabled", true)
	viper.SetDefault("derivative.require_matching_rule", true)

	// MarketStream defaults
	viper.SetDefault("market_stream.broker_url", "ws://localhost:1880/ws")
	viper.SetDefault("market_stream.publisher_api", "http://localhost:9090")
	viper.SetDefault("market_stream.reconnect_delay", "1s")
	viper.SetDefault("market_stream.max_reconnect", "30s")
	viper.SetDefault("market_stream.heartbeat_interval", "30s")
	viper.SetDefault("market_stream.default_levels", 5)

	// Read environment variables
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &config, nil
}
