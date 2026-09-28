// Package lunarcal 提供农历→阳历换算与备忘「发生日」判定原语，
// 供调度器判断某个日期是否为某条备忘的发生日。
package lunarcal

import (
	"fmt"
	"time"

	"github.com/6tail/lunar-go/calendar"
)

// cst 北京时间（东八区），测试与换算统一使用，避免依赖服务器时区。
var cst = time.FixedZone("CST", 8*3600)

// Spec 描述一条备忘的日期规则。
type Spec struct {
	DateType   string // "solar" | "lunar"
	Repeat     string // "yearly" | "monthly" | "daily" | "once"
	Month      int    // 阳历月（DateType 为 solar 时使用）
	Day        int    // 阳历日（DateType 为 solar 时使用）
	LunarMonth int    // 农历月 1-12（DateType 为 lunar 时使用）
	LunarDay   int    // 农历日 1-30（DateType 为 lunar 时使用）
	LunarLeap  bool   // 是否闰月（DateType 为 lunar 时使用）
	OnceDate   string // 单次日期，格式 "2006-01-02"（Repeat 为 once 时使用）
}

// HasLeapMonth 判断指定农历年是否存在指定的闰月。
// lunar-go 约定：闰月用负数月份表达（如 -5 表示闰五月），
// LunarYear.GetLeapMonth() 返回该年的闰月数字（无闰月返回 0）。
func HasLeapMonth(year, month int) bool {
	if month < 1 || month > 12 {
		return false
	}
	return calendar.NewLunarYear(year).GetLeapMonth() == month
}

// LunarMonthDays 返回指定农历月的天数（29 或 30）。
// leap 为 true 时查询闰月；该月不存在（如该年没有此闰月）时返回 0。
func LunarMonthDays(year, lunarMonth int, leap bool) int {
	month := lunarMonth
	if leap {
		month = -month // lunar-go 用负数表示闰月
	}
	lm := calendar.NewLunarMonthFromYm(year, month)
	if lm == nil {
		return 0
	}
	return lm.GetDayCount()
}

// LunarToSolar 把农历日期换算为阳历日期（北京时间零点）。
// 规则：
//   - 闰月回落：leap=true 但该年没有此闰月时，回落为普通同数字月份；
//   - 三十顺延：该农历月没有三十（只有廿九）时，取该月最后一天；
//   - 参数非法或无对应日期时返回 error。
func LunarToSolar(year, lunarMonth, lunarDay int, leap bool) (time.Time, error) {
	if year < 1 {
		return time.Time{}, fmt.Errorf("农历年份非法: %d", year)
	}
	if lunarMonth < 1 || lunarMonth > 12 {
		return time.Time{}, fmt.Errorf("农历月份非法: %d", lunarMonth)
	}
	if lunarDay < 1 || lunarDay > 30 {
		return time.Time{}, fmt.Errorf("农历日非法: %d", lunarDay)
	}
	// 闰月处理：存在该闰月则用负数月份表达，否则回落为普通月份
	month := lunarMonth
	if leap && HasLeapMonth(year, lunarMonth) {
		month = -month
	}
	days := LunarMonthDays(year, lunarMonth, month < 0)
	if days <= 0 {
		return time.Time{}, fmt.Errorf("农历 %d 年 %d 月不存在", year, month)
	}
	// 三十顺延：月内没有三十时取该月最后一天
	day := min(lunarDay, days)
	// 前面已校验月份存在、日不超天数，NewLunarFromYmd 不会再 panic
	l := calendar.NewLunarFromYmd(year, month, day)
	s := l.GetSolar()
	return time.Date(s.GetYear(), time.Month(s.GetMonth()), s.GetDay(), 0, 0, 0, 0, cst), nil
}

// IsOccurrence 判断 date（应为北京时区的当天，函数内只比较年月日）
// 是否为按 spec 规则描述的备忘的发生日。
func IsOccurrence(s Spec, date time.Time) bool {
	y, m, d := date.Date()
	switch s.Repeat {
	case "daily": // 每日重复：恒为发生日
		return true
	case "once": // 单次：与 OnceDate 的年月日相等
		od, err := time.ParseInLocation("2006-01-02", s.OnceDate, cst)
		if err != nil {
			return false
		}
		oy, om, odd := od.Date()
		return oy == y && om == m && odd == d
	case "yearly": // 每年重复
		switch s.DateType {
		case "solar": // 阳历月日相等
			return int(m) == s.Month && d == s.Day
		case "lunar": // 用 date 所属农历年换算成阳历再比。
			// 注意腊月（农历十二月）落在次阳历年 1-2 月，
			// 必须取 date 所属的农历年，不能用阳历年。
			ly := calendar.NewSolarFromYmd(y, int(m), d).GetLunar().GetYear()
			t, err := LunarToSolar(ly, s.LunarMonth, s.LunarDay, s.LunarLeap)
			if err != nil {
				return false
			}
			return sameYMD(t, date)
		}
	case "monthly": // 每月重复
		switch s.DateType {
		case "solar": // 阳历日相等（2月30这类不存在日由校验层挡掉）
			return d == s.Day
		case "lunar":
			// 用 date 的年月把 date 换算成农历，按农历日比较：
			// 即每个农历月的第 LunarDay 天都算发生日。
			l := calendar.NewSolarFromYmd(y, int(m), d).GetLunar()
			if l.GetDay() == s.LunarDay {
				return true
			}
			// 三十顺延：本月没有 LunarDay 这一天（如只有廿九）时，取当月最后一天
			lm := calendar.NewLunarMonthFromYm(l.GetYear(), l.GetMonth())
			return lm != nil && s.LunarDay > lm.GetDayCount() && l.GetDay() == lm.GetDayCount()
		}
	}
	return false
}

// sameYMD 比较两个时间的年月日是否相等（忽略时分秒与时区）。
func sameYMD(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
