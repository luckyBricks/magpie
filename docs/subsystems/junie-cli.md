# Junie CLI

Status: **implemented; junie 26.10.27 (3893.6) verified on Linux in batch,
TUI and ACP modes**. Native Windows and WSL execution still need their own
machines; path, discovery, networking and stopped-distro contracts are covered
by sandbox tests.
The agent is [`junie.go`](../../internal/agent/junie.go), with its tests in
[`junie_test.go`](../../internal/agent/junie_test.go) and the real-binary test
[`junie_cli_test.go`](../../internal/agent/junie_cli_test.go). What is still
open is in [Still open](#still-open). When that list is closed, move what holds
into [Agent wiring](agent-wiring.md) and drop this status line.

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
2026-10-31, and from junie 3893.6 itself where the two differ: see
[What the real binary showed](#what-the-real-binary-showed).

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
| Config | `<home>/config.json` (user), `<project>/.junie/config.json` (project, only when the project is trusted) | Merged; flags override project, project overrides user. Fields include `model`, `effort`, `provider`, `byok`, `proxies`, `model-locations`, `hooks`. 3893.6 doesn't take its `model` or `provider` as the model it starts on (below). |
| Settings | `<home>/settings.json` | Junie's own file, which it writes too (`JunieSettings`): `modelForLaunch` and `providerForLaunch` are the model and BYOK provider it starts on; also `ecoModePerModel`, `effortPerModel`, `subagentsMode` and others. |
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
     alternative, verified through the gateway too. Neither protocol sends
     effort for a custom model in 3893.6.
   - `apiKey` is the gateway's key for that place (`at.gwKey()`, as Droid's),
     written literally: a `${VAR}` reference makes the profile fail to load when
     the variable is not set, and a Junie started from a launcher has none.
   - `displayName: "magpie"` is the mark of whose profile it is.
   - `fasterModel` is the **small** field, as OpenCode's `small`. 3893.6 uses
     it for a TUI helper call. Batch and ACP titles and step summaries still
     use primary. The binary's history compression and summarization resolvers
     also fall back to primary for a custom profile: this field does not move
     every helper or memory operation to the small model.
   - `temperature` is never written: Junie sends none for a custom model unless
     asked, and the provider's default is the right one for most models.
2. magpie selects the profile with `"modelForLaunch": "custom:magpie"` in
   `<home>/settings.json` and takes `providerForLaunch` out: a BYOK provider
   there keeps Junie off a custom model. What the user had in both is stashed
   and put back when magpie steps out. The rest of `settings.json`, all of
   `config.json` (`byok`, `proxies`, `hooks`, …) and the user's other profiles
   stay as they are.
3. The model picker lists the catalog's models and routing groups. Picking
   another model rewrites `magpie.json` (a profile holds one primary model);
   Junie reads it at start, so the Notice asks to restart open sessions.
4. Disconnect removes `magpie.json` (only a profile marked as magpie's) and
   puts the user's `modelForLaunch` and `providerForLaunch` back; a
   `settings.json` magpie made goes with it when nothing else is left in it.
   Picking one of Junie's own models from magpie steps out the same way, the
   user's provider put back with it.

`Set("")` returns the field to Junie's default model and takes the wiring out.
The agent has no `Sync`: a profile holds one model, so there is no catalog to
bring up to date. Drift (`Check`) compares the profile's `baseUrl` and `apiKey`
with the gateway's, and reports a profile that is gone and a `providerForLaunch`
picked in Junie since.

The gateway knows Junie's requests by their User-Agent (`UA: junie`):
`junie-cli-batch:<version>`, `junie-cli:<version>` (TUI), and
`junie-cli-acp:<version>`. Usage counts all three as Junie CLI's.

An effort level is not offered on a custom profile: 3893.6 sends neither
`reasoning_effort` on Chat nor `reasoning` on Responses, even with
`--model custom:magpie --effort high`. Junie's own effort selector does not
establish that the endpoint receives one.

## Constraints and failure behavior

- A `models/magpie.json` the user wrote themselves (its `displayName` isn't
  `magpie`) is theirs: picking a magpie model reports a conflict and writes
  nothing, and Disconnect leaves it in place.
- Junie's settings and profile are read at start; there is no hot reload.
  A wired TUI task kept `modelForLaunch: custom:magpie` and no
  `providerForLaunch`; it added unrelated settings beside them.
- An unavailable endpoint remains selected. A refused connection is retried,
  then reported as "Could not connect to the model provider. Check the network
  connection." No fallback to another model was observed. The binary's LLM
  client has five retries with exponential delay and a five-minute request
  timeout. A transient startup 503 can recover without reconnecting magpie.
- `--model` and `JUNIE_MODEL` beat `modelForLaunch`: a user who starts Junie
  with either isn't on magpie for that run.
- A Junie that is not signed in to anything has no account: Custom needs none,
  so a wired magpie works without the Junie login.
- Every call Junie made went to the profile's endpoint, so no request went to a
  model magpie didn't route.
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
| Instructions | `<home>/AGENTS.md` | Loaded alongside the project's instructions, verified in requests reaching the fixture. Junie reads project files itself. |

3893.6 does **not** expand `${NAME}` or `${env:NAME}` in MCP headers or a
command's `env`: the fixture received both literally with the variable set.
The target uses `fmtCursor` for the file's shape and `Literal: true` for its
values. Direct Library writes report servers requiring expansion as unsupported. Sync
removes such entries previously written by magpie, keeping their Library
definition and user-owned entries. Custom model profiles resolve references
separately and are unaffected.

On Windows the default home is `%USERPROFILE%/.junie`, unless `JUNIE_HOME`
is set. WSL Junie is a separate `junie@wsl:<distro>` agent, discovered by
`.junie` or its Linux `junie` command. It uses the distro's files and gateway
address, and gets copies of Library skills, home instructions and MCP config.
Host `JUNIE_HOME` is ignored there. An overridden home inside the distro is
not discovered, as magpie cannot read that distro's environment.

[MCP router](mcp-router.md): Junie takes the one `magpie` server in place of
the Library's servers, as for the other agents.

## What the real binary showed

junie 26.10.27 (3893.6), Linux, headless (`--task`), in a sandbox `JUNIE_HOME`,
with no JetBrains sign-in and no BYOK key, through magpie's gateway to a fake
OpenAI-compatible vendor that logged every request:

- `"model": "custom:magpie"` in `config.json` isn't read: the log's
  `model-select` line says `modelInSettings=null`, and Junie stops with "Cannot
  find authorization. Please authenticate before running Junie." `--model
  custom:magpie`, `JUNIE_MODEL` and `settings.json`'s `modelForLaunch` each
  select the profile. A `config.json` with `model`, `provider` and `byok` of
  the user's own doesn't get in the way of `modelForLaunch`.
- `providerForLaunch` (`anthropic`) beside `modelForLaunch: custom:magpie`
  makes Junie stop the same way, so magpie takes it out while connected.
- The profile at `http://127.0.0.1:<port>/v1/chat/completions` works: Junie
  POSTs there, non-streamed, with `model`, `messages`, `tools` (14 of them:
  `bash`, `create`, `search_replace`, `submit`, …), `tool_choice: auto`,
  `parallel_tool_calls: false`, `stop`, `seed`, `n` and `user`. A task ran
  through its tool loop (`create` of a file, then `submit`), and the file was
  written.
- Its User-Agent is `junie-cli-batch:26.10.27`.
- Helper calls (the task's title, a step's summary, a chat title) carry no tools
  and went to `primaryModel`, whether `fasterModel` named only an `id` or every
  field.
- Junie ignores `HOME` (it is Java: `user.home`), so a sandbox needs
  `JUNIE_HOME`, and `-Duser.home=<sandbox>` in `JAVA_TOOL_OPTIONS` too: with
  `JUNIE_HOME` set it still writes `instances/` under the real `~/.junie`.

The remaining checks were run on 2026-11-01 with the same 3893.6 release
(Linux amd64 archive SHA-256
`dc88c2434c4d673d4df1fe579cfaaf34565b223c6a8702c6ee8e0494638f8635`):

| Check | Observed behavior |
| --- | --- |
| TUI | Started from `modelForLaunch`, created a file and submitted through its tools; used `fasterModel` once and kept the selected profile in settings. |
| ACP | `initialize`, `session/new` and `session/prompt` ran without JetBrains authentication, created the file and ended with `end_turn`. |
| Responses | `/v1/responses` completed the tool round trip through the gateway to a Chat fixture returning SSE. Chat stays the default. |
| Streaming | Batch, TUI and ACP custom requests all sent `stream: false`; Responses did too. No streaming capability is claimed for this version. |
| Effort | Explicit high effort produced no reasoning field on either protocol. |
| Endpoint down | A refused endpoint failed with a connection message and left settings selected; an initial 503 recovered on retry. |
| Instructions and MCP | Home and project `AGENTS.md` both reached the primary prompt. MCP header and stdio environment references arrived literally. |

## Still open

- Native Windows CLI execution and a Windows host with real WSL distros.
  This Linux session has neither Windows nor `wsl.exe`; cross-platform vet
  and fake-WSL contract tests do not replace a run there. Check the default
  and space-containing overridden home, TUI/ACP, NAT and mirrored networking,
  and a stopped distro before describing those platforms as end-to-end tested.

## Verification

Written, following the other agents' tests:

- `TestJunie`, `TestJunieForeignProfile` and `TestJunieHomeAndNewFile` in
  `internal/agent`: Connect, a model and small pick, user keys and profile
  additions kept, drift (a provider picked in Junie too), Disconnect putting
  `modelForLaunch` and `providerForLaunch` back, a foreign
  `magpie.json` left alone, a made file going again, `$JUNIE_HOME`. `junie` is
  also in `TestHandWiredMagpieProviderSiblings` and
  `TestOwnProviderNamedAsMagpies`'s lists.
- `JUNIE_HOME` is in `agentenv.Vars`.
- `TestJunieRealCLI`, opt-in: batch Chat, Responses, explicit high effort on
  both protocols, ACP, startup retry, and a Linux PTY for the TUI. The fixture
  drives `create` then `submit`; the file is written, calls are attributed to
  Junie, home instructions reach the request, and Disconnect restores the
  previous BYOK selection. Batch also checks MCP's literal headers. No
  JetBrains or vendor account is used. The PTY accepts Junie's first-run
  project trust and configuration import dialogs. Normal test runs skip it.
- `TestJunieLibrary`, `TestAgentMCPFiles/junie`, `TestWSLOwnFolderTargets`,
  and `TestWSL*More*` cover home overrides (including spaces), instructions,
  unexpanded MCP references, config preservation and WSL discovery,
  NAT/mirrored addresses and stopped-distro behavior.
- The whole suite: `go vet -tags nogui ./... && go test -tags nogui -timeout 45m ./...`.

Run the real-binary check with an installed Junie:

```sh
curl -fsSL https://junie.jetbrains.com/install.sh | bash
MAGPIE_TEST_JUNIE_BIN=~/.local/share/junie/versions/<version>/junie-app/bin/junie \
  go test -race -tags nogui ./internal/agent -run '^TestJunieRealCLI$' -count=1
```

It goes red when magpie leaves `providerForLaunch` in (tried by hand).
