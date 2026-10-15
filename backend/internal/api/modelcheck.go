package api

import (
	"errors"
	"net/http"

	"sub2api-guardian/backend/internal/modelcheck"
	"sub2api-guardian/backend/internal/store"
)

func (s *Server) getModelCheck(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.ModelCheckSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	targets := s.modelChecks.RunningTargets()
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings, "models": modelcheck.Models(), "running": len(targets) > 0, "running_targets": targets})
}

func (s *Server) saveModelCheck(w http.ResponseWriter, r *http.Request) {
	var settings store.ModelCheckSettings
	if err := decodeBody(r, &settings); err != nil {
		writeError(w, err)
		return
	}
	if err := s.modelChecks.Save(settings); err != nil {
		writeError(w, err)
		return
	}
	s.getModelCheck(w, r)
}

func (s *Server) modelCheckRuns(w http.ResponseWriter, r *http.Request) {
	accountID := int64(queryInt(r, "account_id", 0))
	before := int64(queryInt(r, "before", 0))
	if accountID < 0 || before < 0 {
		writeErrorMessage(w, http.StatusBadRequest, "筛选参数无效")
		return
	}
	items, err := s.store.ModelCheckRuns(accountID, r.URL.Query().Get("model"), before, 30)
	if err != nil {
		writeError(w, err)
		return
	}
	for i := range items {
		modelcheck.Reclassify(&items[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) modelCheckTimelines(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ModelCheckTimelines()
	if err != nil {
		writeError(w, err)
		return
	}
	for i := range items {
		for j := range items[i].Runs {
			modelcheck.Reclassify(&items[i].Runs[j])
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) runModelCheck(w http.ResponseWriter, r *http.Request) {
	var target store.ModelCheckTarget
	if err := decodeBody(r, &target); err != nil {
		writeError(w, err)
		return
	}
	if err := s.modelChecks.RunNow(target); err != nil {
		if errors.Is(err, modelcheck.ErrRunning) {
			writeErrorMessage(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}
