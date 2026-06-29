package hook

import (
	"errors"
	"testing"

	"github.com/elek/acpp/config"
	"github.com/stretchr/testify/require"
)

type fakeHook struct{ params map[string]string }

func (f *fakeHook) Outgoing(hc HookContext, msg any) any { return msg }
func (f *fakeHook) Incoming(hc HookContext, msg any) any { return msg }

func TestBuild_KnownTypeWithParams(t *testing.T) {
	Register("test-build", func(p map[string]string) (Hook, error) {
		return &fakeHook{params: p}, nil
	})

	hooks, err := Build([]config.HookConfig{
		{Type: "test-build", Params: map[string]string{"k": "v"}},
	})
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	require.Equal(t, map[string]string{"k": "v"}, hooks[0].(*fakeHook).params)
}

func TestBuild_UnknownType(t *testing.T) {
	_, err := Build([]config.HookConfig{{Type: "nope"}})
	require.Error(t, err)
}

func TestBuild_FactoryError(t *testing.T) {
	Register("test-build-err", func(p map[string]string) (Hook, error) {
		return nil, errors.New("boom")
	})
	_, err := Build([]config.HookConfig{{Type: "test-build-err"}})
	require.Error(t, err)
}

func TestRegister_DuplicatePanics(t *testing.T) {
	Register("test-dup", func(p map[string]string) (Hook, error) { return &fakeHook{}, nil })
	require.Panics(t, func() {
		Register("test-dup", func(p map[string]string) (Hook, error) { return &fakeHook{}, nil })
	})
}
