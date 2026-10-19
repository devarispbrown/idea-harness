package config

import (
	"os"
	"path/filepath"
	"strings"
)

// SetDotEnv upserts KEY=VALUE into dir/.env, preserving every other line and
// comment. A line that already sets KEY (with or without `export`, quoted or
// not) is replaced in place; otherwise a line is appended. The file is created
// mode 0600 — it holds secrets — and a missing file starts empty.
func SetDotEnv(dir, key, value string) error {
	path := filepath.Join(dir, ".env")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(data), "\n")
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		probe := strings.TrimPrefix(trimmed, "export ")
		eq := strings.Index(probe, "=")
		if eq <= 0 || strings.TrimSpace(probe[:eq]) != key {
			continue
		}
		lines[i] = key + "=" + value
		replaced = true
	}
	if !replaced {
		lines = append(lines, key+"="+value)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return os.WriteFile(path, []byte(out), 0o600)
}

// LoadDotEnv reads a .env file (KEY=VALUE, with optional `export` prefix and
// quotes) and returns its values. Values are used only as a fallback to the
// real process environment for secret resolution (§47). No interpolation or
// shell expansion is performed.
func LoadDotEnv(dir string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// Strip surrounding single/double quotes.
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if key != "" && val != "" {
			out[key] = val
		}
	}
	return out
}
