# Junie CLI

Status: **implemented, not yet tried against a real `junie`**. The agent is
[`junie.go`](../../internal/agent/junie.go) with its tests in
[`junie_test.go`](../../internal/agent/junie_test.go); what is still open is
listed in [Not yet verified](#not-yet-verified). When that list is closed, move
what holds into [Agent wiring](agent-wiring.md) and drop this status line.

[Junie CLI](https://junie.jetbrains.com/docs/junie-cli.html) is JetBrains'
terminal coding agent (command `junie`; Linux, macOS and Windows). magpie adds
it as a built-in agent: the Agents page gets a Junie CLI row that points it at
the gateway, as for the other agents in [Agent wiring](agent-wiring.md). It is a
separate product from JetBrains Air, which runs Junie among its own agents and
has its own row ([`air.go`](../../internal/agent/air.go)).

The facts below about Junie come from its documentation
([model selection](https://junie.jetbrains.com/docs/junie-cli-model-selection.html),
[custom LLM models](https://junie.jetbrains.com/docs/custom-llm-models.html),
[config.json](https://junie.jetbrains.com/docs/junie-cli-configuration.html),
[environment variables](https://junie.jetbrains.com/docs/environment-variables.html),
[MCP](https://junie.jetbrains.com/docs/junie-cli-mcp-configuration.html),
[skills](https://junie.jetbrains.com/docs/agent-skills.html)), read on
2026-10-31. They are not yet checked against a real binary; see
[Not yet verified](#not-yet-verified).

## Responsibilities and boundaries

| Part | Responsibility |
| --- | --- |
| Agent description | `junie` in `internal/agent` ([`junie.go`](../../internal/agent/junie.go)): detection, config path, the `model` and `small` fields, the wiring below. |
| Wiring | One custom model profile, `magpie.json`, in Junie's models folder, and the model setting that selects it. |
| Library | Instructions, MCP servers and skills magpie writes for Junie ([Library](library.md)). |
| Not magpie's | Junie's own sign-in (JetBrains account, `JUNIE_API_KEY`), its BYOK keys, its hooks, trust decisions and other profiles. magpie leaves them as they are. |

## Where Junie reads things

| What | Location | Notes |
| --- | --- | --- |
| Home | `~/.junie`, or `$JUNIE_HOME` | `$JUNIE_HOME` names a folder: read it with `appdir.Getenv` and add it to `agentenv.Vars` (`TestFolderVarsAreListed` fails without it). |
| Settings | `<home>/config.json` (user), `<project>/.junie/config.json` (project, only when the project is trusted) | Merged; flags override project, project overrides user. Fields include `model`, `effort`, `provider`, `byok`, `proxies`, `model-locations`, `hooks`. |
| Other settings | `<home>/settings.json` | A separate channel, not merged with `config.json`; the model, provider and effort each resolve through their own consumer. Holds `modelForLaunch` in a user's walkthrough. |
| Custom models | `<home>/models/*.json`, `<project>/.junie/models/*.json` | The file name without `.json` is the profile id; the model is selected as `custom:<id>`. |
| MCP servers | `<home>/mcp/mcp.json`, `<project>/.junie/mcp/mcp.json` | `{"mcpServers": {name: {command, args, env} or {url, headers}, "timeout": ms}}`. |
| Skills | `<home>/skills/<name>/`, `<project>/.junie/skills/<name>/` | The open Agent Skills format. |
| Guidelines | `AGENTS.md`, `.junie/AGENTS.md`, `.junie/rules/*.md`, `.junie/playbook.md`, `.junie/guidelines.md` (legacy) | `.junie/AGENTS.md`, when present, is used alone. |

Detection: the `junie` command on magpie's PATH (`proc.FindTool`), or the home
folder. As for every agent, a folder alone, which an uninstall leaves behind,
shows the row as gone (`CLIMissing`).

## How magpie connects it

Junie reaches a model through one of four providers: Junie (a JetBrains
subscription), BYOK (its keys for OpenAI, Anthropic, Google, xAI, OpenRouter,
Copilot, LiteLLM), Custom (a profile file) or Proxy. BYOK takes no base URL of
its own, so magpie uses **Custom**:

1. magpie writes `<home>/models/magpie.json`:

   ```json
   {
     "id": "<the picked model>",
     "baseUrl": "http://127.0.0.1:<port>/v1/chat/completions",
     "apiType": "OpenAICompletion",
     "apiKey": "<the gateway key>",
     "displayName": "magpie",
     "primaryModel": {"id": "<the picked model>"},
     "fasterModel": {"id": "<the small model>"}
   }
   ```

   - `baseUrl` is the complete endpoint: Junie appends no path. The gateway's
     `/v1/chat/completions` is the first choice because every provider's models
     are reachable through it; `OpenAIResponses` (`/v1/responses`) is the
     alternative if the real binary needs it for reasoning effort.
   - `apiKey` is the gateway's key for that place (`at.gwKey()`, as Droid's),
     written literally: a `${VAR}` reference makes the profile fail to load when
     the variable is not set, and a Junie started from a launcher has none.
   - `displayName: "magpie"` is the mark of whose profile it is.
   - `fasterModel` is Junie's helper model (summaries, routing, memory
     extraction). It is the **small** field, as OpenCode's `small`. Left unset,
     it inherits the primary model's settings.
   - `temperature` is never written: Junie sends none for a custom model unless
     asked, and the provider's default is the right one for most models.
2. magpie selects the profile with `"model": "custom:magpie"` in
   `<home>/config.json`. A `model` there that is the user's own is kept in
   `applied.json` and put back on Disconnect. The user's `provider`, `byok`,
   `proxies`, `hooks` and every other key stay as they are.
3. The model picker lists the catalog's models and routing groups. Picking
   another model rewrites `magpie.json` (a profile holds one primary model);
   Junie reads it at start, so the Notice asks to restart open sessions.
4. Disconnect removes `magpie.json` (only a profile marked as magpie's) and
   puts the user's `model` back; a `config.json` magpie made goes with it when
   nothing else is left in it.

`Set("")` returns the field to Junie's default model and takes the wiring out.
The agent has no `Sync`: a profile holds one model, so there is no catalog to
bring up to date. Drift (`Check`) compares the profile's `baseUrl` and `apiKey`
with the gateway's, and reports a profile that is gone.

An effort level (`effort` in `config.json`, `minimal` to `max`) belongs to
Junie's own model list and is not offered on a custom profile until the real
binary shows it is sent (see below).

## Constraints and failure behavior

- A `models/magpie.json` the user wrote themselves (its `displayName` isn't
  `magpie`) is theirs: picking a magpie model reports a conflict and writes
  nothing, and Disconnect leaves it in place.
- Project config wins over user config for a trusted project. A project's
  `.junie/config.json` that sets `model` keeps winning over magpie's wiring; the
  Agents page says so, as it does for other agents' project overlays (drift is
  judged on the user-scope files only).
- Junie's `config.json` and profile are read at start; there is no hot reload.
- A Junie that is not signed in to anything has no account: Custom needs none,
  so a wired magpie works without the Junie login.
- Junie uses a helper model "from the same provider" with BYOK. With Custom, the
  helper is `fasterModel`, so no request goes to a model magpie did not route.
- The gateway needs no key on this machine, as for other loopback agents; a
  remote gateway puts the real key in `apiKey`.
- Junie's untrusted-project mode ignores project models, MCP servers and skills,
  and keeps the home ones. magpie writes only the home (user-scope) files for
  this reason.

## Library

| Kind | Where | Format |
| --- | --- | --- |
| MCP servers | `<home>/mcp/mcp.json`, a project's `.junie/mcp/mcp.json` | `mcpServers` in Cursor's form (`command`, `args`, `env`, or `url`, `headers`; no `type`), `fmtCursor` in [`targets.go`](../../internal/library/targets.go). |
| Skills | `<home>/skills/<name>/`, a project's `.junie/skills/<name>/` | The shared Agent Skills folder form, linked or copied as for other agents. |
| Instructions | none | Junie reads a project's `AGENTS.md` itself; a user-scope instructions file isn't documented, so magpie writes none until it is verified. |

[MCP router](mcp-router.md): Junie takes the one `magpie` server in place of
the Library's servers, as for the other agents.

## Not yet verified

Each of these is checked with a real `junie` in a sandbox `$JUNIE_HOME`
before the PR merges, and the PR body says what was run and what was not:

- The request Junie sends to a custom endpoint: path, headers and User-Agent
  (diff against what the gateway classifies as Junie, per
  *another app's values come from that app*), tool-call and streaming shape on
  `OpenAICompletion` versus `OpenAIResponses`.
- Whether `127.0.0.1` or `localhost` is accepted in `baseUrl`, and what a
  profile with an unreachable endpoint does at start.
- Whether `custom:magpie` in `config.json` selects the profile for the TUI,
  `--task` (headless) and `--acp`, or `modelForLaunch` in `settings.json`
  beats it. Both channels are written if the second is needed.
- Whether `fasterModel` is used for the internal tasks, and what Junie does
  with a missing one.
- A `$JUNIE_HOME`, Windows paths (`%USERPROFILE%\.junie`) and WSL twins.
- Whether a user-scope instructions file exists, and `${...}` in `mcp.json`.
- Effort on a custom profile.

## Verification

Written, following the other agents' tests:

- `TestJunie`, `TestJunieForeignProfile` and `TestJunieHomeAndNewFile` in
  `internal/agent`: Connect, a model and small pick, user keys and profile
  additions kept, drift, Disconnect putting `model` back, a foreign
  `magpie.json` left alone, a made file going again, `$JUNIE_HOME`. `junie` is
  also in `TestHandWiredMagpieProviderSiblings` and
  `TestOwnProviderNamedAsMagpies`'s lists.
- `JUNIE_HOME` is in `agentenv.Vars`.
- Still to write: an optional real-binary test gated by `MAGPIE_TEST_JUNIE_BIN`,
  as StepCode's and Reasonix's, once a `junie` has been tried.
- The whole suite: `go vet -tags nogui ./... && go test -tags nogui ./...`.
