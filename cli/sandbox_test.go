package cli

import (
	"testing"

	"github.com/elek/acpp/db"
	"github.com/stretchr/testify/require"
)

func TestResolveSandboxSettings(t *testing.T) {
	tests := []struct {
		name         string
		flagType     string
		flagProfiles string
		project      db.ProjectRow
		defaultSbx   string
		wantType     string
		wantProfiles string
	}{
		{
			name:         "nothing set falls back to bbwrap",
			wantType:     "bbwrap",
			wantProfiles: "",
		},
		{
			name:         "global default used when no flag or project",
			defaultSbx:   "none",
			wantType:     "none",
			wantProfiles: "",
		},
		{
			name:         "stored project config overrides global default",
			project:      db.ProjectRow{Sandbox: "bbwrap", SandboxProfiles: "ssh,docker"},
			defaultSbx:   "none",
			wantType:     "bbwrap",
			wantProfiles: "ssh,docker",
		},
		{
			name:         "flag type overrides stored project config",
			flagType:     "none",
			project:      db.ProjectRow{Sandbox: "bbwrap"},
			wantType:     "none",
			wantProfiles: "",
		},
		{
			name:         "flag profiles replace project profiles",
			flagProfiles: "docker,ssh",
			project:      db.ProjectRow{Sandbox: "bbwrap", SandboxProfiles: "systemd"},
			wantType:     "bbwrap",
			wantProfiles: "docker,ssh",
		},
		{
			name:         "flag profiles used even without flag type",
			flagProfiles: "docker",
			wantType:     "bbwrap",
			wantProfiles: "docker",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotProfiles := resolveSandboxSettings(tt.flagType, tt.flagProfiles, tt.project, tt.defaultSbx)
			require.Equal(t, tt.wantType, gotType)
			require.Equal(t, tt.wantProfiles, gotProfiles)
		})
	}
}
