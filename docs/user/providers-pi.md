# Pi

AgentTunnel uses the Pi setup on the connected environment. With a remote environment, its Pi login
and configuration apply, not the setup on your desktop or phone.

Pi is off by default. Add or enable a Pi provider in **Settings > AgentTunnel** to turn it on.
AgentTunnel runs the `pi` binary on the environment's `PATH`. Set **Binary path** on a Pi provider to
point at a different install or a specific executable. The value accepts a bare command name such as
`pi` or an absolute path. You can add more than one Pi provider, each with its own binary path and
provider environment variables.

## Authentication

AgentTunnel does not store Pi credentials. Sign in with Pi itself on the environment: run `pi` and
use `/login` for a subscription provider, or set the provider's API-key environment variables (for
example `ANTHROPIC_API_KEY`) on the environment or on the Pi provider instance.

AgentTunnel marks the Pi provider ready once the CLI responds and lists models. It does not verify
your login state. If you are not signed in, starting a turn reports Pi's own authentication error.
Text generation (commit messages, pull request text, branch names, and thread titles) also runs
through Pi and needs the same authentication.

## Refresh the model list

AgentTunnel loads the model list from `pi --list-models` when an enabled Pi provider starts and keeps
the list in its cache. Reconnecting a client or using a refresh control asks Pi for the list again.
The periodic provider health setting does not refresh Pi's catalog.

After changing a Pi login, a configured provider, or the models Pi can reach outside AgentTunnel,
open **Settings > AgentTunnel**, select the environment, and choose **Refresh provider status**.
Changing the provider's configuration in AgentTunnel also replaces that provider connection.

On mobile, open the thread settings and select **Refresh models**. The control stays disabled while
the refresh runs and shows an error if the refresh fails.

Pi lists its models without requiring a login, so the catalog can be available before you
authenticate. Model identifiers use the `provider/model` form, and the picker groups them by
provider.

If a refresh fails, AgentTunnel keeps the last known models. Fix the problem, then refresh again. A
successful refresh can remove entries that Pi no longer offers.

## Continue an existing thread

Each thread is pinned to one Pi session. When a thread starts, AgentTunnel uses the thread's id as
the Pi session id, so continuing the same thread resumes the same Pi session even after a client
reconnects or the environment restarts. Pi stores that session as a session file in its own config
directory; AgentTunnel does not read or manage those files directly.

You can switch models within a running thread. AgentTunnel sends the new model to Pi before the next
turn.

An existing thread keeps its selected model when that model is temporarily absent from the catalog.
The web picker shows an **Unavailable** row and keeps saved option values visible until the model
metadata returns. AgentTunnel does not switch the thread to the first model in the list. The stored
selection does not guarantee that Pi can still run the model; if Pi rejects it, select an available
model before trying again.

## Limitations

- **No approval prompts.** The Pi mode AgentTunnel uses has no permission or approval protocol, so
  the tools Pi runs execute without an AgentTunnel approval step and the thread's runtime mode does
  not change Pi's access. Approvals and user-input requests are not routed to you.
- **No sandbox flag.** AgentTunnel cannot start Pi in a restricted sandbox. Use Pi's own tool and
  extension configuration to limit what it can do.
- **No extension dialogs.** If a Pi extension asks for interactive input, AgentTunnel cannot display
  the dialog. It cancels the request so the agent can continue and shows a warning instead.
- **Usage and cost are not in the Usage page.** AgentTunnel tracks the token counts Pi reports per
  turn and shows them on the thread's context meter, but the Review usage page does not include Pi
  activity, provider shares, or an API-equivalent cost for Pi models.
