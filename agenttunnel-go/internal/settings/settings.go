// Package settings holds runtime-editable application settings. The orchestrator
// reads it on every dispatch and clients write it over RPC, so changes take
// effect on the next turn without a restart.
//
// Settings persist to a JSON file (by default <home>/.agenttunnel/settings.json)
// so they survive a restart. Every mutation rewrites the file atomically.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// RoleConfig overrides a role's default runtime (harness/model/variant).
type RoleConfig struct {
	Harness contracts.Harness
	Model   string
	Variant string
}

// Project is a workspace rooted at a directory. Threads belong to a project, and
// a project's path is the working directory handed to its agents.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// Store is safe for concurrent use.
type Store struct {
	mu            sync.RWMutex
	path          string
	workdir       string
	autoMode      bool
	approved      map[string]struct{}
	roles         map[contracts.RoleID]RoleConfig
	disabled      map[contracts.Harness]bool
	custom        map[contracts.Harness][]string
	projects      []Project
	staleAfterMin int
	relay         Relay
}

// Relay is the outbound relay configuration. The zero value is off: the daemon
// opens no relay socket until a client enables it.
type Relay struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
}

const (
	defaultStaleAfterMinutes = 30
	minStaleAfterMinutes     = 1
	maxStaleAfterMinutes     = 30 * 24 * 60
)

type fileConfig struct {
	Workdir           string                    `json:"workdir"`
	AutoMode          bool                      `json:"autoMode"`
	ApprovedModels    []string                  `json:"approvedModels"`
	Roles             map[string]fileRoleConfig `json:"roles"`
	DisabledHarnesses []string                  `json:"disabledHarnesses"`
	CustomModels      map[string][]string       `json:"customModels"`
	Projects          []Project                 `json:"projects"`
	StaleAfterMinutes int                       `json:"staleAfterMinutes"`
	Relay             Relay                     `json:"relay"`
}

type fileRoleConfig struct {
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Variant string `json:"variant"`
}

// New returns an in-memory store seeded with a default working directory. It does
// not persist; use Open for a file-backed store.
func New(workdir string) *Store {
	return &Store{
		workdir:       workdir,
		approved:      map[string]struct{}{},
		roles:         map[contracts.RoleID]RoleConfig{},
		disabled:      map[contracts.Harness]bool{},
		custom:        map[contracts.Harness][]string{},
		staleAfterMin: defaultStaleAfterMinutes,
	}
}

// Open loads settings from path, or returns defaults seeded with defaultWorkdir
// when the file does not exist yet.
func Open(path, defaultWorkdir string) (*Store, error) {
	s := New(defaultWorkdir)
	s.path = path

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var fc fileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return nil, err
	}
	s.apply(fc)
	return s, nil
}

// Path is the file settings persist to (empty for an in-memory store).
func (s *Store) Path() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

func (s *Store) apply(fc fileConfig) {
	if wd := strings.TrimSpace(fc.Workdir); wd != "" {
		s.workdir = wd
	}
	s.autoMode = fc.AutoMode
	// A missing or zero value keeps the default; only explicit values persist.
	if fc.StaleAfterMinutes != 0 {
		s.staleAfterMin = clampStaleMinutes(fc.StaleAfterMinutes)
	}

	s.approved = make(map[string]struct{}, len(fc.ApprovedModels))
	for _, m := range fc.ApprovedModels {
		if m = strings.TrimSpace(m); m != "" {
			s.approved[m] = struct{}{}
		}
	}

	s.roles = make(map[contracts.RoleID]RoleConfig, len(fc.Roles))
	for id, rc := range fc.Roles {
		s.roles[contracts.RoleID(id)] = RoleConfig{
			Harness: contracts.Harness(rc.Harness),
			Model:   rc.Model,
			Variant: rc.Variant,
		}
	}

	s.disabled = make(map[contracts.Harness]bool, len(fc.DisabledHarnesses))
	for _, h := range fc.DisabledHarnesses {
		s.disabled[contracts.Harness(h)] = true
	}

	s.custom = make(map[contracts.Harness][]string, len(fc.CustomModels))
	for h, list := range fc.CustomModels {
		s.custom[contracts.Harness(h)] = append([]string(nil), list...)
	}

	s.projects = append([]Project(nil), fc.Projects...)
	s.relay = fc.Relay
}

// --- reads ---

// Workdir is the fallback directory handed to agents when a thread has no project.
func (s *Store) Workdir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.workdir
}

// AutoMode reports whether the orchestrator picks each task's model.
func (s *Store) AutoMode() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.autoMode
}

// ApprovedModels returns the models auto mode is allowed to dispatch.
func (s *Store) ApprovedModels() map[string]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]struct{}, len(s.approved))
	for k := range s.approved {
		out[k] = struct{}{}
	}
	return out
}

// RoleConfig returns a role's override, if any.
func (s *Store) RoleConfig(id contracts.RoleID) (RoleConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg, ok := s.roles[id]
	return cfg, ok
}

// HarnessEnabled reports whether a harness may be used. Harnesses are enabled
// unless explicitly disabled.
func (s *Store) HarnessEnabled(h contracts.Harness) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.disabled[h]
}

