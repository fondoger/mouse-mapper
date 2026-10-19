package desktop

import (
	"context"
	"errors"
	"testing"

	"github.com/fondoger/mouse-mapper/internal/inputmethod"
)

func TestInputMethodDiscoveryIsReadOnlyAndDetached(t *testing.T) {
	m, store, engine, host := mapperFixture(t)
	m.listMethods = func(ctx context.Context) ([]inputmethod.Method, error) {
		if _, bounded := ctx.Deadline(); !bounded {
			t.Fatal("input-method discovery has no timeout")
		}
		return []inputmethod.Method{
			{ID: "test:wechat", Name: "WeChat"}, {ID: "test:doubao", Name: "Doubao"},
			{ID: "test:wechat", Name: "duplicate"}, {Name: "no ID"}, {ID: "bad\nID", Name: "invalid"},
		}, nil
	}
	before := engine.snapshot()
	state := m.RefreshInputMethods()
	if len(state.InputMethods) != 2 || state.InputMethods[0].ID != "test:doubao" || state.InputMethods[1].ID != "test:wechat" {
		t.Fatalf("unexpected discovered list: %+v", state.InputMethods)
	}
	if store.saves != 0 || len(host.loginCalls) != 0 || engine.snapshot().Back.InputMethod != before.Back.InputMethod {
		t.Fatal("read-only discovery changed preferences, startup, or input preparation")
	}
	state.InputMethods[0].Name = "mutated by caller"
	if m.GetState().InputMethods[0].Name == "mutated by caller" {
		t.Fatal("state exposes mutable input-method storage")
	}
}

func TestInputMethodDiscoveryErrorDoesNotPauseMapping(t *testing.T) {
	m, _, _, _ := mapperFixture(t)
	m.listMethods = func(context.Context) ([]inputmethod.Method, error) { return nil, errors.New("discovery failed") }
	state := m.RefreshInputMethods()
	if state.InputMethodsError != "discovery failed" || !state.Settings.Enabled || !state.Status.Running {
		t.Fatalf("discovery error changed mapping state: %+v", state)
	}
}

func TestChangingOneInputMethodPreservesOtherBinding(t *testing.T) {
	m, _, engine, _ := mapperFixture(t)
	state, err := m.ChangeSettings(func(settings *Settings) {
		settings.Back.InputMethod, settings.Back.InputMethodName = "test:wechat", "WeChat"
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.Forward.InputMethod != "" || engine.snapshot().Back.InputMethod != "test:wechat" {
		t.Fatal("per-button preparation was not propagated independently")
	}
	if err := m.Preview("back"); err != nil {
		t.Fatal(err)
	}
	if engine.snapshot().Back.InputMethod != "test:wechat" {
		t.Fatal("overlay preview changed input method configuration")
	}
}
