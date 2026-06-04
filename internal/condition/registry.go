package condition

import (
	"fmt"
	"sort"
)

// ConditionFactory is a function that creates a Condition from a raw config value.
type ConditionFactory func(value string) Condition

// registry holds the mapping of condition type names to their factory functions.
var registry = map[string]ConditionFactory{
	"env":             NewEnvCondition,
	"fileExists":      NewFileCondition,
	"portOpen":        NewPortOpenCondition,
	"commandSucceeds": NewCommandSucceedsCondition,
}

// Register adds a new condition type to the registry.
func Register(typeName string, factory ConditionFactory) {
	registry[typeName] = factory
}

// Known reports whether a condition type name is registered.
func Known(typeName string) bool {
	_, ok := registry[typeName]
	return ok
}

// Create instantiates a Condition from a type name and raw value.
func Create(typeName, value string) (Condition, error) {
	factory, ok := registry[typeName]
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
func EvaluateAll(conditions []map[string]string) (bool, string, error) {
	for _, spec := range flatten(conditions) {
		cond, err := Create(spec.typeName, spec.value)
		if err != nil {
			return false, "", err
		}
		ok, err := cond.Evaluate()
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
func EvaluateAny(conditions []map[string]string) (bool, string, error) {
	specs := flatten(conditions)
	if len(specs) == 0 {
		return true, "", nil
	}

	var lastDesc string
	for _, spec := range specs {
		cond, err := Create(spec.typeName, spec.value)
		if err != nil {
			return false, "", err
		}
		ok, err := cond.Evaluate()
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
func ShouldRun(whenAll, whenAny []map[string]string) (bool, string, error) {
	if len(whenAll) > 0 {
		ok, reason, err := EvaluateAll(whenAll)
		if err != nil {
			return false, "", err
		}
		if !ok {
			return false, reason, nil
		}
	}

	if len(whenAny) > 0 {
		ok, reason, err := EvaluateAny(whenAny)
		if err != nil {
			return false, "", err
		}
		if !ok {
			return false, reason, nil
		}
	}

	return true, "", nil
}
