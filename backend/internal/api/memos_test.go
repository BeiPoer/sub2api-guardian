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
}
