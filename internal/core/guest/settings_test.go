package guest

import "testing"

func TestPublicationRequiresExplicitSafeSelections(t *testing.T) {
	defaults := Defaults()
	if err := Validate(defaults); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Settings)
	}{
		{"enabled without content", func(s *Settings) { s.Enabled = true }},
		{"unpublished metric", func(s *Settings) { s.Metrics = []string{"system.hostname"} }},
		{"duplicate metric", func(s *Settings) { s.Metrics = []string{"cpu.usage_percent", "cpu.usage_percent"} }},
		{"history without a historical metric", func(s *Settings) { s.Metrics = []string{"uptime.seconds"}; s.HistoryHours = 1 }},
		{"unbounded history", func(s *Settings) { s.Metrics = []string{"cpu.usage_percent"}; s.HistoryHours = 8760 }},
		{"implicit private name", func(s *Settings) { s.Nodes = []Resource{{ID: "node", Name: ""}} }},
		{"duplicate resource", func(s *Settings) { s.Nodes = []Resource{{ID: "node", Name: "Public"}, {ID: "node", Name: "Other"}} }},
		{"invalid revision", func(s *Settings) { s.Revision = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Defaults()
			tc.change(&s)
			if Validate(s) == nil {
				t.Fatal("unsafe selection accepted", s)
			}
		})
	}
}
