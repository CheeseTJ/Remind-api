package scheduler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"remind/internal/mailer"
	"remind/internal/memo"
	"remind/internal/store"

	"github.com/6tail/lunar-go/calendar"
)

// fakeSender 记录发送调用的替身。
type fakeSender struct {
	mu    sync.Mutex
	calls []string // "to|subject"
	fail  bool
}

func (f *fakeSender) send(to, subject, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, to+"|"+subject)
	if f.fail {
		return errors.New("smtp down")
	}
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newTest 构造临时 DB + 注入时钟与发送函数的调度器。
// nowCST 为北京时间（可传任意时区时间，内部按 In(CST) 折算）。
func newTest(t *testing.T, now time.Time, sendTime string) (*Scheduler, *fakeSender, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "remind.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := New(db, mailer.New(mailer.SMTPConfig{Host: "localhost", Username: "t@t"}), sendTime)
	fake := &fakeSender{}
	s.now = func() time.Time { return now }
	s.send = fake.send
	return s, fake, db
}

// insertMemo 直接插入一条备忘记录，返回 ID。
func insertMemo(t *testing.T, db *sql.DB, m memo.Memo) int64 {
	t.Helper()
	if m.AdvanceOffsets == nil {
		m.AdvanceOffsets = []int{}
	}
	offsets, _ := json.Marshal(m.AdvanceOffsets)
	if !m.Enabled {
		m.Enabled = true
	}
	res, err := db.Exec(`INSERT INTO memos(
  user_id,user_email,title,content,date_type,repeat,month,day,lunar_month,lunar_day,lunar_leap,
  advance_offsets,once_date,enabled,created_at,updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.UserID, m.UserEmail, m.Title, m.Content, m.DateType, m.Repeat,
		m.Month, m.Day, m.LunarMonth, m.LunarDay, m.LunarLeap,
		string(offsets), m.OnceDate, 1, "2026-01-01 00:00:00", "2026-01-01 00:00:00")
	if err != nil {
		t.Fatalf("插入测试备忘失败: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("获取插入 ID 失败: %v", err)
	}
	return id
}

// sendLogRows 查询 send_log 全部行 (memo_id, due_date, offset_days)。
func sendLogRows(t *testing.T, db *sql.DB) [][3]any {
	t.Helper()
	rows, err := db.Query(`SELECT memo_id,due_date,offset_days FROM send_log ORDER BY id`)
	if err != nil {
		t.Fatalf("查询 send_log 失败: %v", err)
	}
	defer rows.Close()
	var out [][3]any
	for rows.Next() {
		var id, off int64
		var due string
		if err := rows.Scan(&id, &due, &off); err != nil {
			t.Fatalf("扫描 send_log 失败: %v", err)
		}
		out = append(out, [3]any{id, due, off})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 send_log 失败: %v", err)
	}
	return out
}

// beijing 构造北京时间。
func beijing(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, CST)
}

// 用例①：yearly solar 备忘 month/day=今天，offsets [0,3] → 恰好 1 封（offset 3 非发生日）。
func TestRunYearlySolarToday(t *testing.T) {
	now := beijing(2026, time.May, 20, 9, 0)
	s, fake, db := newTest(t, now, "08:00")
	insertMemo(t, db, memo.Memo{
		UserID: "u1", UserEmail: "a@t.com", Title: "阳历生日",
		DateType: "solar", Repeat: "yearly", Month: new(5), Day: new(20),
		AdvanceOffsets: []int{0, 3},
	})

	s.run()
	if got := fake.count(); got != 1 {
		t.Fatalf("期望发送 1 封，实际 %d 封", got)
	}
	logs := sendLogRows(t, db)
	if len(logs) != 1 || logs[0][1] != "2026-05-20" || logs[0][2] != int64(0) {
		t.Fatalf("send_log 不符合预期: %v", logs)
	}
}

// 用例②：发生日在 3 天后，offsets [0,3] → 发 1 封；再 run 一次仍只发 1 封（去重）。
func TestRunDedupeOnSecondRun(t *testing.T) {
	now := beijing(2026, time.May, 17, 8, 5)
	s, fake, db := newTest(t, now, "08:00")
	insertMemo(t, db, memo.Memo{
		UserID: "u1", UserEmail: "a@t.com", Title: "三天后生日",
		DateType: "solar", Repeat: "yearly", Month: new(5), Day: new(20),
		AdvanceOffsets: []int{0, 3},
	})

	s.run()
	s.run()
	if got := fake.count(); got != 1 {
		t.Fatalf("两次 run 后期望仍只发送 1 封，实际 %d 封", got)
	}
	logs := sendLogRows(t, db)
	if len(logs) != 1 {
		t.Fatalf("send_log 期望 1 行，实际 %d 行: %v", len(logs), logs)
	}
	if logs[0][1] != "2026-05-20" || logs[0][2] != int64(3) {
		t.Fatalf("send_log 应为 due=2026-05-20 offset=3，实际: %v", logs)
	}
}

// 用例③：once 备忘 once_date=今天+7、offsets [0,7] → 发 1 封。
func TestRunOnceInSevenDays(t *testing.T) {
	now := beijing(2026, time.May, 13, 8, 0)
	s, fake, db := newTest(t, now, "08:00")
	insertMemo(t, db, memo.Memo{
		UserID: "u1", UserEmail: "a@t.com", Title: "一次性事项",
		DateType: "solar", Repeat: "once", OnceDate: new("2026-05-20"),
		AdvanceOffsets: []int{0, 7},
	})

	s.run()
	if got := fake.count(); got != 1 {
		t.Fatalf("期望发送 1 封，实际 %d 封", got)
	}
	logs := sendLogRows(t, db)
	if len(logs) != 1 || logs[0][1] != "2026-05-20" || logs[0][2] != int64(7) {
		t.Fatalf("send_log 不符合预期: %v", logs)
	}
}

// 用例④：发送后手动查 send_log 确认唯一（UNIQUE 约束生效）。
func TestSendLogUnique(t *testing.T) {
	now := beijing(2026, time.May, 20, 8, 0)
	s, fake, db := newTest(t, now, "08:00")
	id := insertMemo(t, db, memo.Memo{
		UserID: "u1", UserEmail: "a@t.com", Title: "当天提醒",
		DateType: "solar", Repeat: "yearly", Month: new(5), Day: new(20),
		AdvanceOffsets: []int{0},
	})
	s.run()
	// 手动再插同一键，应因 UNIQUE 失败
	_, err := db.Exec(`INSERT INTO send_log(memo_id,user_id,due_date,offset_days,sent_at) VALUES(?,?,?,?,?)`,
		id, "u1", "2026-05-20", 0, "2026-05-20 08:00:00")
	if err == nil {
		t.Fatal("重复插入 send_log 应因 UNIQUE 约束失败")
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("期望 1 封，实际 %d 封", got)
	}
	if logs := sendLogRows(t, db); len(logs) != 1 {
		t.Fatalf("send_log 期望 1 行，实际: %v", logs)
	}
}

// 用例⑤：时区正确性——now 为 UTC 时区（UTC 2026-09-30 18:00 = 北京 2026-10-01 02:00），
// 备忘为北京今天（10-01），发送时刻注入 01:00（早于北京当前时刻）→ 仍正确判定发送。
func TestRunTimezoneUTCInput(t *testing.T) {
	now := time.Date(2026, time.September, 30, 18, 0, 0, 0, time.UTC)
	s, fake, db := newTest(t, now, "01:00")
	insertMemo(t, db, memo.Memo{
		UserID: "u1", UserEmail: "a@t.com", Title: "国庆提醒",
		DateType: "solar", Repeat: "yearly", Month: new(10), Day: new(1),
		AdvanceOffsets: []int{0},
	})

	s.run()
	if got := fake.count(); got != 1 {
		t.Fatalf("UTC 时钟下期望发送 1 封，实际 %d 封", got)
	}
	logs := sendLogRows(t, db)
	if len(logs) != 1 || logs[0][1] != "2026-10-01" {
		t.Fatalf("send_log due_date 应为北京日期 2026-10-01，实际: %v", logs)
	}
}

// 用例⑥：now 早于发送时刻（06:00 < 08:00）→ run 不发送。
func TestRunBeforeSendTime(t *testing.T) {
	now := beijing(2026, time.May, 20, 6, 0)
	s, fake, db := newTest(t, now, "08:00")
	insertMemo(t, db, memo.Memo{
		UserID: "u1", UserEmail: "a@t.com", Title: "当天提醒",
		DateType: "solar", Repeat: "yearly", Month: new(5), Day: new(20),
		AdvanceOffsets: []int{0},
	})

	s.run()
	if got := fake.count(); got != 0 {
		t.Fatalf("早于发送时刻不应发送，实际发送 %d 封", got)
	}
	if logs := sendLogRows(t, db); len(logs) != 0 {
		t.Fatalf("send_log 应为空，实际: %v", logs)
	}
}

// 用例⑦：lunar yearly 备忘（农历2026五月初五=2026-06-19），now=2026-06-16、
// offsets [0,3] → offset 3 命中，发 1 封。
func TestRunLunarYearlyOffsetHit(t *testing.T) {
	now := beijing(2026, time.June, 16, 8, 30)
	s, fake, db := newTest(t, now, "08:00")
	insertMemo(t, db, memo.Memo{
		UserID: "u1", UserEmail: "a@t.com", Title: "端午节",
		DateType: "lunar", Repeat: "yearly", LunarMonth: new(5), LunarDay: new(5),
		AdvanceOffsets: []int{0, 3},
	})

	s.run()
	if got := fake.count(); got != 1 {
		t.Fatalf("期望发送 1 封，实际 %d 封", got)
	}
	logs := sendLogRows(t, db)
	if len(logs) != 1 || logs[0][1] != "2026-06-19" || logs[0][2] != int64(3) {
		t.Fatalf("send_log 应为 due=2026-06-19 offset=3，实际: %v", logs)
	}
}

// 补充：发送失败不回滚 send_log（避免次日轰炸）。
func TestSendFailureKeepsLog(t *testing.T) {
	now := beijing(2026, time.May, 20, 8, 0)
	s, fake, db := newTest(t, now, "08:00")
	fake.fail = true
	insertMemo(t, db, memo.Memo{
		UserID: "u1", UserEmail: "a@t.com", Title: "失败也不重发",
		DateType: "solar", Repeat: "yearly", Month: new(5), Day: new(20),
		AdvanceOffsets: []int{0},
	})

	s.run()
	if got := fake.count(); got != 1 {
		t.Fatalf("失败重试替身应恰好被调用 1 次，实际 %d 次", got)
	}
	if logs := sendLogRows(t, db); len(logs) != 1 {
		t.Fatalf("发送失败后 send_log 不应回滚，实际: %v", logs)
	}
	// 恢复发送后再次 run，因 send_log 已存在不再重发
	fake.fail = false
	s.run()
	if got := fake.count(); got != 1 {
		t.Fatalf("恢复后不应重发，实际累计 %d 封", got)
	}
}

// 补充：user_email 为空 → 跳过。
func TestSkipEmptyEmail(t *testing.T) {
	now := beijing(2026, time.May, 20, 9, 0)
	s, fake, db := newTest(t, now, "08:00")
	insertMemo(t, db, memo.Memo{
		UserID: "u1", UserEmail: "", Title: "无邮箱",
		DateType: "solar", Repeat: "yearly", Month: new(5), Day: new(20),
		AdvanceOffsets: []int{0},
	})

	s.run()
	if got := fake.count(); got != 0 {
		t.Fatalf("无邮箱不应发送，实际 %d 封", got)
	}
}

// 补充：DateLabel 农历/阳历格式。
func TestDateLabel(t *testing.T) {
	if got := DateLabel("lunar", 5, 5); got != "农历 五月初五" {
		t.Errorf("农历标签不符: %q", got)
	}
	if got := DateLabel("lunar", 12, 30); got != "农历 十二月三十" {
		t.Errorf("农历标签不符: %q", got)
	}
	if got := DateLabel("lunar", 1, 21); got != "农历 一月廿一" {
		t.Errorf("农历标签不符: %q", got)
	}
	if got := DateLabel("solar", 10, 23); got != "阳历 10月23日" {
		t.Errorf("阳历标签不符: %q", got)
	}
}

// 补充：NextOccurrence 500 天内找不到返回 false。
func TestNextOccurrence(t *testing.T) {
	m := memo.Memo{DateType: "solar", Repeat: "yearly", Month: new(2), Day: new(29)}
	// 从 2026-05-20 找 500 天：2028-02-29 在约 645 天后，500 天内找不到（2027 无 2/29）
	if _, ok := NextOccurrence(m, beijing(2026, time.May, 20, 0, 0)); ok {
		t.Error("500 天内不应找到 2月29日（2026/2027 均非闰年）")
	}
	m2 := memo.Memo{DateType: "solar", Repeat: "yearly", Month: new(5), Day: new(20)}
	got, ok := NextOccurrence(m2, beijing(2026, time.May, 17, 0, 0))
	if !ok || got.Format("2006-01-02") != "2026-05-20" {
		t.Errorf("next 应为 2026-05-20，实际 ok=%v got=%v", ok, got)
	}
}

// TestNextOccurrenceLunarLaYueCrossYear 腊月跨年回归：农历腊月落在次阳历年，
// next_date 必须能算出来（修复前 IsOccurrence 用阳历年换算导致永远找不到）。
func TestNextOccurrenceLunarLaYueCrossYear(t *testing.T) {
	ip := func(v int) *int { return &v }
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, CST) // 北京时间
	m := memo.Memo{DateType: "lunar", Repeat: "yearly", LunarMonth: ip(12), LunarDay: ip(21)}
	got, ok := NextOccurrence(m, now)
	if !ok {
		t.Fatal("腊月廿一应能在 500 天内找到下一次发生日")
	}
	back := calendar.NewSolarFromYmd(got.Year(), int(got.Month()), got.Day()).GetLunar()
	if back.GetMonth() != 12 || back.GetDay() != 21 {
		t.Errorf("下一次发生日应为腊月廿一, 得到 %s (农历 %d 月 %d 日)",
			got.Format("2006-01-02"), back.GetMonth(), back.GetDay())
	}
	if !got.After(now) {
		t.Errorf("下一次发生日 %s 应晚于 now", got.Format("2006-01-02"))
	}
}
