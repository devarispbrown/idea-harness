package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `idea eval` end to end through the CLI: the scripted run needs no daemon
// and no key, prints the markdown report, writes the JSON, and passes against
// the committed baseline.
func TestEvalCommandScripted(t *testing.T) {
	dir := t.TempDir()
	jsonOut := filepath.Join(dir, "r.json")
	var out, errb bytes.Buffer
	err := Run([]string{"eval", "--case", "habit-tracker", "--case", "markdown-site",
		"--baseline", filepath.Join("..", "eval", "testdata", "baseline-scripted.json"),
		"--json-out", jsonOut}, &out, &errb)
	if err != nil {
		t.Fatalf("eval: %v\nstderr:\n%s\nstdout:\n%s", err, errb.String(), out.String())
	}
	for _, want := range []string{"# idea eval: default", "habit-tracker", "stalled", "No regressions against"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report missing %q:\n%s", want, out.String())
		}
	}
	if !strings.Contains(errb.String(), "scripted provider") {
		t.Errorf("a run with no --model must say it is scripted: %s", errb.String())
	}
	raw, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Provider string `json:"provider"`
		Cases    []struct {
			Name       string `json:"name"`
			HumanStops []any  `json:"human_stops"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Provider != "scripted" || len(rep.Cases) != 2 {
		t.Fatalf("json report = %s", raw)
	}
	// Suite order, not --case order; the genuine blocker is the one stop.
	stops := map[string]int{}
	for _, c := range rep.Cases {
		stops[c.Name] = len(c.HumanStops)
	}
	if stops["habit-tracker"] != 1 || stops["markdown-site"] != 0 {
		t.Fatalf("stops = %v", stops)
	}
}

func TestEvalCommandRefusesBadInput(t *testing.T) {
	for _, args := range [][]string{
		{"eval", "todo-cli"},
		{"eval", "--case", "no-such-case"},
		{"eval", "--suite", "no-such-suite"},
		{"eval", "--timeout", "soon"},
		{"eval", "--policy", "yolo", "--case", "pomodoro"},
	} {
		if err := Run(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
}

// A real model without its key is refused before anything runs.
func TestEvalCommandRefusesAModelWithoutItsKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	err := Run([]string{"eval", "--model", "openai/gpt-5.6-luna", "--case", "pomodoro"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("want a refusal naming the key, got %v", err)
	}
}
