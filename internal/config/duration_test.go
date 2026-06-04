package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDuration_UnmarshalJSON_String(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected time.Duration
	}{
		{"seconds", `"5s"`, 5 * time.Second},
		{"minutes", `"2m"`, 2 * time.Minute},
		{"complex", `"1m30s"`, 90 * time.Second},
		{"milliseconds", `"500ms"`, 500 * time.Millisecond},
		{"hours", `"1h"`, time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			if err := json.Unmarshal([]byte(tt.input), &d); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if time.Duration(d) != tt.expected {
				t.Errorf("got %v, want %v", time.Duration(d), tt.expected)
			}
		})
	}
}

func TestDuration_UnmarshalJSON_Number(t *testing.T) {
	var d Duration
	// JSON number is treated as nanoseconds
	if err := json.Unmarshal([]byte(`1000000000`), &d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if time.Duration(d) != time.Second {
		t.Errorf("got %v, want %v", time.Duration(d), time.Second)
	}
}

func TestDuration_UnmarshalJSON_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"invalid string", `"notaduration"`},
		{"boolean", `true`},
		{"null_string", `""`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			if err := json.Unmarshal([]byte(tt.input), &d); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestDuration_MarshalJSON(t *testing.T) {
	d := Duration(5 * time.Second)
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != `"5s"` {
		t.Errorf("got %s, want %q", data, "5s")
	}
}

func TestDuration_RoundTrip(t *testing.T) {
	original := Duration(90 * time.Second)
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var restored Duration
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if time.Duration(original) != time.Duration(restored) {
		t.Errorf("round trip failed: got %v, want %v", time.Duration(restored), time.Duration(original))
	}
}
