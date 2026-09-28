// Package config 负责加载与校验应用配置。
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// SMTP 邮件发送配置。
type SMTP struct {
	Host     string `yaml:"host"`     // SMTP 服务器地址，必填
	Port     int    `yaml:"port"`     // 端口，默认 465
	Username string `yaml:"username"` // 登录用户名，必填
	Password string `yaml:"password"` // 登录密码，选填，环境变量 SMTP_PASSWORD 可覆盖
	From     string `yaml:"from"`     // 发件人，选填，默认同 Username
}

// Config 应用配置。
type Config struct {
	Listen         string `yaml:"listen"`           // 监听地址，默认 ":8085"
	DBPath         string `yaml:"db_path"`          // SQLite 文件路径，默认 "remind.db"
	DailySendTime  string `yaml:"daily_send_time"`  // 每日发送时间，北京时间 HH:MM，默认 "08:00"
	UcenterBaseURL string `yaml:"ucenter_base_url"` // 用户中心地址，必填
	AppName        string `yaml:"app_name"`         // 应用名，默认 "remind"
	SMTP           SMTP   `yaml:"smtp"`
}

// Load 读取 yaml 配置并应用默认值与环境变量覆盖，返回校验后的配置。
func Load(path string) (*Config, error) {
	cfg := &Config{}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
		}
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyDefaults 补齐默认值并处理环境变量覆盖。
func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = ":8085"
	}
	if c.DBPath == "" {
		c.DBPath = "remind.db"
	}
	if c.DailySendTime == "" {
		c.DailySendTime = "08:00"
	}
	if c.AppName == "" {
		c.AppName = "remind"
	}
	if c.SMTP.Port == 0 {
		c.SMTP.Port = 465
	}
	// 环境变量优先覆盖 SMTP 密码
	if p := os.Getenv("SMTP_PASSWORD"); p != "" {
		c.SMTP.Password = p
	}
	// From 默认同 Username
	if c.SMTP.From == "" {
		c.SMTP.From = c.SMTP.Username
	}
}

// validate 校验必填项，缺失时返回包含所有缺失字段名的错误。
func (c *Config) validate() error {
	var missing []string
	if c.UcenterBaseURL == "" {
		missing = append(missing, "ucenter_base_url")
	}
	if c.SMTP.Host == "" {
		missing = append(missing, "smtp.host")
	}
	if c.SMTP.Username == "" {
		missing = append(missing, "smtp.username")
	}
	if len(missing) > 0 {
		return fmt.Errorf("配置缺少必填项: %v", missing)
	}
	return nil
}
