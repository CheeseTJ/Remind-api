// Package mailer 提供基于标准库 net/smtp + crypto/tls 的邮件发送能力。
// 支持 465 端口隐式 TLS 与其他端口（如 587）STARTTLS 两种模式，
// 并内置失败重试（1s/5s/25s）与提醒邮件 HTML 构建。
package mailer

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// fromName 发件人显示名称。
const fromName = "岁记提醒"

// SMTPConfig SMTP 连接与认证配置。
type SMTPConfig struct {
	Host     string // SMTP 服务器地址
	Port     int    // 端口：465 隐式 TLS；其他端口（如 587）走 STARTTLS
	Username string // 登录用户名
	Password string // 登录密码
	From     string // 发件人邮箱
}

// Mailer 邮件发送器；send 与 sleep 均可注入替换，便于测试。
type Mailer struct {
	cfg   SMTPConfig
	sleep func(time.Duration)        // 重试间隔等待，默认 time.Sleep
	send  func(to, msg string) error // 连接层投递函数，默认 deliver，测试可注入替身
}

// New 创建使用默认投递与等待实现的 Mailer。
func New(cfg SMTPConfig) *Mailer {
	m := &Mailer{cfg: cfg, sleep: time.Sleep}
	m.send = m.deliver
	return m
}

// Send 发送一封 HTML 邮件。
func (m *Mailer) Send(to, subject, htmlBody string) error {
	send := m.send
	if send == nil {
		send = m.deliver
	}
	msg := buildMIME(m.cfg.From, to, subject, htmlBody)
	return send(to, msg)
}

// retryDelays 失败重试的等待间隔，共最多 1+3=4 次尝试。
var retryDelays = []time.Duration{time.Second, 5 * time.Second, 25 * time.Second}

// SendWithRetry 带重试的发送：失败后按 1s/5s/25s 重试，
// 全部失败时返回最后一次错误。
func (m *Mailer) SendWithRetry(to, subject, htmlBody string) error {
	sleep := m.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	var err error
	for attempt := 0; attempt <= len(retryDelays); attempt++ {
		if attempt > 0 {
			sleep(retryDelays[attempt-1])
		}
		err = m.Send(to, subject, htmlBody)
		if err == nil {
			return nil
		}
	}
	return err
}

// deliver 通过 SMTP 连接把已构造好的邮件投递出去。
func (m *Mailer) deliver(to, msg string) error {
	host := m.cfg.Host
	addr := host + ":" + strconv.Itoa(m.cfg.Port)

	if m.cfg.Port == 465 {
		// 465 端口：隐式 TLS，直接建立加密连接
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
		if err != nil {
			return fmt.Errorf("建立 TLS 连接 %s 失败: %w", addr, err)
		}
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			conn.Close()
			return fmt.Errorf("创建 SMTP 客户端失败: %w", err)
		}
		return m.serve(c, to, msg)
	}

	// 其他端口（如 587）：先明文连接，再尝试 STARTTLS 升级
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("建立连接 %s 失败: %w", addr, err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("创建 SMTP 客户端失败: %w", err)
	}
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			c.Close()
			return fmt.Errorf("STARTTLS 升级失败: %w", err)
		}
	} else if !isLocalHost(host) {
		// 服务器不支持 STARTTLS 且非本地调试环境，拒绝明文发送
		c.Close()
		return fmt.Errorf("SMTP 服务器 %s 不支持 STARTTLS，拒绝明文发送", host)
	}
	return m.serve(c, to, msg)
}

// serve 执行认证、MAIL FROM/RCPT TO/DATA 并结束会话。
func (m *Mailer) serve(c *smtp.Client, to, msg string) error {
	defer c.Close()

	// 登录认证：优先 PLAIN，服务器不支持时退 LOGIN
	if m.cfg.Username != "" {
		if err := m.auth(c); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := c.Mail(m.cfg.From); err != nil {
		return fmt.Errorf("设置发件人失败: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("设置收件人失败: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("打开数据通道失败: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("写入邮件内容失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("结束邮件内容失败: %w", err)
	}
	return c.Quit()
}

// auth 按服务器公告的机制选择认证方式：PLAIN 优先，其次 LOGIN。
func (m *Mailer) auth(c *smtp.Client) error {
	ok, mechs := c.Extension("AUTH")
	if ok {
		for _, f := range strings.Fields(mechs) {
			switch strings.ToUpper(f) {
			case "PLAIN":
				return c.Auth(smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host))
			case "LOGIN":
				return c.Auth(&loginAuth{username: m.cfg.Username, password: m.cfg.Password})
			}
		}
		return errors.New("服务器不支持的认证机制: " + mechs)
	}
	return errors.New("服务器未公告 AUTH 扩展，无法认证")
}

// loginAuth 实现 SMTP "AUTH LOGIN"（标准库未内置）。
type loginAuth struct {
	username, password string
}

// Start 返回认证机制名 LOGIN。
func (a *loginAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", nil, nil
}

// Next 根据服务器提示依次回复用户名与密码。
func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	prompt := strings.ToLower(string(fromServer))
	switch {
	case bytes.Contains(fromServer, []byte("Username")), bytes.Contains(fromServer, []byte("username")),
		strings.Contains(prompt, "user"):
		return []byte(a.username), nil
	case bytes.Contains(fromServer, []byte("Password")), bytes.Contains(fromServer, []byte("password")),
		strings.Contains(prompt, "pass"):
		return []byte(a.password), nil
	default:
		return nil, fmt.Errorf("意外的服务器提示: %q", fromServer)
	}
}

// isLocalHost 判断是否本地调试地址（允许无 TLS 明文发送）。
func isLocalHost(host string) bool {
	h := strings.ToLower(host)
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// buildMIME 构造完整 MIME 报文（纯函数，便于测试）：
// 头部含 From/To/Subject（RFC 2047 base64 编码主题）、
// text/html; charset=UTF-8 与 base64 正文（按 76 字符换行）。
func buildMIME(from, to, subject, htmlBody string) string {
	fromAddr := (&mail.Address{Name: fromName, Address: from}).String()
	toAddr := (&mail.Address{Address: to}).String()
	subj := mime.BEncoding.Encode("UTF-8", subject)

	var b strings.Builder
	b.WriteString("From: " + fromAddr + "\r\n")
	b.WriteString("To: " + toAddr + "\r\n")
	b.WriteString("Subject: " + subj + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("\r\n")
	b.WriteString(wrapBase64(base64.StdEncoding.EncodeToString([]byte(htmlBody))))
	return b.String()
}

// wrapBase64 将 base64 字符串按 76 字符换行，避免邮件正文出现超长行。
func wrapBase64(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76])
		b.WriteString("\r\n")
		s = s[76:]
	}
	b.WriteString(s)
	return b.String()
}

