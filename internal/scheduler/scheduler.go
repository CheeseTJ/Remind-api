// Package scheduler 实现每日定时提醒调度：
// 每天北京时间 DailySendTime 触发一次，扫描启用中的备忘，
// 按「今天(北京)+提前天数」是否为发生日判定当天应发送的提醒，
// 并用 send_log 表的 UNIQUE 约束做幂等去重（重启/并发均不会重发）。
package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"
	_ "time/tzdata" // 内嵌时区数据，保证单二进制在任何环境可加载 Asia/Shanghai

	"remind/internal/lunarcal"
	"remind/internal/mailer"
	"remind/internal/memo"
)

// CST 北京时间（东八区）。优先加载系统 tzdata，失败时回退为固定东八区。
var CST = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}()

// ChineseMonths 农历月中文数字（1-12），导出供他处复用。
var ChineseMonths = []string{"", "一", "二", "三", "四", "五", "六", "七", "八", "九", "十", "十一", "十二"}

// LunarDayNames 农历日中文名（下标 1-30），初X/十X/廿X/三十 格式。
var LunarDayNames = map[int]string{
	1: "初一", 2: "初二", 3: "初三", 4: "初四", 5: "初五",
	6: "初六", 7: "初七", 8: "初八", 9: "初九", 10: "初十",
	11: "十一", 12: "十二", 13: "十三", 14: "十四", 15: "十五",
	16: "十六", 17: "十七", 18: "十八", 19: "十九", 20: "二十",
	21: "廿一", 22: "廿二", 23: "廿三", 24: "廿四", 25: "廿五",
	26: "廿六", 27: "廿七", 28: "廿八", 29: "廿九", 30: "三十",
}

// RepeatLabels 重复方式中文文案。
var RepeatLabels = map[string]string{
	"yearly": "每年", "monthly": "每月", "daily": "每日", "once": "单次",
}

// DateLabel 按日期类型生成邮件中的日期标注：
// lunar → 「农历 五月初五」，solar → 「阳历 5月20日」；其他类型返回空串。
func DateLabel(dateType string, month, day int) string {
	switch dateType {
	case "lunar":
		mn := ""
		if month >= 1 && month < len(ChineseMonths) {
			mn = ChineseMonths[month]
		}
		return fmt.Sprintf("农历 %s%s", mn+"月", LunarDayNames[day])
	case "solar":
		return fmt.Sprintf("阳历 %d月%d日", month, day)
	}
	return ""
}

// maxLookAheadDays 计算下一次发生日时最多向后查找的天数。
const maxLookAheadDays = 500

// SpecOf 把 Memo 字段映射为 lunarcal.Spec。
func SpecOf(m memo.Memo) lunarcal.Spec {
	spec := lunarcal.Spec{DateType: m.DateType, Repeat: m.Repeat}
	if m.Month != nil {
		spec.Month = *m.Month
	}
	if m.Day != nil {
		spec.Day = *m.Day
	}
	if m.LunarMonth != nil {
		spec.LunarMonth = *m.LunarMonth
	}
	if m.LunarDay != nil {
		spec.LunarDay = *m.LunarDay
	}
	spec.LunarLeap = m.LunarLeap
	if m.OnceDate != nil {
		spec.OnceDate = *m.OnceDate
	}
	return spec
}

// NextOccurrence 从 now（北京时间）起逐日向后找最多 500 天，
// 返回第一个发生日；找不到返回 ok=false。供 memo 列表展示 next_date。
func NextOccurrence(m memo.Memo, now time.Time) (time.Time, bool) {
	spec := SpecOf(m)
	start := now.In(CST)
	y, mo, d := start.Date()
	start = time.Date(y, mo, d, 0, 0, 0, 0, CST)
	for i := range maxLookAheadDays {
		cand := start.AddDate(0, 0, i)
		if lunarcal.IsOccurrence(spec, cand) {
			return cand, true
		}
	}
	return time.Time{}, false
}

