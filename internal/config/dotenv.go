package config

import (
	"os"
	"path/filepath"
	"strings"
)

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
