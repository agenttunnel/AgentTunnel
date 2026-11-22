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
	"slices"
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
	approved      map[contracts.Harness]map[string]struct{}
	roles         map[contracts.RoleID]RoleConfig
	disabled      map[contracts.Harness]bool
	disabledRoles map[contracts.RoleID]bool
	experimental  bool
	custom        map[contracts.Harness][]string
	projects      []Project
	staleAfterMin int
	relay         Relay
	// masterRuntime overrides the master chat's harness/model/variant,
	// separately from the Orchestrator role's runtime that thread leads use.
	masterRuntime  *RoleConfig
	masterReporter bool
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
	Workdir  string `json:"workdir"`
	AutoMode bool   `json:"autoMode"`
	// ApprovedModels is the legacy flat allowlist, read for migration only. It
	// predates harness keys, so each model was approved for every harness.
	ApprovedModels    []string                  `json:"approvedModels,omitempty"`
	ApprovedByHarness map[string][]string       `json:"approvedByHarness,omitempty"`
	Roles             map[string]fileRoleConfig `json:"roles"`
	DisabledHarnesses []string                  `json:"disabledHarnesses"`
	DisabledRoles     []string                  `json:"disabledRoles"`
	ExperimentalRoles bool                      `json:"experimentalRoles"`
	CustomModels      map[string][]string       `json:"customModels"`
	Projects          []Project                 `json:"projects"`
	StaleAfterMinutes int                       `json:"staleAfterMinutes"`
	Relay             Relay                     `json:"relay"`
	MasterRuntime     *fileMasterRuntimeConfig  `json:"masterRuntime,omitempty"`
	MasterReporter    bool                      `json:"masterReporter,omitempty"`
}

type fileRoleConfig struct {
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Variant string `json:"variant"`
}

// fileMasterRuntimeConfig is settings.json's persisted master runtime override.
type fileMasterRuntimeConfig struct {
	Harness string `json:"harness,omitempty"`
	Model   string `json:"model,omitempty"`
	Variant string `json:"variant,omitempty"`
}

// New returns an in-memory store seeded with a default working directory. It does
// not persist; use Open for a file-backed store.
func New(workdir string) *Store {
	return &Store{
		workdir:       workdir,
		approved:      map[contracts.Harness]map[string]struct{}{},
		roles:         map[contracts.RoleID]RoleConfig{},
		disabled:      map[contracts.Harness]bool{},
		disabledRoles: map[contracts.RoleID]bool{},
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

	s.approved = make(map[contracts.Harness]map[string]struct{}, len(fc.ApprovedByHarness))
	for h, models := range fc.ApprovedByHarness {
		s.setApprovedLocked(contracts.Harness(h), models)
	}
	if len(fc.ApprovedByHarness) == 0 {
		for _, h := range contracts.AllHarnesses() {
			s.setApprovedLocked(h, fc.ApprovedModels)
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

	s.disabledRoles = make(map[contracts.RoleID]bool, len(fc.DisabledRoles))
	for _, id := range fc.DisabledRoles {
		s.disabledRoles[contracts.RoleID(id)] = true
	}
	s.experimental = fc.ExperimentalRoles

	s.custom = make(map[contracts.Harness][]string, len(fc.CustomModels))
	for h, list := range fc.CustomModels {
		s.custom[contracts.Harness(h)] = append([]string(nil), list...)
	}

	s.projects = append([]Project(nil), fc.Projects...)
	s.relay = fc.Relay
	if fc.MasterRuntime != nil {
		rc := RoleConfig{
			Harness: contracts.Harness(fc.MasterRuntime.Harness),
			Model:   fc.MasterRuntime.Model,
			Variant: fc.MasterRuntime.Variant,
		}
		s.masterRuntime = &rc
	}
	s.masterReporter = fc.MasterReporter
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

// ApprovedModels returns the union of every harness's approved models. It is
// the flat view clients list; use ApprovedModelsFor when the harness matters.
func (s *Store) ApprovedModels() map[string]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]struct{}{}
	for _, models := range s.approved {
		for m := range models {
			out[m] = struct{}{}
		}
	}
	return out
}

// ApprovedModelsFor returns the models auto mode may dispatch on one harness.
// An empty result means the harness has no allowlist.
func (s *Store) ApprovedModelsFor(h contracts.Harness) map[string]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]struct{}, len(s.approved[h]))
	for m := range s.approved[h] {
		out[m] = struct{}{}
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

// SetApprovedModels replaces every harness's allowlist with models. It is the
// flat form of SetHarnessApprovedModels, kept for callers that have no harness.
func (s *Store) SetApprovedModels(models []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approved = make(map[contracts.Harness]map[string]struct{}, len(contracts.AllHarnesses()))
	for _, h := range contracts.AllHarnesses() {
		s.setApprovedLocked(h, models)
	}
	s.persistLocked()
}

// SetHarnessApprovedModels replaces one harness's allowlist. Other harnesses
// keep their own lists, so a model approved on one harness stays unapproved on
// the rest.
func (s *Store) SetHarnessApprovedModels(h contracts.Harness, models []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.approved, h)
	s.setApprovedLocked(h, models)
	s.persistLocked()
}

