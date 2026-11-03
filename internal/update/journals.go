package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func loadJournals(state string) (map[string]Job, error) {
	jobs := map[string]Job{}
	entries, err := os.ReadDir(state)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") || !idPattern.MatchString(id) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(state, entry.Name()))
		if err != nil {
			continue
		}
		var record journal
		if Decode(raw, &record) != nil || record.ID != id || record.Component != "core" && record.Component != "agent" {
			continue
		}
		jobs[id] = record.job()
	}
	return jobs, nil
}
