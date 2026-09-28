package memo

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// NextOccurrenceHook 计算备忘下一次发生时间的钩子，由农历/调度模块注入；
// 为 nil 时响应不输出 next_date 字段
var NextOccurrenceHook func(m Memo, now time.Time) (time.Time, bool)

// RegisterRoutes 在 /api 路由组上注册备忘 CRUD 接口
func RegisterRoutes(g *gin.RouterGroup, db *sql.DB) {
	g.POST("/memos", createMemo(db))
	g.GET("/memos", listMemos(db))
	g.GET("/memos/:id", getMemo(db))
	g.PUT("/memos/:id", updateMemo(db))
	g.DELETE("/memos/:id", deleteMemo(db))
}

// memoRequest 创建/更新请求体
type memoRequest struct {
	Title          string `json:"title"`
	Content        string `json:"content"`
	DateType       string `json:"date_type"`
	Repeat         string `json:"repeat"`
	Month          *int   `json:"month"`
	Day            *int   `json:"day"`
	LunarMonth     *int   `json:"lunar_month"`
	LunarDay       *int   `json:"lunar_day"`
	LunarLeap      bool   `json:"lunar_leap"`
	AdvanceOffsets []int  `json:"advance_offsets"`
	OnceDate       string `json:"once_date"`
	Enabled        *bool  `json:"enabled"`
}

// currentUID 从认证中间件注入的上下文中取用户 ID
func currentUID(c *gin.Context) (string, bool) {
	uid := c.GetString("uid")
	return uid, uid != ""
}

// fail 输出统一的错误响应
func fail(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"error": msg})
}

// unauthorized 未认证响应
func unauthorized(c *gin.Context) {
	fail(c, http.StatusUnauthorized, "unauthorized")
}

// validateMemo 校验请求并归一化为 Memo（不含 ID/用户信息），返回 400 时的中文消息
func validateMemo(req *memoRequest) (*Memo, string) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return nil, "标题不能为空"
	}
	if utf8.RuneCountInString(title) > 100 {
		return nil, "标题不能超过100字"
	}
	if utf8.RuneCountInString(req.Content) > 500 {
		return nil, "内容不能超过500字"
	}
	if req.DateType != "solar" && req.DateType != "lunar" {
		return nil, "日期类型必须为 solar 或 lunar"
	}
	switch req.Repeat {
	case "yearly", "monthly", "daily", "once":
	default:
		return nil, "重复类型必须为 yearly/monthly/daily/once"
	}
	// 提前提醒天数：非空、0-365、去重升序
	if len(req.AdvanceOffsets) == 0 {
		return nil, "提前提醒天数不能为空"
	}
	seen := make(map[int]bool, len(req.AdvanceOffsets))
	offsets := make([]int, 0, len(req.AdvanceOffsets))
	for _, o := range req.AdvanceOffsets {
		if o < 0 || o > 365 {
			return nil, "提前提醒天数必须在0-365之间"
		}
		if !seen[o] {
			seen[o] = true
			offsets = append(offsets, o)
		}
	}
	sort.Ints(offsets)

	m := &Memo{
		Title:          title,
		Content:        req.Content,
		DateType:       req.DateType,
		Repeat:         req.Repeat,
		AdvanceOffsets: offsets,
		Enabled:        true, // 默认启用
	}
	if req.Enabled != nil {
		m.Enabled = *req.Enabled
	}
	switch {
	case req.Repeat == "once": // 一次性提醒必须公历，只取 once_date
		if req.DateType != "solar" {
			return nil, "农历仅支持每年/每月重复"
		}
		if strings.TrimSpace(req.OnceDate) == "" {
			return nil, "重复类型为 once 时必须提供 once_date"
		}
		t, err := time.Parse("2006-01-02", req.OnceDate)
		if err != nil {
			return nil, "once_date 格式必须为 YYYY-MM-DD"
		}
		d := t.Format("2006-01-02")
		m.OnceDate = &d
	case req.DateType == "lunar": // 农历周期提醒
		if req.Repeat != "yearly" && req.Repeat != "monthly" {
			return nil, "农历仅支持每年/每月重复"
		}
		if req.LunarMonth == nil || *req.LunarMonth < 1 || *req.LunarMonth > 12 {
			return nil, "农历月份必须在1-12之间"
		}
		if req.LunarDay == nil || *req.LunarDay < 1 || *req.LunarDay > 30 {
			return nil, "农历日期必须在1-30之间"
		}
		m.LunarMonth = req.LunarMonth
		m.LunarDay = req.LunarDay
		m.LunarLeap = req.LunarLeap
	default: // 公历周期提醒
		if req.Month == nil || *req.Month < 1 || *req.Month > 12 {
			return nil, "月份必须在1-12之间"
		}
		if req.Day == nil || *req.Day < 1 || *req.Day > 31 {
			return nil, "日期必须在1-31之间"
		}
		m.Month = req.Month
		m.Day = req.Day
	}
	return m, ""
}

