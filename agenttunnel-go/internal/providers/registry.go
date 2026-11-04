package providers

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"sync"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// ErrUnknownHarness is returned when no driver is registered for a harness.
var ErrUnknownHarness = errors.New("unknown harness")

// Registry maps harnesses to drivers and to optional model discoverers.
type Registry struct {
	mu          sync.RWMutex
	drivers     map[contracts.Harness]Driver
	discoverers map[contracts.Harness]ModelDiscoverer
}

// NewRegistry builds a registry seeded with the given drivers.
func NewRegistry(drivers ...Driver) *Registry {
	r := &Registry{
		drivers:     make(map[contracts.Harness]Driver),
		discoverers: make(map[contracts.Harness]ModelDiscoverer),
	}
	for _, d := range drivers {
		_ = r.Register(d)
	}
	return r
}

// Register adds or replaces a driver.
func (r *Registry) Register(d Driver) error {
	if d == nil {
		return errors.New("nil driver")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drivers[d.Harness()] = d
	return nil
}

// RegisterDiscoverer attaches a model discoverer to a harness.
func (r *Registry) RegisterDiscoverer(h contracts.Harness, d ModelDiscoverer) {
	if d == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discoverers[h] = d
}

// Discoverer returns the model discoverer for a harness, if any.
func (r *Registry) Discoverer(h contracts.Harness) (ModelDiscoverer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.discoverers[h]
	return d, ok
}

// Get returns the driver for a harness.
func (r *Registry) Get(h contracts.Harness) (Driver, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.drivers[h]
	return d, ok
}

// Harnesses lists registered harnesses in stable order.
func (r *Registry) Harnesses() []contracts.Harness {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]contracts.Harness, 0, len(r.drivers))
	for h := range r.drivers {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Available lists registered harnesses whose CLI is resolvable on PATH.
func (r *Registry) Available() []contracts.Harness {
	var out []contracts.Harness
	for _, h := range r.Harnesses() {
		d, _ := r.Get(h)
		if _, err := exec.LookPath(d.Binary()); err == nil {
			out = append(out, h)
		}
	}
	return out
}

// NewAdapter builds a process-backed adapter for a spec's harness.
func (r *Registry) NewAdapter(spec SessionSpec) (Adapter, error) {
	d, ok := r.Get(spec.Harness)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownHarness, spec.Harness)
	}
	return NewProcessAdapter(d), nil
}
