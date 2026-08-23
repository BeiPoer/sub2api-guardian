package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	MemoDocument MemoType = "document"
	MemoSheet    MemoType = "sheet"

	maxMemoTitleRunes = 100
	maxSheetRows      = 200
	maxSheetColumns   = 50
	maxSheetCellRunes = 10_000
)

var (
	ErrMemoNotFound = errors.New("备忘录不存在")
	ErrMemoConflict = errors.New("备忘录已在其他页面更新")
)

type MemoType string

func (t MemoType) Valid() bool { return t == MemoDocument || t == MemoSheet }

type MemoSummary struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Type      MemoType `json:"type"`
	Revision  int64    `json:"revision"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

type Memo struct {
	MemoSummary
	Content json.RawMessage `json:"content"`
}

func (s *Store) Memos() ([]MemoSummary, error) {
	rows, err := s.db.Query(`SELECT id, title, type, revision, created_at, updated_at
		FROM memos ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]MemoSummary, 0)
	for rows.Next() {
		var item MemoSummary
		if err := rows.Scan(&item.ID, &item.Title, &item.Type, &item.Revision,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) Memo(id int64) (Memo, error) {
	return scanMemo(s.db.QueryRow(`SELECT id, title, type, content_json, revision, created_at, updated_at
		FROM memos WHERE id = ?`, id))
}

func (s *Store) CreateMemo(title string, memoType MemoType) (Memo, error) {
	title, err := normalizeMemoTitle(title)
	if err != nil {
		return Memo{}, err
	}
	if !memoType.Valid() {
		return Memo{}, errors.New("备忘录类型无效")
	}
	content, err := defaultMemoContent(memoType)
	if err != nil {
		return Memo{}, err
	}
	now := nowString()

	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec(`INSERT INTO memos(title, type, content_json, revision, created_at, updated_at)
		VALUES(?, ?, ?, 1, ?, ?)`, title, memoType, string(content), now, now)
	if err != nil {
		return Memo{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Memo{}, err
	}
	return Memo{
		MemoSummary: MemoSummary{ID: id, Title: title, Type: memoType, Revision: 1, CreatedAt: now, UpdatedAt: now},
		Content:     content,
	}, nil
}

func (s *Store) UpdateMemo(id int64, title string, content json.RawMessage, expectedRevision int64, force bool) (Memo, error) {
	title, err := normalizeMemoTitle(title)
	if err != nil {
		return Memo{}, err
	}
	if !force && expectedRevision < 1 {
		return Memo{}, errors.New("expected_revision 必须大于 0")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Memo{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var memoType MemoType
	if err := tx.QueryRow(`SELECT type FROM memos WHERE id = ?`, id).Scan(&memoType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Memo{}, ErrMemoNotFound
		}
		return Memo{}, err
	}
	if err := validateMemoContent(memoType, content); err != nil {
		return Memo{}, err
	}

	now := nowString()
	query := `UPDATE memos SET title = ?, content_json = ?, revision = revision + 1, updated_at = ? WHERE id = ?`
	args := []any{title, string(content), now, id}
	if !force {
		query += ` AND revision = ?`
		args = append(args, expectedRevision)
	}
	result, err := tx.Exec(query, args...)
	if err != nil {
		return Memo{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Memo{}, err
	}
	if affected == 0 {
		return Memo{}, ErrMemoConflict
	}

	memo, err := scanMemo(tx.QueryRow(`SELECT id, title, type, content_json, revision, created_at, updated_at
		FROM memos WHERE id = ?`, id))
	if err != nil {
		return Memo{}, err
	}
	if err := tx.Commit(); err != nil {
		return Memo{}, err
	}
	return memo, nil
}

func (s *Store) DeleteMemo(id, expectedRevision int64, force bool) error {
	if !force && expectedRevision < 1 {
		return errors.New("expected_revision 必须大于 0")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	query := `DELETE FROM memos WHERE id = ?`
	args := []any{id}
	if !force {
		query += ` AND revision = ?`
		args = append(args, expectedRevision)
	}
	result, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var exists int
		err := tx.QueryRow(`SELECT 1 FROM memos WHERE id = ?`, id).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMemoNotFound
		}
		if err != nil {
			return err
		}
		return ErrMemoConflict
	}
	return tx.Commit()
}

func normalizeMemoTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("备忘录标题不能为空")
	}
	if utf8.RuneCountInString(title) > maxMemoTitleRunes {
		return "", fmt.Errorf("备忘录标题不能超过 %d 个字符", maxMemoTitleRunes)
	}
	return title, nil
}

func defaultMemoContent(memoType MemoType) (json.RawMessage, error) {
	if memoType == MemoDocument {
		return json.RawMessage(`{"ops":[{"insert":"\n"}]}`), nil
	}
	cells := make([][]string, 20)
	for row := range cells {
		cells[row] = make([]string, 8)
	}
	raw, err := json.Marshal(struct {
		Cells [][]string `json:"cells"`
	}{Cells: cells})
	return json.RawMessage(raw), err
}

func validateMemoContent(memoType MemoType, content json.RawMessage) error {
	if len(content) == 0 || !json.Valid(content) {
		return errors.New("备忘录内容必须是有效 JSON")
	}
	switch memoType {
	case MemoDocument:
		return validateDocumentContent(content)
	case MemoSheet:
		return validateSheetContent(content)
	default:
		return errors.New("备忘录类型无效")
	}
}

func validateDocumentContent(content json.RawMessage) error {
	type deltaOp struct {
		Insert     *string                    `json:"insert"`
		Attributes map[string]json.RawMessage `json:"attributes,omitempty"`
	}
	var delta struct {
		Ops []deltaOp `json:"ops"`
	}
	if err := decodeStrictJSON(content, &delta); err != nil {
		return fmt.Errorf("文档内容不是合法 Delta: %w", err)
	}
	if len(delta.Ops) == 0 {
		return errors.New("文档内容至少需要一个操作")
	}
	for _, op := range delta.Ops {
		if op.Insert == nil || *op.Insert == "" {
			return errors.New("文档只允许非空字符串插入")
		}
		for name, raw := range op.Attributes {
			if err := validateDocumentAttribute(name, raw); err != nil {
				return err
			}
		}
	}
	if !strings.HasSuffix(*delta.Ops[len(delta.Ops)-1].Insert, "\n") {
		return errors.New("文档内容必须以换行符结束")
	}
	return nil
}

func validateDocumentAttribute(name string, raw json.RawMessage) error {
	switch name {
	case "bold", "italic", "underline", "blockquote":
		var enabled bool
		if err := json.Unmarshal(raw, &enabled); err != nil || !enabled {
			return fmt.Errorf("文档属性 %s 无效", name)
		}
	case "header":
		var level int
		if err := json.Unmarshal(raw, &level); err != nil || (level != 1 && level != 2) {
			return errors.New("文档标题级别只支持 1 或 2")
		}
	case "list":
		var style string
		if err := json.Unmarshal(raw, &style); err != nil || (style != "ordered" && style != "bullet") {
			return errors.New("文档列表类型无效")
		}
	case "link":
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || !validMemoLink(value) {
			return errors.New("文档链接只支持 http、https 或 mailto")
		}
	default:
		return fmt.Errorf("文档不支持属性 %s", name)
	}
	return nil
}

func validMemoLink(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "mailto":
		return true
	default:
		return false
	}
}

