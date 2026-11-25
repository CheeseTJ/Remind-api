package mailgw

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// mailgw 客户端的测试。
//
// 重点是「未配置网关时不炸」和「请求体契约正确」—— 前者保证老部署
// 不配网关也能跑，后者保证和 Mail-api 服务的接口约定不漂移。

func TestConfigEnabled(t *testing.T) {
	if (Config{}).Enabled() {
		t.Error("空配置不该 enabled")
	}
	if (Config{URL: "   "}).Enabled() {
		t.Error("空白 URL 不该 enabled")
	}
	if !(Config{URL: "http://127.0.0.1:8095/x"}).Enabled() {
		t.Error("有 URL 应 enabled")
	}
}

// 未配置时 Send 要返回错误，而不是静默成功 —— 否则调用方以为发出去了。
func TestSendWithoutURLFails(t *testing.T) {
	c := New(Config{})
	called := false
	c.post = func(context.Context, string, string, []byte) error {
		called = true
		return nil
	}
	if err := c.Send(context.Background(), []string{"a@b.c"}, "s", "h"); err == nil {
		t.Error("未配置 URL 时应报错")
	}
	if called {
		t.Error("不该真的发请求")
	}
}

func TestSendEmptyRecipientsFails(t *testing.T) {
	c := New(Config{URL: "http://x/y"})
	if err := c.Send(context.Background(), nil, "s", "h"); err == nil {
		t.Error("收件人为空时应报错")
	}
}

// 请求体必须符合 Mail-api 的契约：from_name / to / subject / html。
func TestSendPayloadContract(t *testing.T) {
	c := New(Config{
		URL:      "http://127.0.0.1:8095/internal/mail/send",
		Token:    "tk",
		FromName: "岁记提醒",
	})
	var gotURL, gotToken string
	var gotBody []byte
	c.post = func(_ context.Context, url, token string, body []byte) error {
		gotURL, gotToken, gotBody = url, token, body
		return nil
	}

	if err := c.Send(context.Background(), []string{"a@b.c"}, "主题", "<b>正文</b>"); err != nil {
		t.Fatal(err)
	}
	if gotURL != "http://127.0.0.1:8095/internal/mail/send" {
		t.Errorf("URL = %q", gotURL)
	}
	if gotToken != "tk" {
		t.Errorf("Token = %q", gotToken)
	}

	var payload struct {
		FromName string   `json:"from_name"`
		To       []string `json:"to"`
		Subject  string   `json:"subject"`
		HTML     string   `json:"html"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v", err)
	}
	if payload.FromName != "岁记提醒" {
		t.Errorf("from_name = %q", payload.FromName)
	}
	if len(payload.To) != 1 || payload.To[0] != "a@b.c" {
		t.Errorf("to = %v", payload.To)
	}
	if payload.Subject != "主题" || payload.HTML != "<b>正文</b>" {
		t.Errorf("subject/html 不符: %q / %q", payload.Subject, payload.HTML)
	}
}

// 网关返回非 2xx 时要把响应体带进错误里，便于排查。
func TestSendPropagatesGatewayError(t *testing.T) {
	c := New(Config{URL: "http://x/y"})
	c.post = func(context.Context, string, string, []byte) error {
		return errors.New("邮件网关返回 400: 收件人不在白名单内")
	}
	err := c.Send(context.Background(), []string{"a@b.c"}, "s", "h")
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !strings.Contains(err.Error(), "白名单") {
		t.Errorf("错误信息应透传网关原因: %v", err)
	}
}

// SendWithRetry 适配 scheduler.Sender 接口；未配置时直接报错不重试。
func TestSendWithRetryNoURL(t *testing.T) {
	c := New(Config{})
	if err := c.SendWithRetry("a@b.c", "s", "h"); err == nil {
		t.Error("未配置 URL 时应报错")
	}
}

// Enabled 方法（客户端用）与 Config.Enabled 语义一致。
func TestClientEnabled(t *testing.T) {
	if New(Config{}).Enabled() {
		t.Error("空配置的客户端不该 enabled")
	}
	if !New(Config{URL: "http://x/y"}).Enabled() {
		t.Error("有 URL 应 enabled")
	}
	var nilClient *Client
	if nilClient.Enabled() {
		t.Error("nil 客户端不该 enabled（防止空指针）")
	}
}
