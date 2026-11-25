// Package mailgw 是「调用外部邮件网关」的客户端。
//
// 背景
// ----
// 项目里多个服务都要发通知（Remind 备忘、CICD 证书到期、Backup 备份结果、
// Ucenter 用户通知）。最初各自直连 SMTP，导致 SMTP 凭据散落多处、兼容
// 代码重复、换服务商要改所有仓库。
//
// 现在统一成独立的 Mail-api 服务，各服务只调它的 HTTP 接口。
//
// 为什么保留本地 mailer 包
// ----------------------
// mailgw 是「有网关就用网关」；如果某个部署没配网关（或想独立运行），
// 仍可退回本地 mailer 直连 SMTP。由调用方决定用哪个。
package mailgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config 邮件网关客户端配置。
type Config struct {
	// URL 网关的发信地址，如 http://127.0.0.1:8095/internal/mail/send
	URL string
	// Token Bearer 令牌，可空
	Token string
	// FromName 默认发件人显示名，如「岁记提醒」
	FromName string
	// Timeout 单次请求超时，默认 15s
	Timeout time.Duration
}

// Enabled 配置是否完整到可以调用。
func (c Config) Enabled() bool {
	return strings.TrimSpace(c.URL) != ""
}

// Client 邮件网关客户端。
type Client struct {
	cfg  Config
	http *http.Client
	// post 便于测试注入
	post func(ctx context.Context, url, token string, body []byte) error
}

// New 创建客户端。
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	c := &Client{cfg: cfg, http: &http.Client{Timeout: timeout}}
	c.post = c.doPost
	return c
}

// Enabled 配置是否完整到可以调用网关。
func (c *Client) Enabled() bool {
	return c != nil && c.cfg.Enabled()
}

// Send 发一封 HTML 邮件到指定收件人。
func (c *Client) Send(ctx context.Context, to []string, subject, html string) error {
	if !c.cfg.Enabled() {
		return errors.New("未配置邮件网关地址")
	}
	if len(to) == 0 {
		return errors.New("收件人为空")
	}
	payload := struct {
		FromName string   `json:"from_name,omitempty"`
		To       []string `json:"to"`
		Subject  string   `json:"subject"`
		HTML     string   `json:"html"`
	}{FromName: c.cfg.FromName, To: to, Subject: subject, HTML: html}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化邮件请求失败: %w", err)
	}
	return c.post(ctx, c.cfg.URL, c.cfg.Token, body)
}

func (c *Client) doPost(ctx context.Context, url, token string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("调用邮件网关失败: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return fmt.Errorf("邮件网关返回 %d: %s", resp.StatusCode, msg)
	}
	return nil
}

// SendWithRetry 实现 scheduler.Sender 接口，让网关客户端可直接替换本地 mailer。
//
// 重试策略：网关自身已经有发信重试，这里只对「网络层失败」做少量重试
// （网关没起、隧道抖动），不重复做长退避。
func (c *Client) SendWithRetry(to, subject, body string) error {
	if !c.cfg.Enabled() {
		return errors.New("未配置邮件网关地址")
	}
	var err error
	for attempt, wait := range []time.Duration{0, time.Second, 3 * time.Second} {
		if wait > 0 {
			time.Sleep(wait)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err = c.Send(ctx, []string{to}, subject, body)
		cancel()
		if err == nil {
			return nil
		}
		_ = attempt
	}
	return err
}

// 编译期断言：*Client 满足 scheduler.Sender（避免接口漂移）。
var _ interface {
	SendWithRetry(to, subject, body string) error
} = (*Client)(nil)
