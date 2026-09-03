# `argv` and copyable command strings in machine-facing output

Research date: 2026-09-03. Scope: primary documentation and first-party API
references only. This distinguishes *starting a process* from a CLI returning a
suggested next action.

## Finding

There is a strong convention for **direct process-execution APIs**: represent
the executable separately from its arguments (or as an argument sequence), and
do not invoke a shell by default. It prevents a caller from having to apply
shell quoting rules. It is not, however, a universal convention for **agent
shell tools** or for CLI diagnostic JSON.

OpenAI's current Shell tool is a useful counterexample: a `shell_call` has an
`action.commands` array whose elements are shell-command strings (for example,
`"ls -l"`), and the local-executor instructions say to execute those commands.
That interface deliberately carries shell syntax, rather than `executable` plus
`argv`. [OpenAI Shell tool documentation](https://developers.openai.com/api/docs/guides/tools-shell#shell-output-in-responses)

Therefore, `remedy.argv` is technically valuable only if Brigsby explicitly
promises that a consumer can pass it to a **non-shell process runner**. It is
not inherently more "agent friendly" than `remedy.command`: an agent using a
shell-tool interface will need the latter. A human needs a copyable shell
string.

## Evidence: direct process APIs

| API | Native representation | Shell behavior |
| --- | --- | --- |
| Go `os/exec` | `exec.Command(name string, arg ...string)` constructs `Path` and `Args`; the supplied `arg` omits the program name. | It intentionally does not invoke a system shell or perform glob/pipe/redirection expansion. [Go package docs](https://pkg.go.dev/os/exec#Command) |
| Python `subprocess` | The documentation generally prefers a sequence of program arguments, e.g. `["ls", "-l"]`. It says the first element is the program and preserves a space-containing value as one element. | A one-string command with arguments is for `shell=True`; then it must be formatted as typed at a shell prompt. [Python subprocess docs](https://docs.python.org/3/library/subprocess.html#frequently-used-arguments) |
| Node `child_process` | `spawn(command[, args][, options])` and `execFile(file[, args][, options])` take command/file separately from a string-array of arguments. | `execFile` starts the executable directly by default; enabling `shell` changes that, and Node deprecates combining a shell with `args` because values are joined without escaping. [Node child-process docs](https://nodejs.org/api/child_process.html#child_processspawncommand-args-options) |

These are execution interfaces. Their shape is not evidence that a user-facing
diagnostic should duplicate the same data in two forms.

## Evidence: tool and CLI output conventions

OpenAI Shell exposes shell strings to an agent because the contract is to run
shell commands. Its response example is:

```json
"action": { "commands": ["ls -l"] }
```

The same documentation's local Go executor runs each string through
`sh -c`. [OpenAI Shell tool documentation](https://developers.openai.com/api/docs/guides/tools-shell#local-shell-mode)

For an established CLI example, npm documents `npm audit fix` as the human
follow-up command and separately supports JSON output. The docs do **not**
define a generic JSON `action`, `argv`, and `command` triplet for that remedy.
[npm audit documentation](https://docs.npmjs.com/cli/v11/commands/npm-audit/)

I did not find an authoritative, cross-tool standard requiring CLIs to return
both a structured argv value and a human-copyable command string for one
remedy. That is a scoped negative result from the sources above, not a claim
that no product uses such duplication.

## Recommendation for Brigsby

Choose the contract based on the executor, rather than duplicating by default:

1. If Brigsby's documented machine consumer invokes a program directly, make
   the canonical field `remedy.argv` (array of exact strings). A UI may render a
   separately generated, POSIX-shell-quoted display string, but it should be
   explicitly called `shell_command` and must be derived from `argv`.
2. If agents are expected to use general shell tools and people are expected to
   copy/paste, make the canonical field `remedy.command` (a documented shell
   dialect, ideally POSIX `sh`). Do not label it safe for non-shell execution.
3. If both consumers are a first-class Brigsby requirement, returning both is
   reasonable—but declare one canonical and derive/test the other. Use
   `command` plus `argv` inside `remedy`, not duplicate command text in the
   human `message`.

For Brigsby's current simple remedies, a single `remedy.command` is the
smallest clear interface. Add `argv` only with an explicit non-shell execution
contract; otherwise it adds schema and consistency burden without demonstrated
interoperability benefit.
