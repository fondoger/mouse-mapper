package desktop

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/fondoger/mouse-mapper/internal/inputmethod"
)

// RefreshInputMethods is read-only. Run it outside the UI frame; macOS TIS work
// is marshalled back to the application's main thread by the native backend.
func (m *Mapper) RefreshInputMethods() State {
	m.methodMu.Lock()
	defer m.methodMu.Unlock()
	if m.stopping.Load() {
		return m.GetState()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	methods, err := m.listMethods(ctx)
	clean := make([]inputmethod.Method, 0, len(methods))
	seen := make(map[string]bool)
	for _, method := range methods {
		if method.ID == "" || seen[method.ID] || inputmethod.ValidateSelection(method.ID, method.Name) != nil {
			continue
		}
		seen[method.ID] = true
		if strings.TrimSpace(method.Name) == "" {
			method.Name = method.ID
		}
		clean = append(clean, method)
	}
	slices.SortStableFunc(clean, func(a, b inputmethod.Method) int {
		if result := strings.Compare(a.Name, b.Name); result != 0 {
			return result
		}
		return strings.Compare(a.ID, b.ID)
	})
	m.mu.Lock()
	m.inputMethods, m.methodError = clean, ""
	if err != nil {
		m.methodError = err.Error()
	}
	m.mu.Unlock()
	m.publish()
	return m.GetState()
}
