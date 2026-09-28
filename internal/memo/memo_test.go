package memo

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"remind/internal/store"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newTestRouter 构建带假认证中间件的测试路由；defaultUser 为空则不注入 uid
func newTestRouter(t *testing.T, defaultUser string) (*gin.Engine, *sql.DB) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	r := gin.New()
	r.Use(func(c *gin.Context) {
		user := c.GetHeader("X-Test-User")
		if user == "" {
			user = defaultUser
		}
		if user != "" {
			c.Set("uid", user)
			c.Set("email", user+"@test.com")
		}
		c.Next()
	})
	RegisterRoutes(r.Group("/api"), db)
	return r, db
}

// doJSON 发送 JSON 请求并返回响应记录器
func doJSON(t *testing.T, r http.Handler, method, path, user string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// decodeBody 解析响应 JSON 为 map
func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, w.Body.String())
	}
	return out
}

// solarYearlyReq 公历每年重复的完整请求体
var solarYearlyReq = map[string]any{
	"title":           "妈妈生日",
	"content":         "记得买蛋糕",
	"date_type":       "solar",
	"repeat":          "yearly",
	"month":           5,
	"day":             20,
	"advance_offsets": []int{3, 1, 0},
}

// mustCreate 创建备忘成功并返回响应
func mustCreate(t *testing.T, r *gin.Engine, user string, body map[string]any) map[string]any {
	t.Helper()
	w := doJSON(t, r, http.MethodPost, "/api/memos", user, body)
	if w.Code != http.StatusOK {
		t.Fatalf("创建备忘失败: code=%d body=%s", w.Code, w.Body.String())
	}
	return decodeBody(t, w)
}

// memoPath 备忘详情路径
func memoPath(id int64) string { return fmt.Sprintf("/api/memos/%d", id) }

var timeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`)

// TestCreateSolarYearly 公历每年重复：创建成功且全字段回读一致
func TestCreateSolarYearly(t *testing.T) {
	r, _ := newTestRouter(t, "user-a")
	resp := mustCreate(t, r, "", solarYearlyReq)

	id, ok := resp["id"].(float64)
	if !ok || id <= 0 {
		t.Fatalf("响应缺少合法 id: %s", resp["id"])
	}
	if resp["title"] != "妈妈生日" || resp["content"] != "记得买蛋糕" {
		t.Fatalf("标题/内容不一致: %v", resp)
	}
	if resp["user_id"] != "user-a" || resp["user_email"] != "user-a@test.com" {
		t.Fatalf("用户信息不一致: %v", resp)
	}
	if resp["date_type"] != "solar" || resp["repeat"] != "yearly" {
		t.Fatalf("日期类型/重复类型不一致: %v", resp)
	}
	if resp["month"] != float64(5) || resp["day"] != float64(20) {
		t.Fatalf("月/日不一致: %v", resp)
	}
	// offsets 应去重升序
	want := []any{float64(0), float64(1), float64(3)}
	if !reflect.DeepEqual(resp["advance_offsets"], want) {
		t.Fatalf("advance_offsets 应为 %v, 实际 %v", want, resp["advance_offsets"])
	}
	if resp["enabled"] != true {
		t.Fatalf("enabled 应默认为 true: %v", resp["enabled"])
	}
	if !timeRe.MatchString(fmt.Sprint(resp["created_at"])) ||
		!timeRe.MatchString(fmt.Sprint(resp["updated_at"])) {
		t.Fatalf("时间格式应统一为 2006-01-02 15:04:05: %v %v", resp["created_at"], resp["updated_at"])
	}
	if resp["once_date"] != nil || resp["lunar_month"] != nil || resp["lunar_day"] != nil {
		t.Fatalf("无关字段应为 null: %v", resp)
	}

	// GET 回读，应与创建响应完全一致
	w := doJSON(t, r, http.MethodGet, memoPath(int64(id)), "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("回读失败: code=%d body=%s", w.Code, w.Body.String())
	}
	if got := decodeBody(t, w); !reflect.DeepEqual(got, resp) {
		t.Fatalf("回读与创建响应不一致:\n创建: %v\n回读: %v", resp, got)
	}
}

// TestCreateLunarYearly 农历每年重复创建成功
func TestCreateLunarYearly(t *testing.T) {
	r, _ := newTestRouter(t, "user-a")
	body := map[string]any{
		"title":           "中秋节",
		"date_type":       "lunar",
		"repeat":          "yearly",
		"lunar_month":     8,
		"lunar_day":       15,
		"lunar_leap":      true,
		"advance_offsets": []int{1},
	}
	resp := mustCreate(t, r, "", body)
	if resp["date_type"] != "lunar" || resp["repeat"] != "yearly" {
		t.Fatalf("农历字段不一致: %v", resp)
	}
	if resp["lunar_month"] != float64(8) || resp["lunar_day"] != float64(15) || resp["lunar_leap"] != true {
		t.Fatalf("农历月日不一致: %v", resp)
	}
	if resp["month"] != nil || resp["day"] != nil || resp["once_date"] != nil {
		t.Fatalf("公历字段应为 null: %v", resp)
	}
}

// TestCreateValidation 各类非法输入返回 400
func TestCreateValidation(t *testing.T) {
	r, _ := newTestRouter(t, "user-a")
	cases := []struct {
		name string
		body map[string]any
	}{
		{"lunar+daily", map[string]any{
			"title": "x", "date_type": "lunar", "repeat": "daily",
			"lunar_month": 1, "lunar_day": 1, "advance_offsets": []int{1},
		}},
		{"once缺once_date", map[string]any{
			"title": "x", "date_type": "solar", "repeat": "once",
			"advance_offsets": []int{1},
		}},
		{"once_date非法格式", map[string]any{
			"title": "x", "date_type": "solar", "repeat": "once",
			"once_date": "2026/01/01", "advance_offsets": []int{1},
		}},
		{"月=13", map[string]any{
			"title": "x", "date_type": "solar", "repeat": "yearly",
			"month": 13, "day": 5, "advance_offsets": []int{1},
		}},
		{"advance_offsets空", map[string]any{
			"title": "x", "date_type": "solar", "repeat": "yearly",
			"month": 1, "day": 1, "advance_offsets": []int{},
		}},
		{"offsets含负数", map[string]any{
			"title": "x", "date_type": "solar", "repeat": "yearly",
			"month": 1, "day": 1, "advance_offsets": []int{-1},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doJSON(t, r, http.MethodPost, "/api/memos", "", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("期望 400, 实际 %d body=%s", w.Code, w.Body.String())
			}
			resp := decodeBody(t, w)
			if msg, ok := resp["error"].(string); !ok || msg == "" {
				t.Fatalf("应返回中文 error 消息: %s", w.Body.String())
			}
		})
	}
}

// TestListOnlyOwnAndOrder 列表只返回本用户数据且按更新时间倒序
func TestListOnlyOwnAndOrder(t *testing.T) {
	r, _ := newTestRouter(t, "user-a")
	createMemo := func(title string) {
		t.Helper()
		body := map[string]any{
			"title": title, "date_type": "solar", "repeat": "yearly",
			"month": 1, "day": 1, "advance_offsets": []int{1},
		}
		mustCreate(t, r, "", body)
	}
	createMemo("第一条")
	createMemo("第二条")
	mustCreate(t, r, "user-b", map[string]any{
		"title": "别人家的", "date_type": "solar", "repeat": "yearly",
		"month": 2, "day": 2, "advance_offsets": []int{2},
	})

	w := doJSON(t, r, http.MethodGet, "/api/memos", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("列表失败: code=%d body=%s", w.Code, w.Body.String())
	}
	resp := decodeBody(t, w)
	memos, ok := resp["memos"].([]any)
	if !ok {
		t.Fatalf("应返回 memos 数组: %s", w.Body.String())
	}
	if len(memos) != 2 {
		t.Fatalf("user-a 应只有 2 条, 实际 %d", len(memos))
	}
	// 倒序：后创建的在前
	if memos[0].(map[string]any)["title"] != "第二条" || memos[1].(map[string]any)["title"] != "第一条" {
		t.Fatalf("应按 updated_at 倒序: %v", memos)
	}
	for _, m := range memos {
		if m.(map[string]any)["user_id"] != "user-a" {
			t.Fatalf("列表不应包含其他用户数据: %v", m)
		}
	}

	// user-b 只能看到自己的
	w2 := doJSON(t, r, http.MethodGet, "/api/memos", "user-b", nil)
	memos2 := decodeBody(t, w2)["memos"].([]any)
	if len(memos2) != 1 || memos2[0].(map[string]any)["user_id"] != "user-b" {
		t.Fatalf("user-b 列表隔离失败: %v", memos2)
	}

	// enabled=false 过滤
	w3 := doJSON(t, r, http.MethodGet, "/api/memos?enabled=true", "", nil)
	if len(decodeBody(t, w3)["memos"].([]any)) != 2 {
		t.Fatal("enabled=true 过滤失败")
	}
}

// TestCrossUserAccess404 user-b 访问 user-a 的备忘一律 404
func TestCrossUserAccess404(t *testing.T) {
	r, _ := newTestRouter(t, "user-a")
	resp := mustCreate(t, r, "", solarYearlyReq)
	id := int64(resp["id"].(float64))
	path := memoPath(id)

	if w := doJSON(t, r, http.MethodGet, path, "user-b", nil); w.Code != http.StatusNotFound {
		t.Fatalf("user-b GET 应 404, 实际 %d", w.Code)
	}
	upd := map[string]any{
		"title": "劫持", "date_type": "solar", "repeat": "yearly",
		"month": 6, "day": 6, "advance_offsets": []int{1},
	}
	if w := doJSON(t, r, http.MethodPut, path, "user-b", upd); w.Code != http.StatusNotFound {
		t.Fatalf("user-b PUT 应 404, 实际 %d", w.Code)
	}
	if w := doJSON(t, r, http.MethodDelete, path, "user-b", nil); w.Code != http.StatusNotFound {
		t.Fatalf("user-b DELETE 应 404, 实际 %d", w.Code)
	}
	// 属主访问不受影响
	if w := doJSON(t, r, http.MethodGet, path, "user-a", nil); w.Code != http.StatusOK {
		t.Fatalf("user-a GET 应 200, 实际 %d", w.Code)
	}
}

// TestUpdateEnabledTitle 更新 enabled 与标题生效
func TestUpdateEnabledTitle(t *testing.T) {
	r, _ := newTestRouter(t, "user-a")
	resp := mustCreate(t, r, "", solarYearlyReq)
	id := int64(resp["id"].(float64))

	upd := map[string]any{
		"title":           "爸爸生日",
		"content":         "记得打电话",
		"date_type":       "solar",
		"repeat":          "yearly",
		"month":           7,
		"day":             8,
		"advance_offsets": []int{7},
		"enabled":         false,
	}
	w := doJSON(t, r, http.MethodPut, memoPath(id), "", upd)
	if w.Code != http.StatusOK {
		t.Fatalf("更新失败: code=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeBody(t, w)
	if got["title"] != "爸爸生日" || got["enabled"] != false || got["month"] != float64(7) {
		t.Fatalf("更新结果不一致: %v", got)
	}
	if !reflect.DeepEqual(got["advance_offsets"], []any{float64(7)}) {
		t.Fatalf("更新后 offsets 不一致: %v", got["advance_offsets"])
	}

	// 回读确认
	w2 := doJSON(t, r, http.MethodGet, memoPath(id), "", nil)
	got2 := decodeBody(t, w2)
	if got2["title"] != "爸爸生日" || got2["enabled"] != false {
		t.Fatalf("回读不一致: %v", got2)
	}
}

// TestDeleteThenGet404 删除后查询 404
func TestDeleteThenGet404(t *testing.T) {
	r, _ := newTestRouter(t, "user-a")
	resp := mustCreate(t, r, "", solarYearlyReq)
	id := int64(resp["id"].(float64))
	path := memoPath(id)

	w := doJSON(t, r, http.MethodDelete, path, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("删除失败: code=%d body=%s", w.Code, w.Body.String())
	}
	if w2 := doJSON(t, r, http.MethodGet, path, "", nil); w2.Code != http.StatusNotFound {
		t.Fatalf("删除后 GET 应 404, 实际 %d", w2.Code)
	}
	// 重复删除同样 404
	if w3 := doJSON(t, r, http.MethodDelete, path, "", nil); w3.Code != http.StatusNotFound {
		t.Fatalf("重复删除应 404, 实际 %d", w3.Code)
	}
}

// TestNextOccurrenceHook 注入钩子后响应包含 next_date，未注入则不输出
func TestNextOccurrenceHook(t *testing.T) {
	r, _ := newTestRouter(t, "user-a")
	resp := mustCreate(t, r, "", solarYearlyReq)
	id := int64(resp["id"].(float64))

	if _, ok := resp["next_date"]; ok {
		t.Fatal("未注入 hook 时不应输出 next_date")
	}
	NextOccurrenceHook = func(m Memo, now time.Time) (time.Time, bool) {
		return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), true
	}
	defer func() { NextOccurrenceHook = nil }()

	w := doJSON(t, r, http.MethodGet, memoPath(id), "", nil)
	if got := decodeBody(t, w)["next_date"]; got != "2026-10-01" {
		t.Fatalf("GET 响应应含 next_date=2026-10-01, 实际 %v", got)
	}

	w2 := doJSON(t, r, http.MethodGet, "/api/memos", "", nil)
	memos := decodeBody(t, w2)["memos"].([]any)
	if memos[0].(map[string]any)["next_date"] != "2026-10-01" {
		t.Fatalf("列表响应应含 next_date: %v", memos[0])
	}
}

// TestUnauthorized 未认证请求返回 401
func TestUnauthorized(t *testing.T) {
	r, _ := newTestRouter(t, "")
	w := doJSON(t, r, http.MethodPost, "/api/memos", "", solarYearlyReq)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("应 401, 实际 %d", w.Code)
	}
	if decodeBody(t, w)["error"] != "unauthorized" {
		t.Fatalf("错误消息应为 unauthorized: %s", w.Body.String())
	}
	if w2 := doJSON(t, r, http.MethodGet, "/api/memos", "", nil); w2.Code != http.StatusUnauthorized {
		t.Fatalf("列表未认证应 401, 实际 %d", w2.Code)
	}
}
