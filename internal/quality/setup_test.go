package quality

import (
	"strings"
	"testing"
)

const setupReadme = "# Tool\n\n" +
	"## Installation\n\n" +
	"```bash\n" +
	"git clone https://example.com/tool.git\n" +
	"cd tool\n" +
	"npm ci\n" +
	"npm run build && npm test\n" +
	"```\n\n" +
	"## Usage\n\n" +
	"```sh\n" +
	"npm start\n" +
	"```\n\n" +
	"## Development\n\n" +
	"```console\n" +
	"$ cd web\n" +
	"$ go test \\\n" +
	"    ./...\n" +
	"ok  example.com/tool\n" +
	"$ go run ./cmd/tool\n" +
	"```\n"

func TestReadmeCommands(t *testing.T) {
	dirs := map[string]bool{"web": true}
	cmds := ReadmeCommands("README.md", setupReadme, func(d string) bool { return dirs[d] })
	var got []string
	for _, c := range cmds {
		s := c.Cmd
		if c.Skip != "" {
			s = "SKIP " + s
		}
		got = append(got, s+" @"+c.Source())
	}
	want := []string{
		"SKIP git clone https://example.com/tool.git @README.md:6",
		"SKIP npm ci @README.md:8",
		"npm run build @README.md:9",
		"npm test @README.md:9",
		"cd web && go test ./... @README.md:22",
		"SKIP cd web && go run ./cmd/tool @README.md:25",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestReadmeCommandsIgnoresUsageAndProse(t *testing.T) {
	readme := "# X\n\n## Usage\n\n```sh\nmake build\n```\n\n## Build\n\n```\nThis is prose, not a command\nexport FOO=1\nmake build\n```\n"
	cmds := ReadmeCommands("README.md", readme, nil)
	if len(cmds) != 1 || cmds[0].Cmd != "make build" || cmds[0].Line != 14 {
		t.Fatalf("only the Build section's make should be read: %+v", cmds)
	}
}

func TestMakefileCommands(t *testing.T) {
	mk := ".PHONY: test build\nall: build\n\ntest:\n\tgo test ./...\n\nbuild:\n\tgo build ./...\n"
	cmds := MakefileCommands(mk)
	if len(cmds) != 2 || cmds[0].Cmd != "make build" || cmds[0].Line != 7 || cmds[1].Cmd != "make test" || cmds[1].Line != 4 {
		t.Fatalf("got %+v", cmds)
	}
}
