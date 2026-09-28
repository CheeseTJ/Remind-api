// Package memo 提供备忘的存储访问与 HTTP 接口。
package memo

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// timeLayout 数据库与接口统一使用的日期时间格式
const timeLayout = "2006-01-02 15:04:05"

// ErrNotFound 备忘不存在或不属于当前用户
var ErrNotFound = errors.New("备忘不存在")

// Memo 备忘实体，字段与 memos 表一一对应
type Memo struct {
	ID             int64
	UserID         string
	UserEmail      string
	Title          string
	Content        string
	DateType       string  // solar | lunar
	Repeat         string  // yearly | monthly | daily | once
	Month          *int    // 公历月 1-12，一次性提醒为 nil
	Day            *int    // 公历日 1-31
	LunarMonth     *int    // 农历月 1-12
	LunarDay       *int    // 农历日 1-30
	LunarLeap      bool    // 是否农历闰月
	AdvanceOffsets []int   // 提前提醒天数（DB 侧为 JSON 字符串），已去重升序
	OnceDate       *string // 一次性提醒日期 YYYY-MM-DD
	Enabled        bool
	CreatedAt      string
	UpdatedAt      string
}

// memoColumns 查询用的列清单
const memoColumns = `id,user_id,user_email,title,content,date_type,repeat,month,day,` +
	`lunar_month,lunar_day,lunar_leap,advance_offsets,once_date,enabled,created_at,updated_at`

// boolToInt 布尔转 SQLite 整数
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// decodeOffsets 把 DB 中的 JSON 字符串转为 []int
func decodeOffsets(s string) []int {
	var offsets []int
	if err := json.Unmarshal([]byte(s), &offsets); err != nil || offsets == nil {
		return []int{}
	}
	return offsets
}

// encodeOffsets 把 []int 转为 DB 使用的 JSON 字符串
func encodeOffsets(offsets []int) (string, error) {
	if offsets == nil {
		offsets = []int{}
	}
	b, err := json.Marshal(offsets)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// scanMemo 从一行记录扫描出 Memo
func scanMemo(scan func(dest ...any) error) (*Memo, error) {
	var m Memo
	var month, day, lunarMonth, lunarDay sql.NullInt64
	var onceDate sql.NullString
	var leap, enabled int
	var offsetsJSON string
	if err := scan(&m.ID, &m.UserID, &m.UserEmail, &m.Title, &m.Content, &m.DateType, &m.Repeat,
		&month, &day, &lunarMonth, &lunarDay, &leap, &offsetsJSON, &onceDate, &enabled,
		&m.CreatedAt, &m.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
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
	m.Enabled = enabled != 0
	m.AdvanceOffsets = decodeOffsets(offsetsJSON)
	if onceDate.Valid {
		v := onceDate.String
		m.OnceDate = &v
	}
	return &m, nil
}

// Create 新建备忘并回填 ID 与时间戳
func Create(db *sql.DB, m *Memo) error {
	offsets, err := encodeOffsets(m.AdvanceOffsets)
	if err != nil {
		return err
	}
	now := time.Now().Format(timeLayout)
	m.CreatedAt = now
	m.UpdatedAt = now
	res, err := db.Exec(`INSERT INTO memos(
  user_id,user_email,title,content,date_type,repeat,month,day,lunar_month,lunar_day,lunar_leap,
  advance_offsets,once_date,enabled,created_at,updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.UserID, m.UserEmail, m.Title, m.Content, m.DateType, m.Repeat,
		m.Month, m.Day, m.LunarMonth, m.LunarDay, boolToInt(m.LunarLeap),
		offsets, m.OnceDate, boolToInt(m.Enabled), m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	m.ID = id
	return nil
}

// GetByID 按 ID 查询指定用户的备忘，不存在返回 ErrNotFound
func GetByID(db *sql.DB, userID string, id int64) (*Memo, error) {
	row := db.QueryRow(`SELECT `+memoColumns+` FROM memos WHERE id=? AND user_id=?`, id, userID)
	return scanMemo(row.Scan)
}

// ListByUser 列出指定用户的备忘，按 updated_at 倒序；enabled 非 nil 时按启用状态过滤
func ListByUser(db *sql.DB, userID string, enabled *bool) ([]*Memo, error) {
	query := `SELECT ` + memoColumns + ` FROM memos WHERE user_id=?`
	args := []any{userID}
	if enabled != nil {
		query += ` AND enabled=?`
		args = append(args, boolToInt(*enabled))
	}
	query += ` ORDER BY updated_at DESC, id DESC`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []*Memo{}
	for rows.Next() {
		m, err := scanMemo(rows.Scan)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

// Update 更新指定用户的备忘内容并刷新 updated_at，不存在返回 ErrNotFound
func Update(db *sql.DB, m *Memo) error {
	offsets, err := encodeOffsets(m.AdvanceOffsets)
	if err != nil {
		return err
	}
	m.UpdatedAt = time.Now().Format(timeLayout)
	res, err := db.Exec(`UPDATE memos SET user_email=?,title=?,content=?,date_type=?,repeat=?,`+
		`month=?,day=?,lunar_month=?,lunar_day=?,lunar_leap=?,advance_offsets=?,once_date=?,`+
		`enabled=?,updated_at=? WHERE id=? AND user_id=?`,
		m.UserEmail, m.Title, m.Content, m.DateType, m.Repeat,
		m.Month, m.Day, m.LunarMonth, m.LunarDay, boolToInt(m.LunarLeap),
		offsets, m.OnceDate, boolToInt(m.Enabled), m.UpdatedAt, m.ID, m.UserID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete 删除指定用户的备忘，不存在返回 ErrNotFound
func Delete(db *sql.DB, userID string, id int64) error {
	res, err := db.Exec(`DELETE FROM memos WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
