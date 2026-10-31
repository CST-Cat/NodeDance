// Package update owns release selection and transactional Core/Agent updates.
// Clients submit these same requests through the Core operation contract.
package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const Repository = "CST-Cat/NodeDance"
const ChannelBeta = "beta"

var tagPattern = regexp.MustCompile(`^(core|agent)-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Request struct {
	Component string `json:"component"`
	NodeID    string `json:"nodeId,omitempty"`
	Version   string `json:"version,omitempty"`
	JobID     string `json:"jobId,omitempty"`
	Confirm   bool   `json:"confirm,omitempty"`
}
type RPC struct {
	Action         string  `json:"action"`
	IdempotencyKey string  `json:"idempotencyKey,omitempty"`
	Request        Request `json:"request"`
}
type Response struct {
	Status int             `json:"status"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
}
type Target struct {
	AgentConfig      string `json:"agentConfig,omitempty"`
	Mode             string `json:"mode"`
	Container        string `json:"container,omitempty"`
	Binary           string `json:"binary,omitempty"`
	Unit             string `json:"unit,omitempty"`
	DataDir          string `json:"dataDir"`
	SourceDir        string `json:"sourceDir,omitempty"`
	HealthCAFile     string `json:"healthCaFile,omitempty"`
	HealthServerName string `json:"healthServerName,omitempty"`
	HealthURL        string `json:"healthUrl,omitempty"`
	ComposeFile      string `json:"composeFile,omitempty"`
}
type Config struct {
	StateDir string            `json:"stateDir"`
	Targets  map[string]Target `json:"targets"`
}
type Release struct {
	Component   string `json:"component"`
	Version     string `json:"version"`
	URL         string `json:"url"`
	Notes       string `json:"notes"`
	BinaryURL   string `json:"-"`
	ChecksumURL string `json:"-"`
}
type Check struct {
	Component  string  `json:"component"`
	Current    string  `json:"current"`
	Latest     string  `json:"latest"`
	Available  bool    `json:"available"`
	Configured bool    `json:"configured"`
	Mode       string  `json:"mode,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	Release    Release `json:"release"`
}
type Job struct {
	ID              string     `json:"id"`
	Component       string     `json:"component"`
	Mode            string     `json:"mode"`
	Current         string     `json:"current"`
	Target          string     `json:"target"`
	Status          string     `json:"status"`
	Stage           string     `json:"stage"`
	Progress        float64    `json:"progress"`
	Error           string     `json:"error,omitempty"`
	Started         time.Time  `json:"started"`
	Finished        *time.Time `json:"finished,omitempty"`
	Parent          string     `json:"parent,omitempty"`
	Backup          string     `json:"backup,omitempty"`
	BackupDeleting  bool       `json:"backupDeleting,omitempty"`
	BackupDeleted   bool       `json:"backupDeleted,omitempty"`
	AgentGeneration uint64     `json:"-"`
	OldContainer    string     `json:"-"`
	NewContainer    string     `json:"-"`
	OldBinary       string     `json:"-"`
	OldCompose      string     `json:"-"`
	Snapshot        bool       `json:"-"`
	Switched        bool       `json:"-"`
	Source          bool       `json:"-"`
	IdempotencyKey  string     `json:"-"`
}

// Journal embeds public state and durable recovery details without exposing host paths.
type journal struct {
	AgentGeneration uint64
	Job
	OldContainer   string
	NewContainer   string
	OldBinary      string
	OldCompose     string
	Snapshot       bool
	Switched       bool
	Source         bool
	IdempotencyKey string
}

func (j Job) record() journal {
	return journal{Job: j, AgentGeneration: j.AgentGeneration, OldContainer: j.OldContainer, NewContainer: j.NewContainer, OldBinary: j.OldBinary, OldCompose: j.OldCompose, Snapshot: j.Snapshot, Switched: j.Switched, Source: j.Source, IdempotencyKey: j.IdempotencyKey}
}

func (j journal) job() Job {
	j.Job.AgentGeneration = j.AgentGeneration
	j.Job.OldContainer = j.OldContainer
	j.Job.NewContainer = j.NewContainer
	j.Job.OldBinary = j.OldBinary
	j.Job.OldCompose = j.OldCompose
	j.Job.Snapshot = j.Snapshot
	j.Job.Switched = j.Switched
	j.Job.Source = j.Source
	j.Job.IdempotencyKey = j.IdempotencyKey
	return j.Job
}
func Validate(r Request) error {
	if r.Component != "core" && r.Component != "agent" {
		return errors.New("component must be core or agent")
	}
	if r.Version == ChannelBeta {
		if r.Component != "core" {
			return errors.New("the beta channel is only available for Core")
		}
	} else if r.Version != "" && !ValidTag(r.Component, r.Version) {
		return errors.New("invalid component release tag")
	}
	if r.JobID != "" && !idPattern.MatchString(r.JobID) {
		return errors.New("invalid update job ID")
	}
	return nil
}
func ValidTag(component, tag string) bool {
	m := tagPattern.FindStringSubmatch(tag)
	if len(m) == 0 || m[1] != component {
		return false
	}
	for i := 2; i < 5; i++ {
		if _, err := strconv.ParseUint(m[i], 10, 64); err != nil {
			return false
		}
	}
	return true
}
func Compare(a, b string) int {
	aa, bb := tagPattern.FindStringSubmatch(a), tagPattern.FindStringSubmatch(b)
	if len(aa) == 0 || len(bb) == 0 {
		return 0
	}
	for i := 2; i < 5; i++ {
		x, _ := strconv.ParseUint(aa[i], 10, 64)
		y, _ := strconv.ParseUint(bb[i], 10, 64)
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}
func Decode(raw []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}