// setApprovedLocked stores the trimmed, non-empty models under h. Caller holds mu.
func (s *Store) setApprovedLocked(h contracts.Harness, models []string) {
	for _, m := range models {
		if m = strings.TrimSpace(m); m == "" {
			continue
		}
		if s.approved[h] == nil {
			s.approved[h] = map[string]struct{}{}
		}
		s.approved[h][m] = struct{}{}
	}
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

// RoleEnabled reports whether a role may be used. Roles are enabled unless
// explicitly disabled. Callers must still honor the role's Required flag.
func (s *Store) RoleEnabled(id contracts.RoleID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.disabledRoles[id]
}

// SetRoleEnabled enables or disables a role.
func (s *Store) SetRoleEnabled(id contracts.RoleID, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if enabled {
		delete(s.disabledRoles, id)
	} else {
		s.disabledRoles[id] = true
	}
	s.persistLocked()
}

// ExperimentalRoles reports whether experimental roles may be used.
func (s *Store) ExperimentalRoles() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.experimental
}

// SetExperimentalRoles toggles experimental roles.
func (s *Store) SetExperimentalRoles(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.experimental = v
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

// --- master chat ---

// MasterRuntime returns the master chat's runtime override, if one is set.
// Empty fields inherit the Orchestrator role's runtime.
func (s *Store) MasterRuntime() (RoleConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.masterRuntime == nil {
		return RoleConfig{}, false
	}
	return *s.masterRuntime, true
}

// SetMasterRuntime replaces the master chat's runtime override. A zero config
// removes it, so the master runs on the Orchestrator role's default.
func (s *Store) SetMasterRuntime(rc RoleConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rc == (RoleConfig{}) {
		s.masterRuntime = nil
	} else {
		copy := rc
		s.masterRuntime = &copy
	}
	s.persistLocked()
}

// MasterReporter reports whether reporter mode is on: the master sends a status
// report over WhatsApp whenever connectivity is established.
func (s *Store) MasterReporter() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.masterReporter
}

// SetMasterReporter turns reporter mode on or off.
func (s *Store) SetMasterReporter(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.masterReporter = enabled
	s.persistLocked()
}

// --- persistence ---

func (s *Store) snapshotLocked() fileConfig {
	fc := fileConfig{
		Workdir:           s.workdir,
		AutoMode:          s.autoMode,
		ApprovedByHarness: make(map[string][]string, len(s.approved)),
		Roles:             make(map[string]fileRoleConfig, len(s.roles)),
		DisabledHarnesses: make([]string, 0, len(s.disabled)),
		DisabledRoles:     make([]string, 0, len(s.disabledRoles)),
		ExperimentalRoles: s.experimental,
		CustomModels:      make(map[string][]string, len(s.custom)),
		Projects:          append([]Project(nil), s.projects...),
		StaleAfterMinutes: s.staleAfterMin,
		Relay:             s.relay,
	}
	for h, models := range s.approved {
		list := make([]string, 0, len(models))
		for m := range models {
			list = append(list, m)
		}
		slices.Sort(list)
		fc.ApprovedByHarness[string(h)] = list
	}
	for id, rc := range s.roles {
		fc.Roles[string(id)] = fileRoleConfig{Harness: string(rc.Harness), Model: rc.Model, Variant: rc.Variant}
	}
	for h := range s.disabled {
		fc.DisabledHarnesses = append(fc.DisabledHarnesses, string(h))
	}
	for id := range s.disabledRoles {
		fc.DisabledRoles = append(fc.DisabledRoles, string(id))
	}
	for h, list := range s.custom {
		fc.CustomModels[string(h)] = append([]string(nil), list...)
	}
	if s.masterRuntime != nil {
		fc.MasterRuntime = &fileMasterRuntimeConfig{
			Harness: string(s.masterRuntime.Harness),
			Model:   s.masterRuntime.Model,
			Variant: s.masterRuntime.Variant,
		}
	}
	fc.MasterReporter = s.masterReporter
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
