package cli

import (
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	spec := flagSpec{
		"--json": flagBool,
		"--note": flagValue,
		"--doc":  flagRepeated,
	}

	cases := []struct {
		name       string
		args       []string
		wantPos    []string
		wantBool   map[string]bool
		wantValue  map[string]string
		wantRepeat []string
		wantErr    string
	}{
		{
			name:     "flag after positional",
			args:     []string{"REQ-1", "--json"},
			wantPos:  []string{"REQ-1"},
			wantBool: map[string]bool{"--json": true},
		},
		{
			name:    "flag before positional",
			args:    []string{"--json", "REQ-1"},
			wantPos: []string{"REQ-1"},
		},
		{
			name:      "value flag, separate token",
			args:      []string{"REQ-1", "--note", "looks good"},
			wantPos:   []string{"REQ-1"},
			wantValue: map[string]string{"--note": "looks good"},
		},
		{
			// The value of a value flag is not a positional. Getting this wrong
			// is what fed "--json" (and would have fed "omp") to the initiative
			// resolver.
			name:      "value flag, inline form",
			args:      []string{"--note=looks good", "REQ-1"},
			wantPos:   []string{"REQ-1"},
			wantValue: map[string]string{"--note": "looks good"},
		},
		{
			name:       "repeated flag keeps every value",
			args:       []string{"--doc", "a.md", "--doc=b.md"},
			wantRepeat: []string{"a.md", "b.md"},
		},
		{
			name:    "bare dash is a positional (stdin)",
			args:    []string{"-"},
			wantPos: []string{"-"},
		},
		{
			name:    "double dash ends flag parsing",
			args:    []string{"--", "--json"},
			wantPos: []string{"--json"},
		},
		{
			name:    "unknown flag names what is accepted",
			args:    []string{"--nope"},
			wantErr: `unknown flag "--nope"; accepted flags: --doc, --json, --note`,
		},
		{
			name:    "value flag with no value",
			args:    []string{"--note"},
			wantErr: `flag "--note" requires a value`,
		},
		{
			name:    "boolean flag rejects a value",
			args:    []string{"--json=yes"},
			wantErr: `flag "--json" does not take a value`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pos, flags, err := parseArgs(spec, tc.args)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error %q, got nil", tc.wantErr)
				}
				if err.Error() != tc.wantErr {
					t.Fatalf("error = %q, want %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(pos, ",") != strings.Join(tc.wantPos, ",") {
				t.Errorf("positionals = %v, want %v", pos, tc.wantPos)
			}
			for name, want := range tc.wantBool {
				if flags.has(name) != want {
					t.Errorf("has(%q) = %v, want %v", name, flags.has(name), want)
				}
			}
			for name, want := range tc.wantValue {
				if got := flags.value(name); got != want {
					t.Errorf("value(%q) = %q, want %q", name, got, want)
				}
			}
			if tc.wantRepeat != nil {
				got := flags.list("--doc")
				if strings.Join(got, ",") != strings.Join(tc.wantRepeat, ",") {
					t.Errorf("list(--doc) = %v, want %v", got, tc.wantRepeat)
				}
			}
		})
	}
}
