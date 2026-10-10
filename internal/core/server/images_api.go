package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/distribution/reference"
)

const imageListTimeout = 35 * time.Second
const imageCredentialLifetime = 5 * time.Minute

type pendingImageCredential struct {
	nodeID string
	// The Core-owned staged copy uses clearable buffers. API request strings and
	// the single-use protocol payload are separately released at their handoff.
	username  []byte
	password  []byte
	expiresAt time.Time
}

func (c *pendingImageCredential) clear() {
	if c == nil {
		return
	}
	clear(c.username)
	clear(c.password)
	c.username = nil
	c.password = nil
}

func (c pendingImageCredential) available() bool {
	return validPendingImageCredentialText(c.username, 256) && validPendingImageCredentialText(c.password, 4096)
}

func validPendingImageCredentialText(value []byte, maxBytes int) bool {
	if len(value) == 0 || len(value) > maxBytes {
		return false
	}
	for _, item := range value {
		if item == 0 || item == '\r' || item == '\n' || item == 0x7f {
			return false
		}
	}
	return true
}

type imagePullRequest struct {
	ImageReference string `json:"imageReference"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
}

type imageDeleteRequest struct {
	DeleteConfirmed bool   `json:"deleteConfirmed"`
	ConfirmationID  string `json:"confirmationId"`
}

func imageRoute(path string) (nodeID, operation, target string, ok bool) {
	if !strings.HasPrefix(path, "/api/v1/nodes/") {
		return "", "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/nodes/"), "/")
	if len(parts) < 2 || len(parts) > 3 || !validUUID(parts[0]) || parts[1] != "images" {
		return "", "", "", false
	}
	if len(parts) == 2 {
		return parts[0], "list", "", true
	}
	if parts[2] == "pull" {
		return parts[0], "pull", "", true
	}
	if !validFullImageID(parts[2]) {
		return "", "", "", false
	}
	return parts[0], "delete", parts[2], true
}

func validFullImageID(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[7:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (s *Server) handleImageAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	nodeID, operation, target, ok := imageRoute(r.URL.Path)
	if !ok {
		return false
	}
	switch operation {
	case "list":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		pageSize := 50
		page := 0
		if raw := r.URL.Query().Get("pageSize"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > protocol.MaxImagePageSize {
				http.Error(w, "invalid image page size", http.StatusBadRequest)
				return true
			}
			pageSize = parsed
		}
		if raw := r.URL.Query().Get("page"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 || parsed > 1_000_000 {
				http.Error(w, "invalid image page", http.StatusBadRequest)
				return true
			}
			page = parsed
		}
		filter := r.URL.Query().Get("filter")
		if len(filter) > 256 || strings.ContainsAny(filter, "\r\n\x00") {
			http.Error(w, "invalid image filter", http.StatusBadRequest)
			return true
		}
		request := protocol.ImageListRequest{Filter: filter, Page: uint32(page), PageSize: uint32(pageSize)}
		response, err := s.requestNodeImages(r.Context(), nodeID, request)
		if err != nil {
			writeImageAPIError(w, err)
			return true
		}
		if response.ErrorCode != "" {
			http.Error(w, "Docker image inventory unavailable", http.StatusServiceUnavailable)
			return true
		}
		writeJSON(w, http.StatusOK, response)
		return true
	case "pull":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		var request imagePullRequest
		defer func() { request.Username, request.Password = "", "" }()
		if !decodeJSON(w, r, &request) {
			return true
		}
		if _, err := reference.ParseNormalizedNamed(request.ImageReference); err != nil || len(request.ImageReference) > 512 || strings.TrimSpace(request.ImageReference) != request.ImageReference {
			http.Error(w, "invalid image reference", http.StatusBadRequest)
			return true
		}
		credentials := (*protocol.RegistryCredentials)(nil)
		if request.Username != "" || request.Password != "" {
			candidate := protocol.RegistryCredentials{Username: request.Username, Password: request.Password}
			defer func() { candidate.Username, candidate.Password = "", "" }()
			if !candidate.Valid() {
				http.Error(w, "invalid registry credentials", http.StatusBadRequest)
				return true
			}
			credentials = &candidate
		}
		intent := protocol.TaskIntent{Action: protocol.TaskImagePull, ContainerID: protocol.ImageTargetKey("pull:" + request.ImageReference), ImageReference: request.ImageReference}
		s.createImageTask(w, r, current, nodeID, intent, credentials)
		return true
	case "delete":
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		var request imageDeleteRequest
		if !decodeJSON(w, r, &request) {
			return true
		}
		if !request.DeleteConfirmed || request.ConfirmationID != target {
			http.Error(w, "image deletion confirmation must match the full image ID", http.StatusBadRequest)
			return true
		}
		intent := protocol.TaskIntent{Action: protocol.TaskImageDelete, ContainerID: protocol.ImageTargetKey("delete:" + target), ImageID: target}
		s.createImageTask(w, r, current, nodeID, intent, nil)
		return true
	default:
		return false
	}
}

func (s *Server) createImageTask(w http.ResponseWriter, r *http.Request, current *session, nodeID string, intent protocol.TaskIntent, credentials *protocol.RegistryCredentials) {
	if credentials != nil {
		defer func() { credentials.Username, credentials.Password = "", "" }()
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		http.Error(w, "Idempotency-Key is required", http.StatusBadRequest)
		return
	}
	if err := protocol.ValidateTaskIntent(intent); err != nil {
		http.Error(w, "invalid image task", http.StatusBadRequest)
		return
	}
	state, view, _, viewErr := s.dockerViewStateForNode(r.Context(), nodeID)
	gate := func(ctx context.Context) (bool, func(), error) {
		if viewErr != nil {
			return false, nil, errTaskDockerUnavailable
		}
		if !state.Exists {
			return false, nil, coretasks.ErrNodeNotFound
		}
		if state.Status != "online" || !view.AgentOnline || view.DataStale || view.DockerAvailability != "available" || !view.DockerSnapshotFresh {
			return false, nil, coretasks.ErrNodeOffline
		}
		release, err := s.lockTaskBridgeReady(nodeID, state.Generation)
		if err != nil {
			return false, nil, err
		}
		return false, release, nil
	}
	credentialsLocked := credentials != nil
	if credentialsLocked {
		// Match the dispatcher lock order so an Agent poll cannot claim this
		// durable auth-required row before its one-use credential is installed.
		s.imageAuthMu.Lock()
		s.expireImageCredentialsLocked(s.now())
	}
	result, err := s.tasks.EnqueueWithGate(r.Context(), coretasks.EnqueueRequest{
		NodeID: nodeID, IdempotencyKey: key, Intent: intent, RegistryAuthRequired: credentials != nil,
		ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr,
	}, gate)
	if err != nil {
		if credentialsLocked {
			s.imageAuthMu.Unlock()
		}
		s.writeTaskStoreError(w, err)
		return
	}
	if credentials != nil && result.Task.Status == taskstate.Queued && result.Task.DeliveryState == "ready" && !result.Task.Evidence.DeliveryCommitted {
		s.storeImageCredentialsLocked(result.Task.TaskID, nodeID, *credentials)
		credentials.Username, credentials.Password = "", ""
	}
	if credentialsLocked {
		s.imageAuthMu.Unlock()
	}
	if result.Created || credentials != nil {
		s.signalAgentTasks(nodeID)
	}
	status := http.StatusAccepted
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"taskId": result.Task.TaskID, "status": result.Task.Status})
}

// claimNextImageTask keeps the availability check and the one-use credential
// take under imageAuthMu. ClaimNextWithRegistryAuth commits delivery only when
// this check succeeds; failed/expired entries are durably resolved before any
// task frame can be written.
func (s *Server) claimNextImageTask(ctx context.Context, connection coretasks.AgentConnection) (coretasks.Task, bool, *protocol.RegistryCredentials, error) {
	s.imageAuthMu.Lock()
	defer s.imageAuthMu.Unlock()
	s.expireImageCredentialsLocked(s.now())
	task, claimed, err := s.tasks.ClaimNextWithRegistryAuth(ctx, connection, sql.NullInt64{}, "unknown", func(task coretasks.Task) bool {
		entry, ok := s.imageAuth[task.TaskID]
		return ok && entry.nodeID == task.NodeID && entry.available()
	})
	if !claimed && task.Result.Code == coretasks.ResultRegistryCredentialsUnavailable {
		if entry, ok := s.imageAuth[task.TaskID]; ok {
			entry.clear()
			delete(s.imageAuth, task.TaskID)
		}
	}
	if err != nil || !claimed || !task.RegistryAuthRequired {
		return task, claimed, nil, err
	}
	entry := s.imageAuth[task.TaskID]
	delete(s.imageAuth, task.TaskID)
	credentials := protocol.RegistryCredentials{Username: string(entry.username), Password: string(entry.password)}
	entry.clear()
	return task, true, &credentials, nil
}

func (s *Server) storeImageCredentials(taskID, nodeID string, credentials protocol.RegistryCredentials) {
	s.imageAuthMu.Lock()
	s.storeImageCredentialsLocked(taskID, nodeID, credentials)
	s.imageAuthMu.Unlock()
}

func (s *Server) storeImageCredentialsLocked(taskID, nodeID string, credentials protocol.RegistryCredentials) {
	s.expireImageCredentialsLocked(s.now())
	if previous, ok := s.imageAuth[taskID]; ok {
		previous.clear()
	}
	s.imageAuth[taskID] = pendingImageCredential{nodeID: nodeID, username: []byte(credentials.Username),
		password: []byte(credentials.Password), expiresAt: s.now().Add(imageCredentialLifetime)}
}

func (s *Server) takeImageCredentials(taskID, nodeID string) *protocol.RegistryCredentials {
	s.imageAuthMu.Lock()
	s.expireImageCredentialsLocked(s.now())
	entry, ok := s.imageAuth[taskID]
	if ok {
		delete(s.imageAuth, taskID)
	}
	s.imageAuthMu.Unlock()
	if !ok {
		return nil
	}
	if entry.nodeID != nodeID {
		entry.clear()
		return nil
	}
	credentials := protocol.RegistryCredentials{Username: string(entry.username), Password: string(entry.password)}
	entry.clear()
	return &credentials
}

func (s *Server) clearImageCredentials(taskID string) {
	s.imageAuthMu.Lock()
	if entry, ok := s.imageAuth[taskID]; ok {
		entry.clear()
	}
	delete(s.imageAuth, taskID)
	s.imageAuthMu.Unlock()
}

func (s *Server) clearImageCredentialsForNode(nodeID string) {
	s.imageAuthMu.Lock()
	for taskID, entry := range s.imageAuth {
		if entry.nodeID == nodeID {
			entry.clear()
			delete(s.imageAuth, taskID)
		}
	}
	s.imageAuthMu.Unlock()
}

func (s *Server) requestImagePullCancellation(task coretasks.Task) error {
	if task.Intent.Action != protocol.TaskImagePull || task.Status != taskstate.Running || task.DispatchJournalID == "" {
		return coretasks.ErrTaskStateConflict
	}
	s.agentConnectionsMu.Lock()
	defer s.agentConnectionsMu.Unlock()
	for _, connection := range s.agentConnections {
		if connection.nodeID != task.NodeID || !connection.capabilityEnabled(protocol.CapabilityTaskBridge) || connection.generation == 0 {
			continue
		}
		connection.taskMu.RLock()
		if !connection.taskSynced || connection.taskJournalID != task.DispatchJournalID {
			connection.taskMu.RUnlock()
			continue
		}
		request := protocol.TaskCancelRequest{TaskID: task.TaskID, JournalID: task.DispatchJournalID}
		command := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTaskCancelRequest,
			Generation: connection.generation, RequestID: task.TaskID, Payload: marshalAgentPayload(request)}
		if err := protocol.ValidateTaskCancelRequest(command, request, task.NodeID, task.DispatchJournalID, connection.generation); err != nil {
			connection.taskMu.RUnlock()
			return err
		}
		select {
		case connection.commands <- command:
			connection.taskMu.RUnlock()
			return nil
		case <-connection.ctx.Done():
			connection.taskMu.RUnlock()
			clear(command.Payload)
			return coretasks.ErrNodeOffline
		default:
			connection.taskMu.RUnlock()
			clear(command.Payload)
			return errors.New("Agent command queue is full")
		}
	}
	return coretasks.ErrNodeOffline
}

func (s *Server) expireImageCredentials(now time.Time) {
	s.imageAuthMu.Lock()
	s.expireImageCredentialsLocked(now)
	s.imageAuthMu.Unlock()
}

func (s *Server) expireImageCredentialsLocked(now time.Time) {
	for taskID, entry := range s.imageAuth {
		if !now.Before(entry.expiresAt) {
			entry.clear()
			delete(s.imageAuth, taskID)
		}
	}
}

func (s *Server) clearAllImageCredentials() {
	if s == nil {
		return
	}
	s.imageAuthMu.Lock()
	for taskID, entry := range s.imageAuth {
		entry.clear()
		delete(s.imageAuth, taskID)
	}
	s.imageAuthMu.Unlock()
}

func (s *Server) requestNodeImages(ctx context.Context, nodeID string, request protocol.ImageListRequest) (protocol.ImageListResponse, error) {
	state, _, err := s.currentMetricsState(ctx, nodeID)
	if err != nil {
		return protocol.ImageListResponse{}, errTaskDockerUnavailable
	}
	if !state.Exists {
		return protocol.ImageListResponse{}, coretasks.ErrNodeNotFound
	}
	s.agentConnectionsMu.Lock()
	var connection *agentConnection
	for _, candidate := range s.agentConnections {
		if candidate.nodeID == nodeID {
			connection = candidate
			break
		}
	}
	s.agentConnectionsMu.Unlock()
	if connection == nil || !connection.capabilityEnabled(protocol.CapabilityImages) || connection.generation != state.Generation {
		return protocol.ImageListResponse{}, coretasks.ErrNodeOffline
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return protocol.ImageListResponse{}, errors.New("image request ID could not be generated")
	}
	requestID := hex.EncodeToString(idBytes)
	responseCh := make(chan protocol.ImageListResponse, 1)
	connection.imageResponseMu.Lock()
	connection.imageResponses[requestID] = imageResponseWaiter{response: responseCh, page: request.Page}
	connection.imageResponseMu.Unlock()
	defer func() {
		connection.imageResponseMu.Lock()
		delete(connection.imageResponses, requestID)
		connection.imageResponseMu.Unlock()
	}()
	payload, err := json.Marshal(request)
	if err != nil {
		return protocol.ImageListResponse{}, errors.New("image request could not be encoded")
	}
	command := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeImageListRequest,
		Generation: connection.generation, RequestID: requestID, Payload: payload}
	select {
	case connection.commands <- command:
	case <-ctx.Done():
		return protocol.ImageListResponse{}, ctx.Err()
	case <-connection.ctx.Done():
		return protocol.ImageListResponse{}, coretasks.ErrNodeOffline
	default:
		return protocol.ImageListResponse{}, errors.New("Agent image request queue is full")
	}
	timer := time.NewTimer(imageListTimeout)
	defer timer.Stop()
	select {
	case response := <-responseCh:
		if response.Page != request.Page {
			return protocol.ImageListResponse{}, errors.New("Agent image response does not match the requested page")
		}
		return response, nil
	case <-ctx.Done():
		return protocol.ImageListResponse{}, ctx.Err()
	case <-connection.ctx.Done():
		return protocol.ImageListResponse{}, coretasks.ErrNodeOffline
	case <-timer.C:
		return protocol.ImageListResponse{}, context.DeadlineExceeded
	}
}

func (c *agentConnection) resolveImageResponse(requestID string, response protocol.ImageListResponse) {
	c.imageResponseMu.Lock()
	defer c.imageResponseMu.Unlock()
	if waiter := c.imageResponses[requestID]; waiter.response != nil {
		select {
		case waiter.response <- response:
		default:
		}
	}
}

func decodeImagePayload(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > protocol.MaxMessageBytes {
		return errors.New("image payload exceeds its bound")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("image payload has trailing data")
	}
	return nil
}

func writeImageAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, coretasks.ErrNodeNotFound):
		http.Error(w, "node not found", http.StatusNotFound)
	case errors.Is(err, coretasks.ErrNodeOffline):
		http.Error(w, "Agent is offline or does not support image management", http.StatusServiceUnavailable)
	case errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "Docker image inventory request timed out", http.StatusGatewayTimeout)
	default:
		http.Error(w, "Docker image inventory request failed", http.StatusServiceUnavailable)
	}
}