func validateSheetContent(content json.RawMessage) error {
	var sheet struct {
		Cells [][]string `json:"cells"`
	}
	if err := decodeStrictJSON(content, &sheet); err != nil {
		return fmt.Errorf("表格内容无效: %w", err)
	}
	if len(sheet.Cells) < 1 || len(sheet.Cells) > maxSheetRows {
		return fmt.Errorf("表格行数必须在 1–%d 之间", maxSheetRows)
	}
	columns := len(sheet.Cells[0])
	if columns < 1 || columns > maxSheetColumns {
		return fmt.Errorf("表格列数必须在 1–%d 之间", maxSheetColumns)
	}
	for _, row := range sheet.Cells {
		if len(row) != columns {
			return errors.New("表格每一行的列数必须一致")
		}
		for _, cell := range row {
			if utf8.RuneCountInString(cell) > maxSheetCellRunes {
				return fmt.Errorf("单元格不能超过 %d 个字符", maxSheetCellRunes)
			}
		}
	}
	return nil
}

func decodeStrictJSON(raw []byte, out any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

type memoScanner interface {
	Scan(dest ...any) error
}

func scanMemo(row memoScanner) (Memo, error) {
	var memo Memo
	var content string
	err := row.Scan(&memo.ID, &memo.Title, &memo.Type, &content, &memo.Revision,
		&memo.CreatedAt, &memo.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Memo{}, ErrMemoNotFound
	}
	if err != nil {
		return Memo{}, err
	}
	memo.Content = json.RawMessage(content)
	return memo, nil
}
