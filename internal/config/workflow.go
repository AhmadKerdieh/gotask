package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Workflow is the in-memory representation of workflow.yaml.
//
// It is loaded once at startup and treated as immutable for the life of
// the process. Methods on Workflow are read-only lookups (IsStatus,
// IsPriority, CanTransition, IsTerminal). Mutations would require
// rebuilding the lookup maps in a thread-safe way, which we defer to a
// later phase if hot-reload is ever introduced.
//
// We deliberately do NOT import internal/domain here. config is a
// lower-level package than domain and importing upward would create a
// dependency cycle (domain → config → domain). Workflow operates on raw
// strings; the validator bridges Workflow and the domain.Status /
// domain.Priority types by converting them with String().
type Workflow struct {
	Statuses         []string            `yaml:"statuses"`
	TerminalStatuses []string            `yaml:"terminal_statuses"`
	Transitions      map[string][]string `yaml:"transitions"`
	Priorities       []string            `yaml:"priorities"`
	DefaultStatus    string              `yaml:"default_status"`
	DefaultPriority  string              `yaml:"default_priority"`

	// Precomputed lookups, populated after Load. Unexported so callers go
	// through methods.
	statusSet     map[string]struct{}
	priSet        map[string]struct{}
	terminalSet   map[string]struct{}
	transitionSet map[string]map[string]struct{} // from → set of allowed to
}

// LoadWorkflow reads workflow.yaml from the given path, parses it, runs
// internal-consistency checks, and returns a ready-to-query *Workflow.
//
// Internal-consistency checks (none of these can be expressed in YAML
// alone, which is why we run them here):
//
//   - statuses and priorities lists are non-empty and have no duplicates
//   - terminal_statuses ⊆ statuses
//   - every key in transitions is in statuses
//   - every destination in transitions is in statuses
//   - default_status ∈ statuses
//   - default_priority ∈ priorities
//   - terminal statuses have no outgoing transitions (defensive check —
//     listing a transition out of a terminal status is meaningless)
//
// Any failure here is fatal at startup: the application cannot serve
// requests with an inconsistent workflow.
func LoadWorkflow(path string) (*Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow file %q: %w", path, err)
	}

	var wf Workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parse workflow file %q: %w", path, err)
	}

	if err := wf.validate(); err != nil {
		return nil, fmt.Errorf("invalid workflow file %q: %w", path, err)
	}

	wf.buildLookups()
	return &wf, nil
}

// validate runs the consistency checks documented on LoadWorkflow.
func (w *Workflow) validate() error {
	if len(w.Statuses) == 0 {
		return fmt.Errorf("statuses: must list at least one status")
	}
	if len(w.Priorities) == 0 {
		return fmt.Errorf("priorities: must list at least one priority")
	}
	if w.DefaultStatus == "" {
		return fmt.Errorf("default_status: required")
	}
	if w.DefaultPriority == "" {
		return fmt.Errorf("default_priority: required")
	}

	// Build a temporary status set for the cross-checks.
	statuses := toSet(w.Statuses)
	if len(statuses) != len(w.Statuses) {
		return fmt.Errorf("statuses: duplicate entries")
	}
	priorities := toSet(w.Priorities)
	if len(priorities) != len(w.Priorities) {
		return fmt.Errorf("priorities: duplicate entries")
	}

	if _, ok := statuses[w.DefaultStatus]; !ok {
		return fmt.Errorf("default_status %q is not in statuses", w.DefaultStatus)
	}
	if _, ok := priorities[w.DefaultPriority]; !ok {
		return fmt.Errorf("default_priority %q is not in priorities", w.DefaultPriority)
	}

	terminals := toSet(w.TerminalStatuses)
	for s := range terminals {
		if _, ok := statuses[s]; !ok {
			return fmt.Errorf("terminal_statuses: %q is not in statuses", s)
		}
	}

	for from, dests := range w.Transitions {
		if _, ok := statuses[from]; !ok {
			return fmt.Errorf("transitions: source %q is not in statuses", from)
		}
		// A terminal status listing transitions is almost certainly a
		// configuration error — fail loudly so the operator catches it.
		if _, isTerminal := terminals[from]; isTerminal && len(dests) > 0 {
			return fmt.Errorf("transitions: terminal status %q must not list any destinations", from)
		}
		for _, to := range dests {
			if _, ok := statuses[to]; !ok {
				return fmt.Errorf("transitions: destination %q (from %q) is not in statuses", to, from)
			}
			if to == from {
				return fmt.Errorf("transitions: self-transition %q → %q is not allowed", from, to)
			}
		}
	}

	return nil
}

// buildLookups populates the unexported sets used by the query methods.
// Called once after validate() so we never expose an unvalidated Workflow.
func (w *Workflow) buildLookups() {
	w.statusSet = toSet(w.Statuses)
	w.priSet = toSet(w.Priorities)
	w.terminalSet = toSet(w.TerminalStatuses)

	w.transitionSet = make(map[string]map[string]struct{}, len(w.Transitions))
	for from, dests := range w.Transitions {
		w.transitionSet[from] = toSet(dests)
	}
}

// IsStatus reports whether s is a legal status.
func (w *Workflow) IsStatus(s string) bool {
	_, ok := w.statusSet[s]
	return ok
}

// IsPriority reports whether p is a legal priority.
func (w *Workflow) IsPriority(p string) bool {
	_, ok := w.priSet[p]
	return ok
}

// IsTerminal reports whether s is a terminal status (no outgoing
// transitions). Returns false for unknown statuses — callers should
// check IsStatus first if they want to distinguish "unknown" from
// "known but not terminal".
func (w *Workflow) IsTerminal(s string) bool {
	_, ok := w.terminalSet[s]
	return ok
}

// CanTransition reports whether moving from status `from` to status `to`
// is allowed by the workflow. Returns false if either status is unknown
// or if from is terminal.
//
// Note: from == to is always false (no-op transitions are rejected at
// the validator layer with a clearer message; this method exists to
// answer "is the transition legal", and a no-op is not a legal
// transition).
func (w *Workflow) CanTransition(from, to string) bool {
	if from == to {
		return false
	}
	dests, ok := w.transitionSet[from]
	if !ok {
		return false
	}
	_, allowed := dests[to]
	return allowed
}

// AllowedTransitions returns the legal destinations from a status. The
// returned slice is freshly allocated and safe to mutate. Returns nil
// for an unknown status; returns an empty (non-nil) slice for a known
// terminal status.
func (w *Workflow) AllowedTransitions(from string) []string {
	dests, ok := w.transitionSet[from]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(dests))
	for d := range dests {
		out = append(out, d)
	}
	return out
}

// toSet is a tiny helper that builds a set from a slice. Useful in
// validation paths where we want O(1) "is x in the list" lookups.
func toSet(xs []string) map[string]struct{} {
	s := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		s[x] = struct{}{}
	}
	return s
}
