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
	// MailGateway 内部邮件网关配置。其他服务（如 CICD）通过它发信，
	// 这样 SMTP 凭据只在这一处。
	MailGateway MailGateway `yaml:"mail_gateway"`
}

// MailGateway 指向独立邮件网关（Mail-api）的客户端配置。
//
// 为什么不在这里实现 SMTP：项目里四个服务（Remind/CICD/Backup/Ucenter）
// 都要发信。各自直连 SMTP 会导致凭据散落、兼容代码重复、换服务商要改
// 多个仓库。统一成独立服务后，这里只需要一个 URL。
//
// 留空则退回本地 SMTP 直连（见 smtp 段），保证老部署仍能工作。
type MailGateway struct {
	// URL 网关发信地址，如 http://127.0.0.1:8095/internal/mail/send
	URL string `yaml:"url"`
	// Token Bearer 令牌
	Token string `yaml:"token"`
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
	// 邮件网关地址与令牌支持环境变量覆盖（部署时注入，不落配置文件）
	if u := os.Getenv("MAIL_GATEWAY_URL"); u != "" {
		c.MailGateway.URL = u
	}
	if t := os.Getenv("MAIL_GATEWAY_TOKEN"); t != "" {
		c.MailGateway.Token = t
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
