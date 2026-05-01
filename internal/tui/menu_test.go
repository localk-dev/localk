package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMenu_NavigationClampsAtEnds(t *testing.T) {
	m := newMenuModel()
	if m.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.cursor)
	}
	// up at top stays at 0
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.cursor != 0 {
		t.Errorf("up at top: cursor = %d, want 0", m.cursor)
	}
	// walk to bottom
	for range m.items {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.cursor != len(m.items)-1 {
		t.Errorf("after walking down: cursor = %d, want %d", m.cursor, len(m.items)-1)
	}
	// down at bottom stays at bottom
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.cursor != len(m.items)-1 {
		t.Errorf("down at bottom: cursor = %d, want %d", m.cursor, len(m.items)-1)
	}
}

// TestMenu_GHomeJumpToEnds verifies the dashboard's g/G shortcuts
// also work in the menu — keeps the keybinding model consistent
// across screens.
func TestMenu_GHomeJumpToEnds(t *testing.T) {
	m := newMenuModel()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if m.cursor != len(m.items)-1 {
		t.Errorf("G: cursor = %d, want %d", m.cursor, len(m.items)-1)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if m.cursor != 0 {
		t.Errorf("g: cursor = %d, want 0", m.cursor)
	}
}

// TestMenu_EnterCapturesAction is the main contract: pressing Enter
// records which action was chosen so the parent Model can route.
// Each item maps to its declared menuAction; the test pins the
// mapping so the parent's switch in activate() can rely on it.
func TestMenu_EnterCapturesAction(t *testing.T) {
	cases := []struct {
		cursor int
		want   menuAction
	}{
		{0, menuActionGenerate},
		{1, menuActionDashboard},
		{2, menuActionUp},
		{3, menuActionDown},
	}
	for _, tc := range cases {
		m := newMenuModel()
		m.cursor = tc.cursor
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if m.chosen == nil {
			t.Errorf("cursor=%d: chosen is nil after Enter", tc.cursor)
			continue
		}
		if *m.chosen != tc.want {
			t.Errorf("cursor=%d: chosen = %v, want %v", tc.cursor, *m.chosen, tc.want)
		}
	}
}

// TestMenu_ResetClearsChoice ensures re-entering the menu doesn't
// re-trigger the previous selection. Without this, after returning
// from the dashboard the parent would loop back into the dashboard
// because m.menu.chosen still holds the last value.
func TestMenu_ResetClearsChoice(t *testing.T) {
	m := newMenuModel()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.chosen == nil {
		t.Fatalf("expected chosen set after Enter")
	}
	m.reset()
	if m.chosen != nil {
		t.Errorf("after reset: chosen = %v, want nil", m.chosen)
	}
}
