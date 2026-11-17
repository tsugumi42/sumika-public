// Package config 读取设置：内置默认值，加上可选的设置文件。
package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
)

//go:embed defaults.json
var defaultsJSON []byte

// Cfg 是程序的全部设置。
type Cfg struct {
	LowStockThreshold int    `json:"low_stock_threshold"`
	PageSize          int    `json:"page_size"`
	Currency          string `json:"currency"`
}

// LoadCfg 先取内置默认值，path 不为空时再用文件里写了的项覆盖。
func LoadCfg(path string) (Cfg, error) {
	var c Cfg
	if err := json.Unmarshal(defaultsJSON, &c); err != nil {
		return Cfg{}, fmt.Errorf("默认设置坏了：%w", err)
	}
	if path == "" {
		return c, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Cfg{}, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Cfg{}, fmt.Errorf("%s：%w", path, err)
	}
	return c, nil
}
