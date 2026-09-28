package lunarcal

import (
	"testing"
	"time"

	"github.com/6tail/lunar-go/calendar"
)

// beijing 用北京时间构造当天零点，避免依赖服务器时区。
func beijing(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))
}

// TestLunarToSolarAnchors 权威锚点：端午节（农历五月初五）换算。
func TestLunarToSolarAnchors(t *testing.T) {
	cases := []struct {
		year int
		want time.Time
	}{
		{2026, beijing(2026, time.June, 19)}, // 农历 2026 五月初五 -> 2026-06-19
		{2025, beijing(2025, time.May, 31)},  // 农历 2025 五月初五 -> 2025-05-31
	}
	for _, c := range cases {
		got, err := LunarToSolar(c.year, 5, 5, false)
		if err != nil {
			t.Fatalf("LunarToSolar(%d,5,5,false) 返回错误: %v", c.year, err)
		}
		if !got.Equal(c.want) {
			t.Errorf("农历 %d 五月初五 = %v, 期望 %v", c.year, got, c.want)
		}
	}
}

// TestLeapMonthExist 闰月存在性：2028 年有闰五月，2027 年没有。
func TestLeapMonthExist(t *testing.T) {
	if !HasLeapMonth(2028, 5) {
		t.Fatal("2028 年应有闰五月")
	}
	if HasLeapMonth(2027, 5) {
		t.Fatal("2027 年不应有闰五月")
	}
	if days := LunarMonthDays(2028, 5, true); days != 29 {
		t.Errorf("2028 闰五月应为 29 天, 得到 %d", days)
	}
}

// TestLeapMonthConvert 闰月换算：闰五月初一换算后用库反向转回农历，
// 必须是闰五月（月份为 -5）且为初一。
func TestLeapMonthConvert(t *testing.T) {
	got, err := LunarToSolar(2028, 5, 1, true)
	if err != nil {
		t.Fatalf("LunarToSolar(2028,5,1,true) 返回错误: %v", err)
	}
	back := calendar.NewSolarFromYmd(got.Year(), int(got.Month()), got.Day()).GetLunar()
	if back.GetMonth() != -5 || back.GetDay() != 1 {
		t.Errorf("2028 闰五月初一应换算为闰五月初一, 反查得到农历 %d 月 %d 日 (阳历 %s)",
			back.GetMonth(), back.GetDay(), got.Format("2006-01-02"))
	}
	// 反向锚点：已知 2028 闰五月初一为 2028-06-23，同时验证与普通五月初一不同
	normal, err := LunarToSolar(2028, 5, 1, false)
	if err != nil {
		t.Fatalf("LunarToSolar(2028,5,1,false) 返回错误: %v", err)
	}
	if normal.Equal(got) {
		t.Error("闰五月初一与普通五月初一不应是同一天")
	}
}

// TestLeapMonthFallback 闰月回落：2027 无闰五月，leap=true 应回落为普通五月。
func TestLeapMonthFallback(t *testing.T) {
	leap, err := LunarToSolar(2027, 5, 15, true)
	if err != nil {
		t.Fatalf("LunarToSolar(2027,5,15,true) 返回错误: %v", err)
	}
	normal, err := LunarToSolar(2027, 5, 15, false)
	if err != nil {
		t.Fatalf("LunarToSolar(2027,5,15,false) 返回错误: %v", err)
	}
	if !leap.Equal(normal) {
		t.Errorf("无闰月时应回落: leap=%v, normal=%v", leap, normal)
	}
	// 反查确认回落换算对应的是普通（非闰）五月十五
	back := calendar.NewSolarFromYmd(normal.Year(), int(normal.Month()), normal.Day()).GetLunar()
	if back.GetMonth() != 5 || back.GetDay() != 15 {
		t.Errorf("回落换算应为普通五月十五, 反查得到农历 %d 月 %d 日", back.GetMonth(), back.GetDay())
	}
}

// TestDay30Deferral 三十顺延：取 2026 年一个只有廿九的农历月（用库查询），
// lunarDay=30 应等于该月第 29 天的阳历。
func TestDay30Deferral(t *testing.T) {
	month := 0
	for m := 1; m <= 12; m++ {
		if LunarMonthDays(2026, m, false) == 29 {
			month = m
			break
		}
	}
	if month == 0 {
		t.Fatal("2026 年未找到只有 29 天的农历月")
	}
	got30, err := LunarToSolar(2026, month, 30, false)
	if err != nil {
		t.Fatalf("LunarToSolar(2026,%d,30,false) 返回错误: %v", month, err)
	}
	want, err := LunarToSolar(2026, month, 29, false)
	if err != nil {
		t.Fatalf("LunarToSolar(2026,%d,29,false) 返回错误: %v", month, err)
	}
	if !got30.Equal(want) {
		t.Errorf("农历 2026 年 %d 月三十应顺延到廿九: got=%v, want=%v", month, got30, want)
	}
}