// Scheduler 每日调度器。
type Scheduler struct {
	db       *sql.DB
	mail     *mailer.Mailer
	sendTime string                               // 每日发送时刻 "HH:MM"（北京时间）
	now      func() time.Time                     // 可注入时钟，默认 time.Now
	send     func(to, subject, body string) error // 可注入发送函数，默认 mail.SendWithRetry
	logger   *log.Logger
}

// New 创建调度器；sendTime 为北京时间的 "HH:MM"。
func New(db *sql.DB, mail *mailer.Mailer, sendTime string) *Scheduler {
	s := &Scheduler{
		db:       db,
		mail:     mail,
		sendTime: sendTime,
		now:      time.Now,
		logger:   log.Default(),
	}
	s.send = mail.SendWithRetry
	return s
}

// Start 启动后台调度协程，ctx 取消时退出。
func (s *Scheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

// loop 先立即补跑一次，然后睡到下一个发送时刻并循环。
func (s *Scheduler) loop(ctx context.Context) {
	s.run()
	for {
		next := s.nextSendTime(s.now())
		s.logger.Printf("调度器已启动，下次发送时刻: %s", next.Format("2006-01-02 15:04 (MST)"))
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			s.logger.Printf("调度器已停止")
			return
		case <-timer.C:
			s.run()
		}
	}
}

// parseSendTime 解析 "HH:MM"，非法时回退默认 08:00。
func parseSendTime(s string) (hour, minute int) {
	var h, m int
	if n, _ := fmt.Sscanf(s, "%d:%d", &h, &m); n == 2 && h >= 0 && h <= 23 && m >= 0 && m <= 59 {
		return h, m
	}
	return 8, 0
}

// sendTimeToday 返回指定时刻对应「今天」的北京发送时刻。
func (s *Scheduler) sendTimeToday(nowCST time.Time) time.Time {
	h, m := parseSendTime(s.sendTime)
	return time.Date(nowCST.Year(), nowCST.Month(), nowCST.Day(), h, m, 0, 0, CST)
}

// nextSendTime 计算下一个发送时刻：今天的发送时刻已过则明天。
func (s *Scheduler) nextSendTime(now time.Time) time.Time {
	nowCST := now.In(CST)
	today := s.sendTimeToday(nowCST)
	if nowCST.Before(today) {
		return today
	}
	return today.AddDate(0, 0, 1)
}

// pendingMemo 当天待发送的一条提醒（备忘 + 命中的提前天数）。
type pendingMemo struct {
	m       *memo.Memo
	dueDate string // 发生日 "2006-01-02"
	offset  int    // 提前天数
}

// run 执行一轮发送：已过当天发送时刻才扫描，逐条判定并去重发送。
func (s *Scheduler) run() {
	nowCST := s.now().In(CST)
	if nowCST.Before(s.sendTimeToday(nowCST)) {
		return // 还没到今天的发送时刻，不发送
	}

	memos, err := s.loadEnabledMemos()
	if err != nil {
		s.logger.Printf("调度器查询备忘失败: %v", err)
		return
	}

	today := nowCST.Format("2006-01-02")
	todayDate, _ := time.ParseInLocation("2006-01-02", today, CST)
	for _, m := range memos {
		if m.UserEmail == "" {
			s.logger.Printf("备忘 #%d(%s) 无收件邮箱，跳过", m.ID, m.Title)
			continue
		}
		for _, o := range m.AdvanceOffsets {
			// 核心判定：北京「今天」+ offset 天是否为发生日
			candidate := todayDate.AddDate(0, 0, o)
			if !lunarcal.IsOccurrence(SpecOf(*m), candidate) {
				continue
			}
			if err := s.deliver(m, candidate, o, nowCST); err != nil {
				s.logger.Printf("备忘 #%d(%s) 发送失败(offset=%d): %v", m.ID, m.Title, o, err)
			}
		}
	}
}