// toResponse 组装 JSON 响应；withNext 且钩子可用时附带 next_date（RFC3339 日期）
func toResponse(m *Memo, withNext bool) gin.H {
	r := gin.H{
		"id":              m.ID,
		"user_id":         m.UserID,
		"user_email":      m.UserEmail,
		"title":           m.Title,
		"content":         m.Content,
		"date_type":       m.DateType,
		"repeat":          m.Repeat,
		"month":           m.Month,
		"day":             m.Day,
		"lunar_month":     m.LunarMonth,
		"lunar_day":       m.LunarDay,
		"lunar_leap":      m.LunarLeap,
		"advance_offsets": m.AdvanceOffsets,
		"once_date":       m.OnceDate,
		"enabled":         m.Enabled,
		"created_at":      m.CreatedAt,
		"updated_at":      m.UpdatedAt,
	}
	if withNext && NextOccurrenceHook != nil {
		if t, ok := NextOccurrenceHook(*m, time.Now()); ok {
			r["next_date"] = t.Format("2006-01-02")
		}
	}
	return r
}

// createMemo POST /memos
func createMemo(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := currentUID(c)
		if !ok {
			unauthorized(c)
			return
		}
		var req memoRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, "请求参数格式错误")
			return
		}
		m, msg := validateMemo(&req)
		if msg != "" {
			fail(c, http.StatusBadRequest, msg)
			return
		}
		m.UserID = uid
		m.UserEmail = c.GetString("email")
		if err := Create(db, m); err != nil {
			fail(c, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		c.JSON(http.StatusOK, toResponse(m, false))
	}
}

// listMemos GET /memos?enabled=true|false
func listMemos(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := currentUID(c)
		if !ok {
			unauthorized(c)
			return
		}
		var enabled *bool
		if q := c.Query("enabled"); q != "" {
			b, err := strconv.ParseBool(q)
			if err != nil {
				fail(c, http.StatusBadRequest, "enabled 参数必须为 true 或 false")
				return
			}
			enabled = &b
		}
		list, err := ListByUser(db, uid, enabled)
		if err != nil {
			fail(c, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		memos := make([]gin.H, 0, len(list))
		for _, m := range list {
			memos = append(memos, toResponse(m, true))
		}
		c.JSON(http.StatusOK, gin.H{"memos": memos})
	}
}

// getMemo GET /memos/:id
func getMemo(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := currentUID(c)
		if !ok {
			unauthorized(c)
			return
		}
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "无效的备忘ID")
			return
		}
		m, err := GetByID(db, uid, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				fail(c, http.StatusNotFound, "备忘不存在")
			} else {
				fail(c, http.StatusInternalServerError, "服务器内部错误")
			}
			return
		}
		c.JSON(http.StatusOK, toResponse(m, true))
	}
}

// updateMemo PUT /memos/:id
func updateMemo(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := currentUID(c)
		if !ok {
			unauthorized(c)
			return
		}
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "无效的备忘ID")
			return
		}
		var req memoRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, "请求参数格式错误")
			return
		}
		m, msg := validateMemo(&req)
		if msg != "" {
			fail(c, http.StatusBadRequest, msg)
			return
		}
		m.ID = id
		m.UserID = uid
		m.UserEmail = c.GetString("email")
		if err := Update(db, m); err != nil {
			if errors.Is(err, ErrNotFound) {
				fail(c, http.StatusNotFound, "备忘不存在")
			} else {
				fail(c, http.StatusInternalServerError, "服务器内部错误")
			}
			return
		}
		// 回读 DB，保证响应里 created_at/updated_at 等为库中真实值
		fresh, err := GetByID(db, uid, id)
		if err != nil {
			fail(c, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		c.JSON(http.StatusOK, toResponse(fresh, false))
	}
}

// deleteMemo DELETE /memos/:id
func deleteMemo(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := currentUID(c)
		if !ok {
			unauthorized(c)
			return
		}
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "无效的备忘ID")
			return
		}
		if err := Delete(db, uid, id); err != nil {
			fail(c, http.StatusNotFound, "备忘不存在")
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}
