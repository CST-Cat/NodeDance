package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func (m *Manager) DeleteBackup(ctx context.Context, r Request) (Job, error) {
	if err := Validate(r); err != nil {
		return Job{}, err
	}
	if !r.Confirm || r.JobID == "" || r.Version != "" {
		return Job{}, errors.New("backup deletion requires a jobId and explicit confirmation")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.active {
		return Job{}, errors.New("backup deletion is unavailable while an update is active")
	}
	job, ok := m.jobs[r.JobID]
	if !ok || job.Component != r.Component {
		return Job{}, errors.New("update job not found")
	}
	if job.Finished == nil || (job.Status != "succeeded" && job.Status != "rolled_back" && job.Status != "failed") {
		return Job{}, errors.New("backup is still required for update recovery")
	}
	for _, other := range m.jobs {
		if other.Parent == job.ID && other.Finished == nil {
			return Job{}, errors.New("backup is in use by a rollback")
		}
	}
	target, ok := m.Config.Targets[r.Component]
	if !ok {
		return Job{}, errors.New("managed deployment is unavailable")
	}
	if err := privateDirectory(m.Config.StateDir); err != nil {
		return Job{}, err
	}
	workspace := filepath.Join(m.Config.StateDir, job.ID)
	if info, err := os.Lstat(workspace); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Job{}, errors.New("backup workspace must be a regular private directory")
		}
		if err := privateDirectory(workspace); err != nil {
			return Job{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Job{}, err
	}
	if job.BackupDeleted {
		return job, nil
	}
	job.BackupDeleting = true
	if err := m.persist(job); err != nil {
		return Job{}, err
	}
	if job.OldContainer != "" && job.Status == "succeeded" && job.Parent == "" {
		if err := m.Docker.initialize(ctx); err != nil {
			return Job{}, err
		}
		current, err := m.Docker.inspect(ctx, target.Container)
		if err != nil {
			return Job{}, err
		}
		if current["Id"] == job.OldContainer {
			return Job{}, errors.New("refusing to delete the running Agent container")
		}
		old, err := m.Docker.inspect(ctx, job.OldContainer)
		if err == nil {
			state, _ := old["State"].(map[string]any)
			if old["Name"] != "/"+target.Container+".rollback-"+job.ID || state["Running"] != false {
				return Job{}, errors.New("refusing to delete an active or changed backup container")
			}
			if err := m.Docker.call(ctx, "DELETE", m.Docker.version+"/containers/"+job.OldContainer+"?force=false&v=false", nil, nil); err != nil {
				return Job{}, err
			}
		} else if !strings.HasPrefix(err.Error(), "Docker HTTP 404:") {
			return Job{}, err
		}
	}
	if err := os.RemoveAll(workspace); err != nil {
		return Job{}, err
	}
	job.BackupDeleting = false
	job.BackupDeleted = true
	job.Backup = ""
	job.OldBinary = ""
	job.OldContainer = ""
	job.OldCompose = ""
	job.Snapshot = false
	job.Switched = false
	if err := m.persist(job); err != nil {
		return Job{}, err
	}
	return job, nil
}
