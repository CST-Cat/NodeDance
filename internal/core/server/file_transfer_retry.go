package server

import (
	"errors"
	"net/http"
	"path"
	"strings"

	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

func fileTransferRetryRoute(value string) (string, string, bool) {
	parts := strings.Split(value, "/")
	if len(parts) != 9 || parts[1] != "api" || parts[2] != "v1" || parts[3] != "nodes" || !validUUID(parts[4]) || parts[5] != "files" || parts[6] != "transfers" || parts[8] != "retry" || parts[7] == "" {
		return "", "", false
	}
	return parts[4], parts[7], true
}

func (s *Server) handleFileTransferRetry(w http.ResponseWriter, r *http.Request, current *session, nodeID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		http.Error(w, "Idempotency-Key is required", http.StatusBadRequest)
		return
	}
	task, err := s.tasks.Get(r.Context(), nodeID, taskID)
	if errors.Is(err, coretasks.ErrTaskNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "file transfer could not be read", http.StatusInternalServerError)
		return
	}
	if task.Intent.File == nil || task.Intent.File.Transfer == nil || task.Status != taskstate.Failed && task.Status != taskstate.Canceled && task.Status != taskstate.TimedOut {
		http.Error(w, "retry requires a confirmed failed or canceled file transfer; observe unknown results first", http.StatusConflict)
		return
	}
	transfer := task.Intent.File.Transfer
	result, err := s.transferFile(r.Context(), current, transfer.SourceNodeID, transfer.SourcePath, nodeID, path.Dir(task.Intent.File.Path), key, transfer.BandwidthLimit, transfer.Mode, taskID)
	if err != nil {
		failure := fileTransferFailure{SourcePath: transfer.SourcePath, Error: err.Error()}
		var taskErr fileTransferTaskError
		if errors.As(err, &taskErr) {
			failure.TaskID = taskErr.taskID
		}
		writeJSON(w, http.StatusOK, map[string]any{"transferred": []fileTransferResult{}, "failed": []fileTransferFailure{failure}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transferred": []fileTransferResult{result}})
}
