package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"sub2api-guardian/backend/internal/store"
)

func TestMemoAPIWorkflowAndConflicts(t *testing.T) {
	handler, _ := setupAPI(t, &fakeUpstream{groupCount: 1})

	createdResponse := doJSON(t, handler, http.MethodPost, "/api/memos", map[string]any{
		"title": "接口文档", "type": "document",
	})
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("创建返回 %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created store.Memo
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil || created.Revision != 1 {
		t.Fatalf("创建响应无效: %+v err=%v", created, err)
	}

	list := doJSON(t, handler, http.MethodGet, "/api/memos", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"title":"接口文档"`) || strings.Contains(list.Body.String(), `"content"`) {
		t.Fatalf("列表响应错误: %d %s", list.Code, list.Body.String())
	}
	detail := doJSON(t, handler, http.MethodGet, "/api/memos/"+itoa(created.ID), nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"content"`) {
		t.Fatalf("详情响应错误: %d %s", detail.Code, detail.Body.String())
	}

	firstContent := map[string]any{"ops": []any{map[string]any{"insert": "第一版\n"}}}
	updatedResponse := doJSON(t, handler, http.MethodPut, "/api/memos/"+itoa(created.ID), map[string]any{
		"title": "接口文档", "content": firstContent, "expected_revision": 1,
	})
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("更新返回 %d: %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	var updated store.Memo
	if err := json.Unmarshal(updatedResponse.Body.Bytes(), &updated); err != nil || updated.Revision != 2 {
		t.Fatalf("更新响应无效: %+v err=%v", updated, err)
	}

	secondContent := map[string]any{"ops": []any{map[string]any{"insert": "第二版\n"}}}
	conflict := doJSON(t, handler, http.MethodPut, "/api/memos/"+itoa(created.ID), map[string]any{
		"title": "接口文档", "content": secondContent, "expected_revision": 1,
	})
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), `"code":"MEMO_CONFLICT"`) {
		t.Fatalf("陈旧更新未冲突: %d %s", conflict.Code, conflict.Body.String())
	}
	forced := doJSON(t, handler, http.MethodPut, "/api/memos/"+itoa(created.ID), map[string]any{
		"title": "强制覆盖", "content": secondContent, "expected_revision": 1, "force": true,
	})
	if forced.Code != http.StatusOK || !strings.Contains(forced.Body.String(), `"revision":3`) {
		t.Fatalf("强制更新失败: %d %s", forced.Code, forced.Body.String())
	}

	archivesResponse := doJSON(t, handler, http.MethodGet, "/api/memos/"+itoa(created.ID)+"/archives", nil)
	var archives struct {
		Items []store.MemoArchive `json:"items"`
	}
	if archivesResponse.Code != http.StatusOK || json.Unmarshal(archivesResponse.Body.Bytes(), &archives) != nil ||
		len(archives.Items) != 2 || archives.Items[1].SourceRevision != 1 {
		t.Fatalf("恢复点列表错误: %d %s", archivesResponse.Code, archivesResponse.Body.String())
	}
	archiveID := archives.Items[1].ID
	staleRestore := doJSON(t, handler, http.MethodPost,
		"/api/memos/"+itoa(created.ID)+"/archives/"+itoa(archiveID)+"/restore", map[string]any{
			"expected_revision": 2,
		})
	if staleRestore.Code != http.StatusConflict || !strings.Contains(staleRestore.Body.String(), `"code":"MEMO_CONFLICT"`) {
		t.Fatalf("陈旧恢复未冲突: %d %s", staleRestore.Code, staleRestore.Body.String())
	}
	restored := doJSON(t, handler, http.MethodPost,
		"/api/memos/"+itoa(created.ID)+"/archives/"+itoa(archiveID)+"/restore", map[string]any{
			"expected_revision": 2, "force": true,
		})
	if restored.Code != http.StatusOK || !strings.Contains(restored.Body.String(), `"revision":4`) ||
		!strings.Contains(restored.Body.String(), `"content":{"ops":[{"insert":"\n"}]}`) {
		t.Fatalf("强制恢复失败: %d %s", restored.Code, restored.Body.String())
	}

	deleteConflict := doJSON(t, handler, http.MethodDelete, "/api/memos/"+itoa(created.ID), map[string]any{
		"expected_revision": 2,
	})
	if deleteConflict.Code != http.StatusConflict {
		t.Fatalf("陈旧删除未冲突: %d %s", deleteConflict.Code, deleteConflict.Body.String())
	}
	deleted := doJSON(t, handler, http.MethodDelete, "/api/memos/"+itoa(created.ID), map[string]any{
		"expected_revision": 2, "force": true,
	})
	if deleted.Code != http.StatusOK {
		t.Fatalf("强制删除失败: %d %s", deleted.Code, deleted.Body.String())
	}
	missing := doJSON(t, handler, http.MethodGet, "/api/memos/"+itoa(created.ID), nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("删除后详情返回 %d: %s", missing.Code, missing.Body.String())
	}
}

