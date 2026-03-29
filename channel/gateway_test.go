package channel

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/config"
	"github.com/elek/acpp/db"
	"github.com/stretchr/testify/require"
)

func TestAutoCreateSessionCreatesProject(t *testing.T) {
	store := db.NewMemStore()
	cfg := &config.Config{
		Defaults: config.Defaults{
			Agent: "<STUB>",
		},
	}
	sm := acp.NewSessionManager()
	cm := NewChannelManager()

	ch := &StubChannel{}
	cm.Register("test", func(ctx context.Context) (Channel, error) {
		return ch, nil
	})
	require.NoError(t, cm.Start(context.Background()))

	gw := NewGateway(cfg, store, store, sm, cm)

	cs := ChannelSource{ChannelID: "test", SourceID: "my-project"}

	// Send a message — this triggers autoCreateSession which resolves params
	// and creates the session (and project).
	err := gw.OnMessageReceived(cs, "hello", nil)
	require.NoError(t, err)

	// The project should have been auto-created via GetProject.
	proj, err := store.GetProject(context.Background(), "my-project")
	require.NoError(t, err)
	require.Equal(t, "my-project", proj.Name)

	// A session should be persisted in the store.
	sessions, err := store.ListSessions(context.Background())
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "<STUB>", sessions[0].Agent)
}

func TestAutoCreateSessionPersistsSearchPathDir(t *testing.T) {
	// Create a temp directory to use as search_path base.
	dir := t.TempDir()

	// Create a subdirectory matching the channel name.
	projectDir := dir + "/my-project"
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	store := db.NewMemStore()
	cfg := &config.Config{
		Defaults: config.Defaults{
			Agent: "<STUB>",
		},
		SearchPath: []string{dir},
	}
	sm := acp.NewSessionManager()
	cm := NewChannelManager()

	ch := &StubChannel{}
	cm.Register("test", func(ctx context.Context) (Channel, error) {
		return ch, nil
	})
	require.NoError(t, cm.Start(context.Background()))

	gw := NewGateway(cfg, store, store, sm, cm)

	cs := ChannelSource{ChannelID: "test", SourceID: "my-project"}

	err := gw.OnMessageReceived(cs, "hello", nil)
	require.NoError(t, err)

	// The resolved search_path directory should be persisted on the project.
	proj, err := store.GetProject(context.Background(), "my-project")
	require.NoError(t, err)
	require.Equal(t, projectDir, proj.Dir)

	// The session should use the same directory.
	sessions, err := store.ListSessions(context.Background())
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, projectDir, sessions[0].Dir)
}

func TestAutoCreateSessionUsesPersistedDir(t *testing.T) {
	store := db.NewMemStore()

	// Pre-set the project dir before the session is created.
	require.NoError(t, store.SetProjectField(context.Background(), "my-project", "dir", "/preset/path"))

	cfg := &config.Config{
		Defaults: config.Defaults{
			Agent: "<STUB>",
		},
	}
	sm := acp.NewSessionManager()
	cm := NewChannelManager()

	ch := &StubChannel{}
	cm.Register("test", func(ctx context.Context) (Channel, error) {
		return ch, nil
	})
	require.NoError(t, cm.Start(context.Background()))

	gw := NewGateway(cfg, store, store, sm, cm)

	cs := ChannelSource{ChannelID: "test", SourceID: "my-project"}

	err := gw.OnMessageReceived(cs, "hello", nil)
	require.NoError(t, err)

	// The session should use the pre-set directory.
	sessions, err := store.ListSessions(context.Background())
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "/preset/path", sessions[0].Dir)
}

func TestSecondMessageReusesSameSession(t *testing.T) {
	store := db.NewMemStore()
	cfg := &config.Config{
		Defaults: config.Defaults{
			Agent: "<STUB>",
		},
	}
	sm := acp.NewSessionManager()
	cm := NewChannelManager()

	ch := &StubChannel{}
	cm.Register("test", func(ctx context.Context) (Channel, error) {
		return ch, nil
	})
	require.NoError(t, cm.Start(context.Background()))

	gw := NewGateway(cfg, store, store, sm, cm)
	cs := ChannelSource{ChannelID: "test", SourceID: "my-project"}

	require.NoError(t, gw.OnMessageReceived(cs, "first", nil))

	// Wait briefly for the relay to be ready before sending second message.
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, gw.OnMessageReceived(cs, "second", nil))

	// Only one session should exist — the second message reuses it.
	sessions, err := store.ListSessions(context.Background())
	require.NoError(t, err)
	require.Len(t, sessions, 1)
}


func TestParseShellCommand(t *testing.T) {
	tests := []struct {
		input       string
		wantCmd     string
		wantSandbox bool
		wantIsShell bool
	}{
		{"!ls -la", "ls -la", true, true},
		{"!!ls -la", "ls -la", false, true},
		{"hello world", "", false, false},
		{"  !pwd", "pwd", true, true},
		{"  !!echo hi", "echo hi", false, true},
		{"! spaced", " spaced", true, true},
		{"", "", false, false},
		{"/start", "", false, false},
	}
	for _, tt := range tests {
		cmd, sandbox, isShell := parseShellCommand(tt.input)
		require.Equal(t, tt.wantCmd, cmd, "input: %q", tt.input)
		require.Equal(t, tt.wantSandbox, sandbox, "input: %q", tt.input)
		require.Equal(t, tt.wantIsShell, isShell, "input: %q", tt.input)
	}
}

func TestExecuteShellCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := executeShellCommand(ctx, "echo hello", "", "", true)
	require.Contains(t, result, "$ echo hello")
	require.Contains(t, result, "hello")
}
