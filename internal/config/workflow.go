// Package config also exposes LoadWorkflow, which reads workflow.yaml — the
// document that defines valid task statuses and priorities. It uses its own
// Viper instance so workflow reloads don't disturb application config.
//
// Implementation lands in Phase 2 (Domain Models & Validation).
package config

// Workflow describes the legal vocabulary for tasks: the statuses a task may
// hold, and the priorities it may take. Statuses are ordered; transitions
// between them will be policed by the service layer in Phase 4.
type Workflow struct {
	Statuses   []string `mapstructure:"statuses"`
	Priorities []string `mapstructure:"priorities"`
}

// LoadWorkflow will load and validate the workflow.yaml file.
// TODO(phase-2): implement with a fresh *viper.Viper, validate non-empty
// slices, lowercase normalisation, and unique entries.
func LoadWorkflow(path string) (*Workflow, error) {
	_ = path
	return nil, nil
}
