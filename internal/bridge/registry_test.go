package bridge

import (
	"testing"

	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
)

// mockModule implements Module for testing.
type mockModule struct {
	name    string
	handled bool
	lastSID string
	lastTID uint32
	lastCtx ModuleContext
}

func (m *mockModule) Name() string { return m.name }
func (m *mockModule) Handle(ctx ModuleContext, sessionID string, taskID uint32, spite *implantpb.Spite) {
	m.handled = true
	m.lastSID = sessionID
	m.lastTID = taskID
	m.lastCtx = ctx
}

func TestRegistry_Register_And_Get(t *testing.T) {
	r := NewRegistry()
	m := &mockModule{name: "test"}
	r.Register(m)

	got := r.Get("test")
	if got != m {
		t.Error("Get returned wrong module")
	}
	if r.Get("nonexistent") != nil {
		t.Error("Get should return nil for unknown module")
	}
}

func TestRegistry_Names_Order(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockModule{name: "alpha"})
	r.Register(&mockModule{name: "beta"})
	r.Register(&mockModule{name: "gamma"})

	names := r.Names()
	expected := []string{"alpha", "beta", "gamma"}
	if len(names) != len(expected) {
		t.Fatalf("names count: got %d, want %d", len(names), len(expected))
	}
	for i := range expected {
		if names[i] != expected[i] {
			t.Errorf("names[%d]: got %q, want %q", i, names[i], expected[i])
		}
	}
}

func TestRegistry_Names_ReturnsCopy(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockModule{name: "alpha"})

	names := r.Names()
	names[0] = "mutated"

	// Original should be unaffected.
	if r.Names()[0] != "alpha" {
		t.Error("Names should return a copy, not a reference to internal state")
	}
}

func TestRegistry_Dispatch_RoutesToCorrectModule(t *testing.T) {
	r := NewRegistry()
	m1 := &mockModule{name: "exec"}
	m2 := &mockModule{name: "upload"}
	r.Register(m1)
	r.Register(m2)

	ctx := ModuleContext{ListenerID: "test", Tasks: NewTaskManager()}
	spite := &implantpb.Spite{Name: "upload"}

	ok := r.Dispatch(ctx, "sess-1", 42, spite)
	if !ok {
		t.Error("dispatch should succeed")
	}
	if m1.handled {
		t.Error("exec should not be called")
	}
	if !m2.handled {
		t.Error("upload should be called")
	}
	if m2.lastSID != "sess-1" {
		t.Errorf("sessionID: got %q, want %q", m2.lastSID, "sess-1")
	}
	if m2.lastTID != 42 {
		t.Errorf("taskID: got %d, want %d", m2.lastTID, 42)
	}
}

func TestRegistry_Dispatch_UnknownModule(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockModule{name: "exec"})

	ok := r.Dispatch(ModuleContext{}, "sess-1", 1, &implantpb.Spite{Name: "nonexistent"})
	if ok {
		t.Error("dispatch should fail for unknown module")
	}
}

func TestRegistry_DuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockModule{name: "exec"})

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate registration")
		}
	}()
	r.Register(&mockModule{name: "exec"})
}

// TestRegistry_Names_MatchesOriginalList is a REGRESSION test that ensures
// the refactored module list exactly matches the original hardcoded list
// from register.go:23-44.
func TestRegistry_Names_MatchesOriginalList(t *testing.T) {
	expected := []string{
		"exec", "agent", "tapping", "tapping_off",
		"upload", "download",
		"netstat", "ps", "ls", "whoami", "pwd", "cat", "env",
		"kill", "mkdir", "rm", "cp", "mv", "cd", "chmod",
	}

	r := buildDefaultRegistry()
	names := r.Names()

	if len(names) != len(expected) {
		t.Fatalf("module count: got %d, want %d\ngot:  %v\nwant: %v", len(names), len(expected), names, expected)
	}
	for i := range expected {
		if names[i] != expected[i] {
			t.Errorf("module[%d]: got %q, want %q", i, names[i], expected[i])
		}
	}
}