// deliver 去重登记并发送单条提醒；返回 nil 表示已发送成功或已发送过。
func (s *Scheduler) deliver(m *memo.Memo, candidate time.Time, offset int, nowCST time.Time) error {
	dueDate := candidate.Format("2006-01-02")
	// INSERT OR IGNORE：首次插入影响 1 行才发送，重复（已存在）为 0 行直接跳过，
	// 依赖 UNIQUE(memo_id, due_date, offset_days) 天然去重，重启/并发安全。
	res, err := s.db.Exec(`INSERT OR IGNORE INTO send_log(memo_id,user_id,due_date,offset_days,sent_at) VALUES(?,?,?,?,?)`,
		m.ID, m.UserID, dueDate, offset, nowCST.Format("2006-01-02 15:04:05"))
	if err != nil {
		return fmt.Errorf("记录发送日志失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取发送日志影响行数失败: %w", err)
	}
	if n == 0 {
		return nil // 已发送过，去重跳过
	}

	info := mailer.ReminderInfo{
		Title:          m.Title,
		Content:        m.Content,
		OccurrenceDate: dueDate,
		DateLabel:      DateLabel(m.DateType, labelMonth(m), labelDay(m)),
		OffsetDays:     offset,
		RepeatLabel:    RepeatLabels[m.Repeat],
	}
	subject, body := mailer.BuildReminderEmail(info)
	if err := s.send(m.UserEmail, subject, body); err != nil {
		return fmt.Errorf("发送邮件到 %s 失败: %w", m.UserEmail, err) // 不回滚 send_log，避免次日轰炸
	}
	s.logger.Printf("已发送提醒: memo=#%d %s -> %s (due=%s offset=%d)", m.ID, m.Title, m.UserEmail, dueDate, offset)
	return nil
}

// labelMonth/labelDay 取构造 DateLabel 所需的月/日。
func labelMonth(m *memo.Memo) int {
	if m.DateType == "lunar" {
		if m.LunarMonth != nil {
			return *m.LunarMonth
		}
		return 0
	}
	if m.Month != nil {
		return *m.Month
	}
	return 0
}

func labelDay(m *memo.Memo) int {
	if m.DateType == "lunar" {
		if m.LunarDay != nil {
			return *m.LunarDay
		}
		return 0
	}
	if m.Day != nil {
		return *m.Day
	}
	return 0
}

// loadEnabledMemos 查询全部启用中的备忘。
func (s *Scheduler) loadEnabledMemos() ([]*memo.Memo, error) {
	rows, err := s.db.Query(`SELECT id,user_id,user_email,title,content,date_type,repeat,
  month,day,lunar_month,lunar_day,lunar_leap,advance_offsets,once_date
  FROM memos WHERE enabled=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*memo.Memo
	for rows.Next() {
		var m memo.Memo
		var month, day, lunarMonth, lunarDay sql.NullInt64
		var leap int
		var offsetsJSON string
		var onceDate sql.NullString
		if err := rows.Scan(&m.ID, &m.UserID, &m.UserEmail, &m.Title, &m.Content,
			&m.DateType, &m.Repeat, &month, &day, &lunarMonth, &lunarDay, &leap,
			&offsetsJSON, &onceDate); err != nil {
			return nil, err
		}
		intPtr := func(n sql.NullInt64) *int {
			if !n.Valid {
				return nil
			}
			v := int(n.Int64)
			return &v
		}
		m.Month = intPtr(month)
		m.Day = intPtr(day)
		m.LunarMonth = intPtr(lunarMonth)
		m.LunarDay = intPtr(lunarDay)
		m.LunarLeap = leap != 0
		if onceDate.Valid {
			v := onceDate.String
			m.OnceDate = &v
		}
		if err := json.Unmarshal([]byte(offsetsJSON), &m.AdvanceOffsets); err != nil {
			m.AdvanceOffsets = []int{}
		}
		list = append(list, &m)
	}
	return list, rows.Err()
}
