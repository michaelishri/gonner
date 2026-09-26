package condition

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/michaelishri/gonner/internal/execution"
)

// ConditionFactory is a function that creates a Condition from a raw config value.
type ConditionFactory func(value string) Condition

// registry holds the mapping of condition type names to their factory functions.
var registryMu sync.RWMutex
var registry = map[string]ConditionFactory{
	"env":             NewEnvCondition,
	"fileExists":      NewFileCondition,
	"portOpen":        NewPortOpenCondition,
	"commandSucceeds": NewCommandSucceedsCondition,
}

// Register adds a new condition type to the registry.
func Register(typeName string, factory ConditionFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[typeName] = factory
}

// Known reports whether a condition type name is registered.
func Known(typeName string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	_, ok := registry[typeName]
	return ok
}

// Create instantiates a Condition from a type name and raw value.
func Create(typeName, value string) (Condition, error) {
	registryMu.RLock()
	factory, ok := registry[typeName]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown condition type: %q", typeName)
	}
	return factory(value), nil
}

// flatten expands a list of single-condition objects into an ordered list of
// (type, value) pairs. Each element is typically a one-key map, but multi-key
// maps are supported and expanded in sorted key order for determinism.
func flatten(conditions []map[string]string) []condSpec {
	var specs []condSpec
	for _, m := range conditions {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			specs = append(specs, condSpec{typeName: k, value: m[k]})
		}
	}
	return specs
}

// condSpec is a single resolved condition (type + raw value).
type condSpec struct {
	typeName string
	value    string
}

// EvaluateAll evaluates a list of conditions where all must be true (AND logic).
// Returns true if the list is empty.
func (e *Evaluator) EvaluateAll(ctx context.Context, conditions []map[string]string) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	for _, spec := range flatten(conditions) {
		if err := ctx.Err(); err != nil {
			return false, "", err
		}
		cond, err := e.create(spec.typeName, spec.value)
		if err != nil {
			return false, "", err
		}
		ok, err := cond.Evaluate(ctx)
		if err != nil {
			return false, "", fmt.Errorf("evaluating %s condition: %w", spec.typeName, err)
		}
		if !ok {
			return false, fmt.Sprintf("%s %s", spec.typeName, spec.value), nil
		}
	}
	return true, "", nil
}

// EvaluateAny evaluates a list of conditions where at least one must be true (OR logic).
// Returns true if the list is empty.
func (e *Evaluator) EvaluateAny(ctx context.Context, conditions []map[string]string) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	specs := flatten(conditions)
	if len(specs) == 0 {
		return true, "", nil
	}

	var lastDesc string
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return false, "", err
		}
		cond, err := e.create(spec.typeName, spec.value)
		if err != nil {
			return false, "", err
		}
		ok, err := cond.Evaluate(ctx)
		if err != nil {
			return false, "", fmt.Errorf("evaluating %s condition: %w", spec.typeName, err)
		}
		if ok {
			return true, "", nil
		}
		lastDesc = fmt.Sprintf("%s %s", spec.typeName, spec.value)
	}
	return false, lastDesc, nil
}

// ShouldRun evaluates both whenAll and whenAny conditions for a process.
// Returns (true, "") if the process should run, or (false, reason) if skipped.
func (e *Evaluator) ShouldRun(ctx context.Context, whenAll, whenAny []map[string]string) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	if len(whenAll) > 0 {
		ok, reason, err := e.EvaluateAll(ctx, whenAll)
		if err != nil {
			return false, "", err
		}
		if !ok {
			return false, reason, nil
		}
	}

	if len(whenAny) > 0 {
		ok, reason, err := e.EvaluateAny(ctx, whenAny)
		if err != nil {
			return false, "", err
		}
		if !ok {
			return false, reason, nil
		}
	}

	return true, "", nil
}

// Evaluator injects the manager's execution service and process environment
// without changing factories for user-registered conditions.
type Evaluator struct {
	service *execution.Service
	spec    execution.Spec
}

func NewEvaluator(service *execution.Service, spec execution.Spec) *Evaluator {
	return &Evaluator{service: service, spec: spec}
}
func (e *Evaluator) create(name, value string) (Condition, error) {
	cond, err := Create(name, value)
	if err != nil {
		return nil, err
	}
	if c, ok := cond.(*CommandSucceedsCondition); ok {
		// Preserve custom registered factories, and do not mutate objects a
		// factory may share between evaluators.
		copy := *c
		copy.service = e.service
		copy.spec = e.spec
		return &copy, nil
	}
	return cond, nil
}
func EvaluateAll(ctx context.Context, c []map[string]string) (bool, string, error) {
	return NewEvaluator(execution.NewService(), execution.Spec{}).EvaluateAll(ctx, c)
}
func EvaluateAny(ctx context.Context, c []map[string]string) (bool, string, error) {
	return NewEvaluator(execution.NewService(), execution.Spec{}).EvaluateAny(ctx, c)
}
func ShouldRun(ctx context.Context, all, any []map[string]string) (bool, string, error) {
	return NewEvaluator(execution.NewService(), execution.Spec{}).ShouldRun(ctx, all, any)
}