func TestMemoAPIValidation(t *testing.T) {
	handler, _ := setupAPI(t, &fakeUpstream{groupCount: 1})

	for _, payload := range []map[string]any{
		{"title": "", "type": "document"},
		{"title": "未知", "type": "unknown"},
	} {
		response := doJSON(t, handler, http.MethodPost, "/api/memos", payload)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("非法创建返回 %d: %s", response.Code, response.Body.String())
		}
	}

	documentResponse := doJSON(t, handler, http.MethodPost, "/api/memos", map[string]any{
		"title": "文档", "type": "document",
	})
	var document store.Memo
	_ = json.Unmarshal(documentResponse.Body.Bytes(), &document)
	invalidDocument := doJSON(t, handler, http.MethodPut, "/api/memos/"+itoa(document.ID), map[string]any{
		"title":             "文档",
		"content":           map[string]any{"ops": []any{map[string]any{"insert": map[string]any{"image": "x"}}}},
		"expected_revision": 1,
	})
	if invalidDocument.Code != http.StatusBadRequest {
		t.Fatalf("嵌入对象返回 %d: %s", invalidDocument.Code, invalidDocument.Body.String())
	}

	sheetResponse := doJSON(t, handler, http.MethodPost, "/api/memos", map[string]any{
		"title": "表格", "type": "sheet",
	})
	var sheet store.Memo
	_ = json.Unmarshal(sheetResponse.Body.Bytes(), &sheet)
	if !strings.Contains(sheetResponse.Body.String(), `"column_widths":[140,140,140,140,140,140,140,140]`) ||
		!strings.Contains(sheetResponse.Body.String(), `"wrap_text":true`) {
		t.Fatalf("表格默认显示设置缺失: %s", sheetResponse.Body.String())
	}
	invalidSheet := doJSON(t, handler, http.MethodPut, "/api/memos/"+itoa(sheet.ID), map[string]any{
		"title": "表格", "content": map[string]any{"cells": []any{[]string{"a"}, []string{"b", "c"}}},
		"expected_revision": 1,
	})
	if invalidSheet.Code != http.StatusBadRequest {
		t.Fatalf("非矩形表格返回 %d: %s", invalidSheet.Code, invalidSheet.Body.String())
	}
	tooManyRows := make([][]string, 201)
	for row := range tooManyRows {
		tooManyRows[row] = []string{""}
	}
	oversizedSheet := doJSON(t, handler, http.MethodPut, "/api/memos/"+itoa(sheet.ID), map[string]any{
		"title": "表格", "content": map[string]any{"cells": tooManyRows}, "expected_revision": 1,
	})
	if oversizedSheet.Code != http.StatusBadRequest {
		t.Fatalf("超行数表格返回 %d: %s", oversizedSheet.Code, oversizedSheet.Body.String())
	}
	invalidWidth := doJSON(t, handler, http.MethodPut, "/api/memos/"+itoa(sheet.ID), map[string]any{
		"title": "表格", "content": map[string]any{
			"cells": [][]string{{""}}, "column_widths": []int{71}, "wrap_text": true,
		}, "expected_revision": 1,
	})
	if invalidWidth.Code != http.StatusBadRequest {
		t.Fatalf("非法列宽返回 %d: %s", invalidWidth.Code, invalidWidth.Body.String())
	}
	validDisplaySettings := doJSON(t, handler, http.MethodPut, "/api/memos/"+itoa(sheet.ID), map[string]any{
		"title": "表格", "content": map[string]any{
			"cells": [][]string{{"a", "b"}}, "column_widths": []int{220, 140}, "wrap_text": false,
		}, "expected_revision": 1,
	})
	if validDisplaySettings.Code != http.StatusOK ||
		!strings.Contains(validDisplaySettings.Body.String(), `"column_widths":[220,140]`) ||
		!strings.Contains(validDisplaySettings.Body.String(), `"wrap_text":false`) {
		t.Fatalf("合法显示设置未完整保存: %d %s", validDisplaySettings.Code, validDisplaySettings.Body.String())
	}
}
