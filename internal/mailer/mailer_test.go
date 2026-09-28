package mailer

import (
	"encoding/base64"
	"errors"
	"mime"
	"strings"
	"testing"
	"time"
)

// TestBuildReminderEmail 提前 N 天场景：快照式断言关键内容均出现。
func TestBuildReminderEmail(t *testing.T) {
	info := ReminderInfo{
		Title:          "妈妈生日",
		Content:        "记得订蛋糕",
		OccurrenceDate: "2026-06-25",
		DateLabel:      "农历 五月廿五",
		OffsetDays:     3,
		RepeatLabel:    "每年",
	}
	subject, body := BuildReminderEmail(info)

	if want := "[岁记] 妈妈生日 · 还有 3 天"; subject != want {
		t.Errorf("主题 = %q, 期望 %q", subject, want)
	}
	for _, want := range []string{
		"妈妈生日",       // 标题
		"2026-06-25", // 发生日期
		"农历 五月廿五",    // 日期标注
		"提前 3 天",     // 本次提醒类型
		"每年",         // 重复方式
		"记得订蛋糕",      // 备注
		"#fbf6ec",    // 奶油底色
		"#e58f9c",    // 玫瑰色标题
		"每天 08:00（北京时间）由 Remind 发送", // 底部小字
	} {
		if !strings.Contains(body, want) {
			t.Errorf("正文缺少关键内容 %q", want)
		}
	}
}

// TestBuildReminderEmailToday 当天场景：主题为「就是今天」，类型为「当天提醒」，无备注不显示。
func TestBuildReminderEmailToday(t *testing.T) {
	info := ReminderInfo{
		Title:          "妈妈生日",
		OccurrenceDate: "2026-06-25",
		DateLabel:      "农历 五月廿五",
		OffsetDays:     0,
		RepeatLabel:    "每年",
	}
	subject, body := BuildReminderEmail(info)

	if want := "[岁记] 妈妈生日 · 就是今天"; subject != want {
		t.Errorf("主题 = %q, 期望 %q", subject, want)
	}
	if !strings.Contains(body, "当天提醒") {
		t.Error("当天正文应包含「当天提醒」")
	}
	if strings.Contains(body, "备注") {
		t.Error("无备注时正文不应出现「备注」")
	}
	if strings.Contains(body, "还有") {
		t.Error("当天正文不应出现「还有 N 天」文案")
	}
}

// TestBuildMIME 断言 MIME 报文的头部与 base64 正文。
func TestBuildMIME(t *testing.T) {
	const from = "from@remind.local"
	const to = "to@remind.local"
	const subj = "岁记提醒：测试"
	const htmlBody = "<p>你好，岁记</p>"

	msg := buildMIME(from, to, subj, htmlBody)

	// 头部基础断言
	if !strings.Contains(msg, "From: ") || !strings.Contains(msg, from) {
		t.Errorf("报文缺少正确的 From 头:\n%s", msg)
	}
	if !strings.Contains(msg, "To: ") || !strings.Contains(msg, "<"+to+">") {
		t.Errorf("报文缺少 To 头 %q", to)
	}
	if !strings.Contains(msg, "MIME-Version: 1.0") {
		t.Error("报文缺少 MIME-Version 头")
	}

	// 逐行解析头部
	var subjectLine, ctypeLine, cteLine string
	for _, line := range strings.Split(msg, "\r\n") {
		switch {
		case strings.HasPrefix(line, "Subject: "):
			subjectLine = line
		case strings.HasPrefix(line, "Content-Type: "):
			ctypeLine = line
		case strings.HasPrefix(line, "Content-Transfer-Encoding: "):
			cteLine = line
		}
	}

	// Subject 应为 RFC 2047 base64 编码（小写 b 标记），且可解码还原
	if !strings.HasPrefix(subjectLine, "Subject: =?UTF-8?") || !strings.Contains(subjectLine, "?b?") {
		t.Errorf("Subject 未使用 RFC 2047 base64 编码: %q", subjectLine)
	}
	enc := strings.TrimPrefix(subjectLine, "Subject: ")
	var decoder mime.WordDecoder
	decoded, err := decoder.DecodeHeader(enc)
	if err != nil {
		t.Fatalf("解码 Subject 失败: %v", err)
	}
	if decoded != subj {
		t.Errorf("Subject 解码后 = %q, 期望 %q", decoded, subj)
	}

	if !strings.Contains(ctypeLine, "text/html") || !strings.Contains(ctypeLine, "charset=UTF-8") {
		t.Errorf("Content-Type = %q, 期望含 text/html 与 charset=UTF-8", ctypeLine)
	}
	if cteLine != "Content-Transfer-Encoding: base64" {
		t.Errorf("Content-Transfer-Encoding = %q", cteLine)
	}

	// 正文 base64 解码后应还原为原始 HTML
	parts := strings.SplitN(msg, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("报文中未找到头部与正文的空行分隔")
	}
	raw := strings.ReplaceAll(parts[1], "\r\n", "")
	got, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("正文 base64 解码失败: %v", err)
	}
	if string(got) != htmlBody {
		t.Errorf("正文解码后 = %q, 期望 %q", got, htmlBody)
	}
}

