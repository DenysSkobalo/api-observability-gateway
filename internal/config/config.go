package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	ProxyPort   string        `json:"proxy_port"`
	MetricsPort string        `json:"metrics_port"`
	Routing     []RouteConfig `json:"routing"`
}

type RouteConfig struct {
	Path      string   `json:"path"`
	Upstreams []string `json:"upstreams"`
}

func LoadConfig(path string) (*Config, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(file, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
