package main

import "testing"

func TestSpecVersion(t *testing.T) {
	for _, tc := range []struct {
		name, spec, want string
		wantErr          bool
	}{
		{name: "plain", spec: "info:\n  title: x\n  version: 0.4.0\n", want: "0.4.0"},
		{name: "quoted", spec: "info:\n  version: \"1.2.3\"\n", want: "1.2.3"},
		{name: "trailing comment", spec: "info:\n  version: 0.4.0  # bumped per RELEASING.md\n", want: "0.4.0"},
		{name: "absent", spec: "info:\n  title: x\n", wantErr: true},
		{name: "unparseable", spec: "info:\n  version: draft\n", wantErr: true},
		// Four-space indent is a `version:` nested inside a schema example, not
		// the info block. The anchor is what keeps those out.
		{name: "nested example is not the info version", spec: "components:\n  schemas:\n    X:\n      version: 9.9.9\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := specVersion(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// newestReleased takes the MAXIMUM, not the first heading. The file is
// newest-first today, but a check that depends on ordering stops checking the
// moment an entry is inserted out of order, and this one is meant to survive
// that.
func TestNewestReleased(t *testing.T) {
	for _, tc := range []struct {
		name, changelog, want string
		wantOK                bool
	}{
		{name: "newest first", changelog: "## [Unreleased]\n## [0.3.0]\n## [0.2.0]\n", want: "0.3.0", wantOK: true},
		{name: "out of order", changelog: "## [0.2.0]\n## [0.3.0]\n## [0.1.0]\n", want: "0.3.0", wantOK: true},
		// A `v` prefix used to disable the check silently: one prefixed heading
		// made it fall back to the previous release and pass, and prefixing all
		// of them made it a complete no-op that still printed a green line.
		{name: "v-prefixed", changelog: "## [Unreleased]\n## [v0.5.0]\n## [0.3.0]\n", want: "0.5.0", wantOK: true},
		{name: "unreleased only", changelog: "## [Unreleased]\n", wantOK: false},
		{name: "empty", changelog: "", wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := newestReleased(tc.changelog)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got.String() != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestParseSemver(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "0.4.0", want: "0.4.0"},
		{in: "v0.4.0", want: "0.4.0"},
		{in: " v1.20.300 ", want: "1.20.300"},
		{in: "0.4", wantErr: true},
		{in: "v0.4.0-rc1", wantErr: true}, // runVersionCheck gives this its own message
		{in: "main", wantErr: true},
		{in: "", wantErr: true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSemver(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSemverLess(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"0.1.0", "0.2.0", true},
		{"0.2.0", "0.1.0", false},
		{"0.3.0", "0.3.0", false},
		{"0.9.9", "1.0.0", true},
		{"1.0.0", "0.9.9", false},
		{"0.3.1", "0.3.2", true},
	} {
		a, _ := parseSemver(tc.a)
		b, _ := parseSemver(tc.b)
		if got := a.less(b); got != tc.want {
			t.Errorf("%s < %s = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