// CustomModels returns the models a user added by hand for a harness.
func (s *Store) CustomModels(h contracts.Harness) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.custom[h]...)
}

// StaleAfterMinutes is how long a thread can sit idle before the UI marks it
// stale. A thread with running agents never goes stale.
func (s *Store) StaleAfterMinutes() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.staleAfterMin == 0 {
		return defaultStaleAfterMinutes
	}
	return s.staleAfterMin
}

// SetStaleAfterMinutes updates the staleness threshold, clamped to one minute
// and thirty days.
func (s *Store) SetStaleAfterMinutes(minutes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.staleAfterMin = clampStaleMinutes(minutes)
	s.persistLocked()
}

func clampStaleMinutes(minutes int) int {
	if minutes < minStaleAfterMinutes {
		return minStaleAfterMinutes
	}
	if minutes > maxStaleAfterMinutes {
		return maxStaleAfterMinutes
	}
	return minutes
}

// Relay returns the outbound relay configuration.
func (s *Store) Relay() Relay {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.relay
}

// SetRelay replaces the outbound relay configuration.
func (s *Store) SetRelay(r Relay) {
	r.URL = strings.TrimSpace(r.URL)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.relay = r
	s.persistLocked()
}

// Projects returns the configured projects.
func (s *Store) Projects() []Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Project(nil), s.projects...)
}

// Project returns a project by id.
func (s *Store) Project(id string) (Project, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.projects {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

// --- writes (each persists) ---

// SetWorkdir updates the fallback working directory.
func (s *Store) SetWorkdir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workdir = strings.TrimSpace(dir)
	s.persistLocked()
}

// SetAutoMode toggles auto mode.
func (s *Store) SetAutoMode(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoMode = v
	s.persistLocked()
}

// SetApprovedModels replaces the approved-model allowlist.
func (s *Store) SetApprovedModels(models []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approved = make(map[string]struct{}, len(models))
	for _, m := range models {
		if m = strings.TrimSpace(m); m != "" {
			s.approved[m] = struct{}{}
		}
	}
	s.persistLocked()
}

// SetRoleConfig sets a role's runtime override.
func (s *Store) SetRoleConfig(id contracts.RoleID, cfg RoleConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[id] = cfg
	s.persistLocked()
}

// SetHarnessEnabled enables or disables a harness.
func (s *Store) SetHarnessEnabled(h contracts.Harness, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if enabled {
		delete(s.disabled, h)
	} else {
		s.disabled[h] = true
	}
	s.persistLocked()
}

// AddModel records a user-provided model for a harness, ignoring duplicates.
func (s *Store) AddModel(h contracts.Harness, model string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.custom[h] {
		if m == model {
			return
		}
	}
	s.custom[h] = append(s.custom[h], model)
	s.persistLocked()
}

// AddProject registers a project, replacing any existing one with the same id.
func (s *Store) AddProject(p Project) {
	p.ID = strings.TrimSpace(p.ID)
	p.Name = strings.TrimSpace(p.Name)
	p.Path = strings.TrimSpace(p.Path)
	if p.ID == "" || p.Name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.projects {
		if existing.ID == p.ID {
			s.projects[i] = p
			s.persistLocked()
			return
		}
	}
	s.projects = append(s.projects, p)
	s.persistLocked()
}

// RemoveProject deletes a project by id. Its threads are left in place but become
// unreachable from the projects view.
func (s *Store) RemoveProject(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]Project, 0, len(s.projects))
	for _, p := range s.projects {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	s.projects = kept
	s.persistLocked()
}

// --- persistence ---

func (s *Store) snapshotLocked() fileConfig {
	fc := fileConfig{
		Workdir:           s.workdir,
		AutoMode:          s.autoMode,
		ApprovedModels:    make([]string, 0, len(s.approved)),
		Roles:             make(map[string]fileRoleConfig, len(s.roles)),
		DisabledHarnesses: make([]string, 0, len(s.disabled)),
		CustomModels:      make(map[string][]string, len(s.custom)),
		Projects:          append([]Project(nil), s.projects...),
		StaleAfterMinutes: s.staleAfterMin,
		Relay:             s.relay,
	}
	for m := range s.approved {
		fc.ApprovedModels = append(fc.ApprovedModels, m)
	}
	for id, rc := range s.roles {
		fc.Roles[string(id)] = fileRoleConfig{Harness: string(rc.Harness), Model: rc.Model, Variant: rc.Variant}
	}
	for h := range s.disabled {
		fc.DisabledHarnesses = append(fc.DisabledHarnesses, string(h))
	}
	for h, list := range s.custom {
		fc.CustomModels[string(h)] = append([]string(nil), list...)
	}
	return fc
}

// persistLocked writes the settings file atomically. Callers hold the lock; a
// write failure is non-fatal (the in-memory value is still applied).
func (s *Store) persistLocked() {
	if s.path == "" {
		return
	}
	data, err := json.MarshalIndent(s.snapshotLocked(), "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "settings-*.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return
	}
	_ = os.Rename(name, s.path)
}
