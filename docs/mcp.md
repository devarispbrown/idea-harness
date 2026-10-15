# MCP server

`idea mcp` serves idea-harness to any [Model Context Protocol](https://modelcontextprotocol.io)
client — Claude Code, Cursor, Codex — so an agent you already work in can read an initiative's
state, answer its questions, and review its change sets without leaving the editor.

It is a stdio server (newline-delimited JSON-RPC 2.0) and a client of the daemon, exactly like the
rest of the CLI: it auto-starts the daemon when none is running, authenticates the same way every
`idea` command does, and never opens the state database itself. Every rule the daemon enforces —
an approval needs a completed adversarial review, apply needs an approved change set — applies to a
tool call because it is the same API call.

## Setup

Run `idea init` in the project first. `idea mcp` works on the project registered for its working
directory, the same way `idea status` does.

### Claude Code

From the project directory:

```sh
claude mcp add idea -- idea mcp
```

Add `--scope project` to write it to the project's `.mcp.json` so everyone who opens the repository
gets it. To let Claude apply change sets as well (see [Tiers](#tiers)):

```sh
claude mcp add idea -- idea mcp --allow-apply
```

### Cursor

`.cursor/mcp.json` in the project (or `~/.cursor/mcp.json` for every project):

```json
{
  "mcpServers": {
    "idea": {
      "command": "idea",
      "args": ["mcp", "--dir", "/absolute/path/to/your/project"]
    }
  }
}
```

`--dir` is there because a host may launch the server from a directory other than the project's;
it makes `idea mcp` resolve the project from that path instead of its own working directory.

### Codex

`~/.codex/config.toml`:

```toml
[mcp_servers.idea]
command = "idea"
args = ["mcp", "--dir", "/absolute/path/to/your/project"]
```

### Any other client

Launch `idea mcp` as a stdio server. If `idea` is not on the PATH the client sees, give the binary's
absolute path as the command. Diagnostics go to stderr; stdout carries protocol messages only.

## Tiers

| Invocation | Tools exposed |
| --- | --- |
| `idea mcp --read-only` | read tools only |
| `idea mcp` (default) | read tools + judgment tools |
| `idea mcp --allow-apply` | read + judgment + `apply_change_set`, `revert_change_set` |

The default line is drawn at the working tree, not at "writes". Answering a question, resolving a
decision, approving a change set and running the verification gate record a judgment the daemon
keeps — every one can be revisited (`idea reopen-decision`, a later review) and none changes a file
in your repository. Applying or reverting a change set does change files, so a model can only do it
when whoever configured the server opted in with `--allow-apply`. A tool outside the server's tier
is absent from `tools/list`; calling it anyway returns an error that names the flag that enables it.

Tool annotations tell the host what each tool does: reads carry `readOnlyHint`; `review_change_set`,
`apply_change_set` and `revert_change_set` carry `destructiveHint`, which hosts use to ask before
running them.

## Tools

Initiative-scoped tools take an optional `initiative` (an id from `list_initiatives`) and default
to the most recently created initiative — the same default every `idea` command uses.

### Read

| Tool | Answers | CLI equivalent |
| --- | --- | --- |
| `list_initiatives` | every initiative and its status | `idea status --json` |
| `status` | one initiative: status, counts, next action, recommended move | `idea status` |
| `next_step` | what the initiative waits on, and which tool or command moves it (never runs it) | `idea next` |
| `show_prd` | the latest PRD as markdown; `kind` selects another artifact (`execution_plan`, `technical_design`, …) | `idea prd` |
| `list_tasks` | the execution plan's tasks | `idea tasks` |
| `list_findings` | review findings | `idea reviews` |
| `list_questions` | questions, with the short ref `answer_question` accepts | `idea questions` |
| `list_decisions` | decisions, with numbered options | `idea decisions` |
| `why` / `impact` / `trace` | the product graph around a requirement, task, decision, artifact, assertion or context doc | `idea why` / `idea impact` / `idea trace` |
| `gaps` | what the Product Graph is missing or no longer holds — unimplemented or unverified requirements, untested criteria, stale evidence, unvalidated assumptions, invalidated decisions, orphans, stale artifacts — each with the command that closes it; `kind` filters (comma-separated) | `idea gaps` |
| `changed` | the initiative's timeline | `idea changed` |
| `list_change_sets` | change sets and their review/apply state | `idea changes` |
| `show_change_set` | one change set and its redacted diff (patches over 100 KiB are truncated) | `idea change` |
| `get_operation` | a background operation's state, by the `operation_id` a tool returned | — |

### Judgment (default and `--allow-apply`)

| Tool | Does | CLI equivalent |
| --- | --- | --- |
| `new_initiative` | starts an initiative from an idea; calls the model provider; returns an `operation_id` | `idea new --no-wait` |
| `answer_question` | records an answer | `idea answer` |
| `resolve_decision` | resolves a decision by option number or text | `idea resolve` |
| `review_change_set` | `approve`, `reject`, `changes_requested`, or `rerun` (returns an `operation_id`); `accept_findings` with `approve` | `idea review` |
| `verify` | runs the verification gate and reports coverage | `idea verify [<req>]` |

`verify` runs the gate and records no evidence. `idea verify REQ-n --pass|--fail|--waive` records
evidence as a person's word, so it stays something a person types.

Not exposed: `advance`, `execute` and `next --until-human`, which spend model budget on design and
implementation runs; `approve`/`revise` for the PRD and plan. `next_step` names the CLI command
when one of those is what the initiative waits on.

### Apply (`--allow-apply` only)

| Tool | Does | CLI equivalent |
| --- | --- | --- |
| `apply_change_set` | applies an approved change set to the working tree; returns an `operation_id` | `idea apply` |
| `revert_change_set` | rolls an applied change set back; returns an `operation_id` | `idea revert` |

Long-running work returns an `operation_id` immediately rather than holding the call open; poll
`get_operation` or `status`.

## Resources

Every initiative's latest artifacts are readable as markdown resources:

```text
idea://initiative/{initiative_id}/prd
idea://initiative/{initiative_id}/execution_plan
idea://initiative/{initiative_id}/{kind}
```

`resources/list` enumerates the ones that exist; the three templates are advertised through
`resources/templates/list`. Resources are available in every tier.

## Results and errors

Every tool result carries a concise text answer for the model and the same data as
`structuredContent` (always a JSON object — lists are wrapped, e.g. `{"decisions": [...]}`).

Failures are reported in one of two ways, following the specification:

- **`isError: true` tool results** for anything the model can correct: arguments that fail the
  tool's input schema, and every error the daemon returns — an unknown change set, a `409` such as
  "never finished its adversarial review", "no project registered for /path/to/dir; run
  `idea init` first". The text is the daemon's own message.
- **JSON-RPC errors** for problems in the request itself: unparseable JSON (`-32700`), an unknown
  method (`-32601`), an unknown or disabled tool, non-object arguments, or an unknown resource URI
  (`-32602`), an unsupported protocol version (`-32022`).

## Protocol versions

The server speaks the `2026-07-28` revision (per-request `_meta`, `server/discover`) and the
earlier `initialize`-handshake revisions `2025-11-25`, `2025-06-18`, `2025-03-26` and `2024-11-05`,
choosing per request: a request carrying `io.modelcontextprotocol/protocolVersion` in `_meta` is
served under that revision, and an `initialize` handshake selects the older behaviour. Requests
are answered one at a time, in order. The implementation is in-house (`internal/mcp`) rather than an
SDK: the surface is one transport and eight methods, and the input-schema validator is the JSON
Schema 2020-12 library the module already uses for config validation.