// TestInvalidInput 非法参数应返回错误而非 panic。
func TestInvalidInput(t *testing.T) {
	if _, err := LunarToSolar(2026, 13, 5, false); err == nil {
		t.Error("月份 13 应返回错误")
	}
	if _, err := LunarToSolar(2026, 5, 31, false); err == nil {
		t.Error("农历日 31 应返回错误")
	}
}

// TestIsOccurrenceYearlyLunar 农历每年重复：2026-06-19 为发生日，相邻日不是。
func TestIsOccurrenceYearlyLunar(t *testing.T) {
	s := Spec{DateType: "lunar", Repeat: "yearly", LunarMonth: 5, LunarDay: 5}
	if !IsOccurrence(s, beijing(2026, time.June, 19)) {
		t.Error("2026-06-19 应为农历五月初五的发生日")
	}
	for _, d := range []time.Time{
		beijing(2026, time.June, 18),
		beijing(2026, time.June, 20),
		beijing(2027, time.June, 19), // 2027 端午不是 6-19
	} {
		if IsOccurrence(s, d) {
			t.Errorf("%s 不应为发生日", d.Format("2006-01-02"))
		}
	}
	// 2027 年五月初五换算后仍应是发生日
	got, err := LunarToSolar(2027, 5, 5, false)
	if err != nil {
		t.Fatalf("LunarToSolar(2027,5,5,false) 返回错误: %v", err)
	}
	if !IsOccurrence(s, got) {
		t.Errorf("2027 农历五月初五对应阳历 %s 应为发生日", got.Format("2006-01-02"))
	}
}

// TestIsOccurrenceYearlySolar 阳历每年重复。
func TestIsOccurrenceYearlySolar(t *testing.T) {
	s := Spec{DateType: "solar", Repeat: "yearly", Month: 12, Day: 25}
	if !IsOccurrence(s, beijing(2026, time.December, 25)) {
		t.Error("2026-12-25 应为发生日")
	}
	if IsOccurrence(s, beijing(2026, time.December, 24)) {
		t.Error("2026-12-24 不应为发生日")
	}
}

// TestIsOccurrenceMonthlyLunar 农历每月重复：每个农历月的初五都算发生日。
func TestIsOccurrenceMonthlyLunar(t *testing.T) {
	s := Spec{DateType: "lunar", Repeat: "monthly", LunarMonth: 5, LunarDay: 5}
	// 2026-06-19 是五月初五
	if !IsOccurrence(s, beijing(2026, time.June, 19)) {
		t.Error("2026-06-19 (五月初五) 应为发生日")
	}
	// 2026-06-18 是四月初四... 相邻日按农历日不同不算
	if IsOccurrence(s, beijing(2026, time.June, 18)) {
		t.Error("2026-06-18 不应为发生日")
	}
	// 抽查 2026-07-14：应为六月初一，不是初五
	d := beijing(2026, time.July, 14)
	l := calendar.NewSolarFromYmd(d.Year(), int(d.Month()), d.Day()).GetLunar()
	if l.GetDay() == 5 {
		t.Fatalf("前置条件失败: 2026-07-14 是农历初%d", l.GetDay())
	}
	if IsOccurrence(s, d) {
		t.Errorf("2026-07-14 (农历初%d) 不应为发生日", l.GetDay())
	}
}

// TestIsOccurrenceMonthlySolar 阳历每月重复。
func TestIsOccurrenceMonthlySolar(t *testing.T) {
	s := Spec{DateType: "solar", Repeat: "monthly", Day: 15}
	if !IsOccurrence(s, beijing(2026, time.March, 15)) {
		t.Error("3 月 15 日应为发生日")
	}
	if IsOccurrence(s, beijing(2026, time.March, 16)) {
		t.Error("3 月 16 日不应为发生日")
	}
}