// ReminderInfo 构造提醒邮件所需的业务信息。
type ReminderInfo struct {
	Title          string // 提醒标题，如「妈妈生日」
	Content        string // 备注内容，选填，非空才显示
	OccurrenceDate string // 发生日，格式 "2006-01-02"
	DateLabel      string // 日期标注，如「农历 五月廿五」「阳历 10月23日」
	OffsetDays     int    // 距发生日天数，0 表示当天
	RepeatLabel    string // 重复方式：每年/每月/每日/单次
}

// BuildReminderEmail 构造提醒邮件的主题与 HTML 正文（暖色调内联样式）。
func BuildReminderEmail(info ReminderInfo) (subject string, body string) {
	// 主题：offset=0 为「就是今天」，否则「还有 N 天」
	if info.OffsetDays > 0 {
		subject = fmt.Sprintf("[岁记] %s · 还有 %d 天", info.Title, info.OffsetDays)
	} else {
		subject = fmt.Sprintf("[岁记] %s · 就是今天", info.Title)
	}

	// 本次提醒类型文案
	remindType := "当天提醒"
	if info.OffsetDays > 0 {
		remindType = fmt.Sprintf("提前 %d 天", info.OffsetDays)
	}

	// 发生日期（附农历/阳历标注）
	dateLine := html.EscapeString(info.OccurrenceDate)
	if info.DateLabel != "" {
		dateLine += "（" + html.EscapeString(info.DateLabel) + "）"
	}

	// 备注块，仅有内容时显示
	note := ""
	if strings.TrimSpace(info.Content) != "" {
		note = `<div style="margin:16px 0 0;padding:12px 14px;background-color:#fdf3ec;border-radius:10px;font-size:13px;color:#8a6a52;line-height:1.7;">📝 备注：` + html.EscapeString(info.Content) + `</div>`
	}

	// HTML 正文：奶油底 + 圆角卡片 + 玫瑰色标题，全部内联样式
	body = `<!DOCTYPE html>
<html lang="zh-CN">
<body style="margin:0;padding:24px 12px;background-color:#fbf6ec;font-family:'PingFang SC','Hiragino Sans GB','Microsoft YaHei',sans-serif;">
  <div style="max-width:480px;margin:0 auto;background-color:#fffdf8;border:1px solid #f3e6d0;border-radius:16px;padding:32px 28px;">
    <p style="margin:0 0 6px;font-size:13px;color:#c9a86a;">🔔 岁记提醒</p>
    <h1 style="margin:0 0 20px;font-size:22px;color:#e58f9c;line-height:1.4;">` + html.EscapeString(info.Title) + `</h1>
    <p style="margin:0 0 10px;font-size:14px;color:#7a6a55;line-height:1.8;">📅 发生日期：<span style="color:#5a4a38;font-weight:600;">` + dateLine + `</span></p>
    <p style="margin:0 0 10px;font-size:14px;color:#7a6a55;line-height:1.8;">⏰ 本次提醒：<span style="color:#5a4a38;font-weight:600;">` + remindType + `</span></p>
    <p style="margin:0;font-size:14px;color:#7a6a55;line-height:1.8;">🔁 重复方式：<span style="color:#5a4a38;font-weight:600;">` + html.EscapeString(info.RepeatLabel) + `</span></p>` + note + `
    <hr style="border:none;border-top:1px solid #f0e4cf;margin:22px 0 14px;">
    <p style="margin:0;font-size:12px;color:#b5a58f;">每天 08:00（北京时间）由 Remind 发送</p>
  </div>
</body>
</html>`
	return subject, body
}