// newTestMailer 创建注入了替身投递与空等待的 Mailer。
func newTestMailer(calls *int, sleeps *[]time.Duration, fail error) *Mailer {
	m := New(SMTPConfig{
		Host: "smtp.example.com", Port: 465,
		Username: "user", Password: "pass", From: "from@remind.local",
	})
	m.sleep = func(d time.Duration) { *sleeps = append(*sleeps, d) }
	m.send = func(to, msg string) error {
		*calls++
		return fail
	}
	return m
}

// TestSendWithRetryFirstSuccess 首次成功只发 1 次，不产生等待。
func TestSendWithRetryFirstSuccess(t *testing.T) {
	var calls int
	var sleeps []time.Duration

	m := newTestMailer(&calls, &sleeps, nil)
	if err := m.SendWithRetry("to@remind.local", "标题", "<b>hi</b>"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if calls != 1 {
		t.Errorf("发送次数 = %d, 期望 1", calls)
	}
	if len(sleeps) != 0 {
		t.Errorf("首次成功不应等待, 实际等待 %v", sleeps)
	}
}

// TestSendWithRetryAllFail 一直失败时共尝试 4 次，返回错误且等待 1s/5s/25s。
func TestSendWithRetryAllFail(t *testing.T) {
	var calls int
	var sleeps []time.Duration

	m := newTestMailer(&calls, &sleeps, errors.New("连接失败"))
	err := m.SendWithRetry("to@remind.local", "标题", "<b>hi</b>")
	if err == nil {
		t.Fatal("全部失败时应返回错误")
	}
	if calls != 4 {
		t.Errorf("发送次数 = %d, 期望 4", calls)
	}
	want := []time.Duration{time.Second, 5 * time.Second, 25 * time.Second}
	if len(sleeps) != len(want) {
		t.Fatalf("等待次数 = %d (%v), 期望 %v", len(sleeps), sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Errorf("第 %d 次等待 = %v, 期望 %v", i+1, sleeps[i], want[i])
		}
	}
}

// TestSendWithRetrySucceedOnThird 第 3 次成功则共发 3 次，之后不再重试。
func TestSendWithRetrySucceedOnThird(t *testing.T) {
	var calls int
	var sleeps []time.Duration

	fail := errors.New("暂时失败")
	m := newTestMailer(&calls, &sleeps, fail)
	m.send = func(to, msg string) error {
		calls++
		if calls < 3 {
			return fail // 前两次失败，第 3 次成功
		}
		return nil
	}

	if err := m.SendWithRetry("to@remind.local", "标题", "<b>hi</b>"); err != nil {
		t.Fatalf("第 3 次应成功: %v", err)
	}
	if calls != 3 {
		t.Errorf("发送次数 = %d, 期望 3", calls)
	}
	want := []time.Duration{time.Second, 5 * time.Second}
	if len(sleeps) != len(want) {
		t.Fatalf("等待次数 = %d (%v), 期望 %v", len(sleeps), sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Errorf("第 %d 次等待 = %v, 期望 %v", i+1, sleeps[i], want[i])
		}
	}
}

// TestSendUsesInjectedTransport 验证 Send 会构造 MIME 并经注入的投递函数发出。
func TestSendUsesInjectedTransport(t *testing.T) {
	var gotTo, gotMsg string
	m := New(SMTPConfig{
		Host: "smtp.example.com", Port: 465,
		Username: "user", Password: "pass", From: "from@remind.local",
	})
	m.send = func(to, msg string) error { gotTo, gotMsg = to, msg; return nil }

	if err := m.Send("to@remind.local", "岁记", "<p>hi</p>"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if gotTo != "to@remind.local" {
		t.Errorf("投递收件人 = %q, 期望 to@remind.local", gotTo)
	}
	if !strings.HasPrefix(gotMsg, "From: ") || !strings.Contains(gotMsg, "Subject: =?") {
		t.Errorf("投递报文 MIME 不符合预期:\n%s", gotMsg)
	}
}