// TestIsOccurrenceLunarLaYueCrossYear 腊月跨年回归：农历腊月属于次阳历年，
// 发生日判定必须用「日期所属农历年」换算（修复前用阳历年导致腊月永不匹配）。
func TestIsOccurrenceLunarLaYueCrossYear(t *testing.T) {
	s := Spec{DateType: "lunar", Repeat: "yearly", LunarMonth: 12, LunarDay: 21}
	for _, ly := range []int{2025, 2026, 2027} {
		got, err := LunarToSolar(ly, 12, 21, false)
		if err != nil {
			t.Fatalf("LunarToSolar(%d,12,21,false) 返回错误: %v", ly, err)
		}
		// 锚点自检：换算结果反查必须仍是该农历年的腊月廿一
		back := calendar.NewSolarFromYmd(got.Year(), int(got.Month()), got.Day()).GetLunar()
		if back.GetYear() != ly || back.GetMonth() != 12 || back.GetDay() != 21 {
			t.Fatalf("换算锚点错误: 农历 %d 腊月廿一 应为 %s, 反查得农历 %d 年 %d 月 %d 日",
				ly, got.Format("2006-01-02"), back.GetYear(), back.GetMonth(), back.GetDay())
		}
		if !IsOccurrence(s, got) {
			t.Errorf("%s（农历 %d 腊月廿一）应为发生日", got.Format("2006-01-02"), ly)
		}
		if IsOccurrence(s, got.AddDate(0, 0, 1)) {
			t.Errorf("%s 不应为发生日", got.AddDate(0, 0, 1).Format("2006-01-02"))
		}
	}
}

// TestIsOccurrenceMonthlyLunarDay30Deferral 农历每月+三十：小月（29 天）的
// 月末当天视为发生日（spec 规定的三十顺延）。
func TestIsOccurrenceMonthlyLunarDay30Deferral(t *testing.T) {
	s := Spec{DateType: "lunar", Repeat: "monthly", LunarDay: 30}
	// 2026 年找一个只有 29 天的农历月
	small := 0
	for m := 1; m <= 12; m++ {
		if LunarMonthDays(2026, m, false) == 29 {
			small = m
			break
		}
	}
	if small == 0 {
		t.Fatal("2026 年未找到只有 29 天的农历月")
	}
	// 小月的第 29 天（即月末）应是「三十」的发生日
	lastDay, err := LunarToSolar(2026, small, 29, false)
	if err != nil {
		t.Fatalf("LunarToSolar(2026,%d,29,false) 返回错误: %v", small, err)
	}
	if !IsOccurrence(s, lastDay) {
		t.Errorf("%s（农历 %d 月廿九/月末）应为三十顺延的发生日", lastDay.Format("2006-01-02"), small)
	}
	// 大月（30 天）的第 29 天不应是发生日
	for m := 1; m <= 12; m++ {
		if LunarMonthDays(2026, m, false) != 30 {
			continue
		}
		day29, err := LunarToSolar(2026, m, 29, false)
		if err != nil {
			t.Fatalf("LunarToSolar(2026,%d,29,false) 返回错误: %v", m, err)
		}
		if IsOccurrence(s, day29) {
			t.Errorf("%s（农历 %d 月廿九）不应为发生日，大月三十应为月末", day29.Format("2006-01-02"), m)
		}
		break
	}
}

// TestIsOccurrenceOnceAndDaily 单次精确匹配与每日恒真。
func TestIsOccurrenceOnceAndDaily(t *testing.T) {
	once := Spec{Repeat: "once", OnceDate: "2026-06-19"}
	if !IsOccurrence(once, beijing(2026, time.June, 19)) {
		t.Error("once 应精确匹配 2026-06-19")
	}
	if IsOccurrence(once, beijing(2026, time.June, 18)) {
		t.Error("once 相邻日不应匹配")
	}
	if IsOccurrence(once, beijing(2027, time.June, 19)) {
		t.Error("once 跨年不应匹配")
	}
	badOnce := Spec{Repeat: "once", OnceDate: "not-a-date"}
	if IsOccurrence(badOnce, beijing(2026, time.June, 19)) {
		t.Error("非法 OnceDate 不应匹配")
	}

	daily := Spec{Repeat: "daily"}
	for _, d := range []time.Time{
		beijing(2026, time.June, 19),
		beijing(2027, time.January, 1),
		beijing(2025, time.February, 28),
	} {
		if !IsOccurrence(daily, d) {
			t.Errorf("daily 在 %s 应恒为发生日", d.Format("2006-01-02"))
		}
	}
}
