package server

import (
	"net/http"
	"strconv"

	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

func (s *Server) handleFileTransferStatus(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := r.URL.Query().Get("requestKey")
	count, err := strconv.Atoi(r.URL.Query().Get("count"))
	if err != nil || count < 1 || count > 100 || key == "" || len(key) > taskstate.MaxIdempotencyKeyBytes-3 {
		http.Error(w, "invalid transfer observation", http.StatusBadRequest)
		return
	}
	type item struct {
		Index int      `json:"index"`
		Task  taskView `json:"task"`
	}
	items := make([]item, 0, count)
	for index := range count {
		task, found, err := s.tasks.FindByIdempotency(r.Context(), nodeID, key+"-"+strconv.Itoa(index))
		if err != nil {
			http.Error(w, "transfer observation failed", http.StatusInternalServerError)
			return
		}
		if found && task.Intent.File != nil && task.Intent.File.Transfer != nil {
			items = append(items, item{index, toTaskView(task)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
