package cli

import (
	"fmt"
	"sort"
	"strings"
)

// Flag parsing for the CLI. Commands used to index args positionally and treat
// anything left over as a reference, so `idea tasks --json` resolved an
// initiative named "--json" (DX audit, P1). A flag cannot be told from a
// positional by its "-" prefix alone — the CLI has boolean flags (--pass,
// --yes), value flags (--note, --kind), and repeatable ones (--doc) — so each
// command declares what it accepts and one parser applies that declaration.

type flagKind int

const (
	// flagUnset is the zero value and is never a real kind. Without it, a map
	// miss on a flagSpec returns the same value as flagBool, so
	// `spec["--json"] == flagBool` is true for every command that does not
	// declare --json -- which silently mislabelled logs, next and help as
	// supporting --json when the generated table was first derived from a spec.
	flagUnset    flagKind = iota
	flagBool              // --yes
	flagValue             // --note <v> or --note=<v>
	flagRepeated          // --doc <v>, repeatable
)

// flagSpec declares the flags a command accepts, keyed by flag name including
// its leading dashes.
type flagSpec map[string]flagKind

// jsonFlag is the spec for the state-returning commands, whose only flag is
// --json (docs/cli.md: "State-returning commands support --json").
var jsonFlag = flagSpec{"--json": flagBool}

// parsedFlags holds the flags found on a command line.
type parsedFlags struct {
	bools    map[string]bool
	values   map[string]string
	repeated map[string][]string
}

func (p parsedFlags) has(name string) bool { return p.bools[name] }

func (p parsedFlags) value(name string) string { return p.values[name] }

func (p parsedFlags) list(name string) []string { return p.repeated[name] }

// parseArgs splits args into positionals and flags according to spec. Flags may
// appear anywhere. An unknown flag, or a value flag with no value, is an error
// naming what the command accepts — it never becomes a positional.
func parseArgs(spec flagSpec, args []string) ([]string, parsedFlags, error) {
	p := parsedFlags{
		bools:    map[string]bool{},
		values:   map[string]string{},
		repeated: map[string][]string{},
	}
	var positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]

		// "--" ends flag parsing: everything after it is a positional, which is
		// the only way to pass a value that begins with a dash.
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}

		// "-" alone is a positional: `idea docs add -` reads from stdin.
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}

		name, inlineValue, hasInline := strings.Cut(a, "=")
		kind, ok := spec[name]
		if !ok {
			return nil, p, fmt.Errorf("unknown flag %q; %s", name, acceptedFlags(spec))
		}

		if kind == flagBool {
			if hasInline {
				return nil, p, fmt.Errorf("flag %q does not take a value", name)
			}
			p.bools[name] = true
			continue
		}

		v := inlineValue
		if !hasInline {
			if i+1 >= len(args) {
				return nil, p, fmt.Errorf("flag %q requires a value", name)
			}
			i++
			v = args[i]
		}
		if kind == flagRepeated {
			p.repeated[name] = append(p.repeated[name], v)
		} else {
			p.values[name] = v
		}
	}

	return positional, p, nil
}

// acceptedFlags renders a spec for an error message.
func acceptedFlags(spec flagSpec) string {
	if len(spec) == 0 {
		return "this command takes no flags"
	}
	names := make([]string, 0, len(spec))
	for name := range spec {
		names = append(names, name)
	}
	sort.Strings(names)
	return "accepted flags: " + strings.Join(names, ", ")
}

// accepts reports whether spec declares name with the given kind. Callers must
// use this rather than comparing spec[name] directly: a map miss returns
// flagUnset, and comparing against a real kind would be true for absent flags.
func (spec flagSpec) accepts(name string, kind flagKind) bool {
	k, ok := spec[name]
	return ok && k == kind
}

// names returns the spec's flags in sorted order, for the generated reference.
func (spec flagSpec) names() []string {
	out := make([]string, 0, len(spec))
	for name := range spec {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
