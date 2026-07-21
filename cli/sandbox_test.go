package cli

import (
	"testing"

	"github.com/elek/acpp/config"
	"github.com/stretchr/testify/require"
)

func TestResolveSandboxSettings(t *testing.T) {
	tests := []struct {
		name         string
		flagType     string
		flagProfiles string
		pc           config.ProjectConfig
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
			name:         "project config overrides global default",
			pc:           config.ProjectConfig{Sandbox: config.ProjectSandbox{Name: "bbwrap", Profiles: "ssh,docker"}},
			defaultSbx:   "none",
			wantType:     "bbwrap",
			wantProfiles: "ssh,docker",
		},
		{
			name:         "flag type overrides project config",
			flagType:     "none",
			pc:           config.ProjectConfig{Sandbox: config.ProjectSandbox{Name: "bbwrap"}},
			wantType:     "none",
			wantProfiles: "",
		},
		{
			name:         "flag profiles replace project profiles",
			flagProfiles: "docker,ssh",
			pc:           config.ProjectConfig{Sandbox: config.ProjectSandbox{Name: "bbwrap", Profiles: "systemd"}},
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
			gotType, gotProfiles := resolveSandboxSettings(tt.flagType, tt.flagProfiles, tt.pc, tt.defaultSbx)
			require.Equal(t, tt.wantType, gotType)
			require.Equal(t, tt.wantProfiles, gotProfiles)
		})
	}
}
