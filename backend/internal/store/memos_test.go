package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoCRUDAndDefaults(t *testing.T) {
	st := openTemp(t)

	document, err := st.CreateMemo("  文档备忘录  ", MemoDocument)
	if err != nil {
		t.Fatalf("创建文档失败: %v", err)
	}
	if document.Title != "文档备忘录" || document.Revision != 1 || string(document.Content) != `{"ops":[{"insert":"\n"}]}` {
		t.Fatalf("文档默认值错误: %+v content=%s", document, document.Content)
	}

	sheet, err := st.CreateMemo("表格备忘录", MemoSheet)
	if err != nil {
		t.Fatalf("创建表格失败: %v", err)
	}
	var content struct {
		Cells        [][]string `json:"cells"`
		ColumnWidths []int      `json:"column_widths"`
		WrapText     bool       `json:"wrap_text"`
	}
	if err := json.Unmarshal(sheet.Content, &content); err != nil || len(content.Cells) != 20 || len(content.Cells[0]) != 8 {
		t.Fatalf("表格默认尺寸错误: rows=%d err=%v", len(content.Cells), err)
	}
	if len(content.ColumnWidths) != 8 || content.ColumnWidths[0] != defaultColumnWidth || !content.WrapText {
		t.Fatalf("表格默认显示设置错误: widths=%v wrap=%v", content.ColumnWidths, content.WrapText)
	}

	items, err := st.Memos()
	if err != nil || len(items) != 2 || items[0].ID != sheet.ID {
		t.Fatalf("列表错误: items=%+v err=%v", items, err)
	}
	loaded, err := st.Memo(document.ID)
	if err != nil || loaded.Title != document.Title {
		t.Fatalf("读取文档失败: %+v err=%v", loaded, err)
	}

	updated, err := st.UpdateMemo(document.ID, "更新后的文档", json.RawMessage(`{"ops":[{"insert":"内容\n","attributes":{"bold":true}}]}`), 1, false)
	if err != nil || updated.Revision != 2 || updated.Title != "更新后的文档" {
		t.Fatalf("更新文档失败: %+v err=%v", updated, err)
	}
	items, _ = st.Memos()
	if items[0].ID != document.ID {
		t.Fatalf("更新后的文档应排在首位: %+v", items)
	}
}

func TestMemoOptimisticLock(t *testing.T) {
	st := openTemp(t)
	memo, _ := st.CreateMemo("并发测试", MemoDocument)
	winner := json.RawMessage(`{"ops":[{"insert":"先保存\n"}]}`)
	loser := json.RawMessage(`{"ops":[{"insert":"后保存\n"}]}`)

	saved, err := st.UpdateMemo(memo.ID, memo.Title, winner, memo.Revision, false)
	if err != nil || saved.Revision != 2 {
		t.Fatalf("首次保存失败: %+v err=%v", saved, err)
	}
	if _, err := st.UpdateMemo(memo.ID, memo.Title, loser, memo.Revision, false); !errors.Is(err, ErrMemoConflict) {
		t.Fatalf("陈旧版本应冲突，实际 %v", err)
	}
	current, _ := st.Memo(memo.ID)
	if string(current.Content) != string(winner) {
		t.Fatalf("冲突写入覆盖了胜出内容: %s", current.Content)
	}

	forced, err := st.UpdateMemo(memo.ID, memo.Title, loser, memo.Revision, true)
	if err != nil || forced.Revision != 3 || string(forced.Content) != string(loser) {
		t.Fatalf("强制覆盖失败: %+v err=%v", forced, err)
	}
	if err := st.DeleteMemo(memo.ID, 2, false); !errors.Is(err, ErrMemoConflict) {
		t.Fatalf("陈旧删除应冲突，实际 %v", err)
	}
	if err := st.DeleteMemo(memo.ID, 2, true); err != nil {
		t.Fatalf("强制删除失败: %v", err)
	}
	if _, err := st.Memo(memo.ID); !errors.Is(err, ErrMemoNotFound) {
		t.Fatalf("删除后应不存在，实际 %v", err)
	}
}

func TestMemoValidation(t *testing.T) {
	st := openTemp(t)
	if _, err := st.CreateMemo(" ", MemoDocument); err == nil {
		t.Fatal("空标题应被拒绝")
	}
	if _, err := st.CreateMemo(strings.Repeat("长", 101), MemoDocument); err == nil {
		t.Fatal("超长标题应被拒绝")
	}
	if _, err := st.CreateMemo("未知", MemoType("unknown")); err == nil {
		t.Fatal("未知类型应被拒绝")
	}

	document, _ := st.CreateMemo("文档", MemoDocument)
	invalidDocuments := []json.RawMessage{
		json.RawMessage(`{"ops":[{"insert":{"image":"data:image/png;base64,x"}}]}`),
		json.RawMessage(`{"ops":[{"insert":"链接\n","attributes":{"link":"javascript:alert(1)"}}]}`),
		json.RawMessage(`{"ops":[{"insert":"缺少结尾换行"}]}`),
	}
	for _, content := range invalidDocuments {
		if _, err := st.UpdateMemo(document.ID, document.Title, content, 1, false); err == nil {
			t.Fatalf("非法文档应被拒绝: %s", content)
		}
	}

	sheet, _ := st.CreateMemo("表格", MemoSheet)
	invalidSheets := []json.RawMessage{
		json.RawMessage(`{"cells":[]}`),
		json.RawMessage(`{"cells":[["a"],["b","c"]]}`),
		json.RawMessage(`{"cells":[[1]]}`),
		json.RawMessage(`{"cells":[[""]],"column_widths":[140,140],"wrap_text":true}`),
		json.RawMessage(`{"cells":[[""]],"column_widths":[71],"wrap_text":true}`),
		json.RawMessage(`{"cells":[[""]],"column_widths":[601],"wrap_text":true}`),
	}
	tooManyRows := make([][]string, maxSheetRows+1)
	for row := range tooManyRows {
		tooManyRows[row] = []string{""}
	}
	invalidSheets = append(invalidSheets,
		marshalSheetContent(t, tooManyRows),
		marshalSheetContent(t, [][]string{make([]string, maxSheetColumns+1)}),
		marshalSheetContent(t, [][]string{{strings.Repeat("字", maxSheetCellRunes+1)}}),
	)
	for _, content := range invalidSheets {
		if _, err := st.UpdateMemo(sheet.ID, sheet.Title, content, 1, false); err == nil {
			t.Fatalf("非法表格应被拒绝: %s", content)
		}
	}
	if _, err := st.UpdateMemo(sheet.ID, sheet.Title, json.RawMessage(`{"cells":[["兼容旧数据"]]}`), 1, false); err != nil {
		t.Fatalf("不含显示设置的旧表格应继续有效: %v", err)
	}
}

func marshalSheetContent(t *testing.T, cells [][]string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"cells": cells})
	if err != nil {
		t.Fatalf("序列化表格失败: %v", err)
	}
	return json.RawMessage(raw)
}

func TestMemoPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guardian.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("首次打开失败: %v", err)
	}
	memo, err := st.CreateMemo("持久化", MemoDocument)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	defer func() { _ = again.Close() }()
	loaded, err := again.Memo(memo.ID)
	if err != nil || loaded.Title != memo.Title {
		t.Fatalf("重开后数据丢失: %+v err=%v", loaded, err)
	}
	version, err := again.getMeta(metaSchemaVersion)
	if err != nil || version != "7" {
		t.Fatalf("schema 版本 = %q err=%v", version, err)
	}
}
