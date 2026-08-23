package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"sub2api-guardian/backend/internal/store"
)

type memoCreateInput struct {
	Title string         `json:"title"`
	Type  store.MemoType `json:"type"`
}

type memoUpdateInput struct {
	Title            string          `json:"title"`
	Content          json.RawMessage `json:"content"`
	ExpectedRevision int64           `json:"expected_revision"`
	Force            bool            `json:"force,omitempty"`
}

type memoDeleteInput struct {
	ExpectedRevision int64 `json:"expected_revision"`
	Force            bool  `json:"force,omitempty"`
}

func (s *Server) listMemos(w http.ResponseWriter, _ *http.Request) {
	items, err := s.store.Memos()
	if err != nil {
		writeMemoError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createMemo(w http.ResponseWriter, r *http.Request) {
	var payload memoCreateInput
	if err := decodeBody(r, &payload); err != nil {
		writeErrorMessage(w, http.StatusBadRequest, "请求内容不是有效的 JSON")
		return
	}
	memo, err := s.store.CreateMemo(payload.Title, payload.Type)
	if err != nil {
		writeMemoError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, memo)
}

func (s *Server) getMemo(w http.ResponseWriter, r *http.Request) {
	id, err := memoPathID(r)
	if err != nil {
		writeErrorMessage(w, http.StatusBadRequest, "备忘录 ID 无效")
		return
	}
	memo, err := s.store.Memo(id)
	if err != nil {
		writeMemoError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, memo)
}

func (s *Server) listMemoArchives(w http.ResponseWriter, r *http.Request) {
	id, err := memoPathID(r)
	if err != nil {
		writeErrorMessage(w, http.StatusBadRequest, "备忘录 ID 无效")
		return
	}
	items, err := s.store.MemoArchives(id)
	if err != nil {
		writeMemoError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) updateMemo(w http.ResponseWriter, r *http.Request) {
	id, err := memoPathID(r)
	if err != nil {
		writeErrorMessage(w, http.StatusBadRequest, "备忘录 ID 无效")
		return
	}
	var payload memoUpdateInput
	if err := decodeBody(r, &payload); err != nil {
		writeErrorMessage(w, http.StatusBadRequest, "请求内容不是有效的 JSON")
		return
	}
	memo, err := s.store.UpdateMemo(id, payload.Title, payload.Content, payload.ExpectedRevision, payload.Force)
	if err != nil {
		writeMemoError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, memo)
}

func (s *Server) deleteMemo(w http.ResponseWriter, r *http.Request) {
	id, err := memoPathID(r)
	if err != nil {
		writeErrorMessage(w, http.StatusBadRequest, "备忘录 ID 无效")
		return
	}
	var payload memoDeleteInput
	if err := decodeBody(r, &payload); err != nil {
		writeErrorMessage(w, http.StatusBadRequest, "请求内容不是有效的 JSON")
		return
	}
	if err := s.store.DeleteMemo(id, payload.ExpectedRevision, payload.Force); err != nil {
		writeMemoError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) restoreMemoArchive(w http.ResponseWriter, r *http.Request) {
	memoID, err := memoPathID(r)
	if err != nil {
		writeErrorMessage(w, http.StatusBadRequest, "备忘录 ID 无效")
		return
	}
	archiveID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("archiveId")), 10, 64)
	if err != nil || archiveID < 1 {
		writeErrorMessage(w, http.StatusBadRequest, "恢复点 ID 无效")
		return
	}
	var payload memoDeleteInput
	if err := decodeBody(r, &payload); err != nil {
		writeErrorMessage(w, http.StatusBadRequest, "请求内容不是有效的 JSON")
		return
	}
	memo, err := s.store.RestoreMemoArchive(
		memoID, archiveID, payload.ExpectedRevision, payload.Force,
	)
	if err != nil {
		writeMemoError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, memo)
}

func memoPathID(r *http.Request) (int64, error) {
	id, err := pathID(r)
	if err != nil || id < 1 {
		return 0, errors.New("invalid memo id")
	}
	return id, nil
}

func writeMemoError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrMemoNotFound), errors.Is(err, store.ErrMemoArchiveNotFound):
		writeErrorMessage(w, http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrMemoConflict):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": err.Error(),
			"code":  "MEMO_CONFLICT",
		})
	default:
		writeErrorMessage(w, http.StatusBadRequest, err.Error())
	}
}
