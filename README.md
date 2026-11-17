# Triage Factory

<p align="center">
  <img src="docs/imgs/factory-page.png" alt="Factory" width="90%" />
</p>

<p align="center">An AI-powered software factory where humans decide what gets automated and can take over at any time.</p>

<p align="center">
  <a href="https://github.com/sky-ai-eng/triage-factory/releases">
    <img src="https://img.shields.io/github/v/release/sky-ai-eng/triage-factory?sort=semver&label=latest%20release" alt="Latest release" />
  </a>
  <a href="https://github.com/sky-ai-eng/triage-factory/actions/workflows/test.yml">
    <img src="https://github.com/sky-ai-eng/triage-factory/actions/workflows/test.yml/badge.svg?branch=main" alt="Test" />
  </a>
  <a href="https://github.com/sky-ai-eng/triage-factory/actions/workflows/lint.yml">
    <img src="https://github.com/sky-ai-eng/triage-factory/actions/workflows/lint.yml/badge.svg?branch=main" alt="Lint" />
  </a>
</p>

Triage Factory watches everything that needs attention across your GitHub, Jira, and Linear — open PRs, review requests, CI failures, merge conflicts, assigned tickets — scores and ranks it with AI, and lets you delegate the work to Claude Code agents that run in isolation and stream back to a live dashboard. You decide what gets automated, and you can take over any agent's run at any point.

It's a single Go binary that runs on infrastructure you control. One developer runs it locally — SQLite, credentials in the OS keychain (or an encrypted file when no keychain is reachable). A whole organization runs the _same_ binary self-hosted in multi-tenant mode — Postgres, per-org row-level isolation, and every agent run confined to its own gVisor sandbox. Your code and credentials stay on your infrastructure, and the only things that leave it are API calls to the services you connect — GitHub, Jira, Linear, and your model provider. Local mode is fundamentally the same product, just with N=1 orgs, teams, and users.

For a product tour and screenshots, see [triagefactory.com](https://www.triagefactory.com).

## How it works

Work flows through an automation engine drawn as a factory floor. A durable **entity** (a PR, a Jira ticket, a Linear issue) emits **events** as things happen to it — a review lands, CI fails, a label changes. Events that match your rules become **tasks**; a task you delegate becomes a **run** — one headless Claude Code execution inside an isolated git worktree. You map event types to prompts in a visual graph, so "review requested" routes to your review prompt and "Jira assigned" routes to your implementation prompt. Agents work autonomously and stream their activity back in real time; when a run finishes, you review and approve.

## Local or self-hosted

**Local (N=1)** — one developer, on your laptop. One command to install and run:

```bash
brew tap sky-ai-eng/tap && brew install triagefactory && triagefactory
```

State lives in SQLite, credentials live in the OS keychain or an encrypted file, and agent runs execute in isolated git worktrees (not secured sandboxes). No Postgres, no Docker, no DevOps. For direct downloads, building from source, and the flag reference, see [local mode](docs/users/local/README.md).

**Self-hosted (multi-tenant)** — your whole org, on Linux hosts you control. The same binary and schema, deployed with Docker Compose as a control + executor split: Postgres with per-org row-level security, SSO/SAML sign-in, audit logs, and Slack ingest via the [Enterprise Edition](ee/), a `/usage` dashboard with per-team and org-wide spend caps, and every agent run confined to its own gVisor sandbox. See [self-hosting](docs/users/self-hosting/README.md) to stand one up.

## Pi coding agent (local, experimental)

This fork also supports the Pi coding-agent SDK for local runs. Claude remains
the default; select Pi explicitly when starting the server:

```bash
TF_AGENT_RUNTIME=pi \
  TF_PI_MODEL=anthropic/claude-sonnet-4-5 triagefactory
```

Install `@earendil-works/pi-coding-agent` and configure its provider credentials
first (for example, with Pi's `/login` or provider API-key environment variables).
The adapter discovers the SDK from `pi` on `PATH`, including Nix installations.
Set `TF_PI_SDK_PATH` to the package directory containing `dist/index.js` if
automatic discovery is unavailable. Node.js must also be on `PATH`.

`TF_PI_MODEL` accepts a Pi `provider/model-id` and overrides TF's Claude-oriented
model selection for all Pi calls, including background one-shot calls. If omitted,
TF's configured model must match an installed Pi model ID. Pi uses its own provider
configuration and credentials; stored TF Claude credentials and Claude AI
connectors are not supported. Earendil Works' **Radius** gateway is supported:
run `/login radius` in Pi, choose a model from `/model`, and set
`TF_PI_MODEL=radius/<model-id>`. `RADIUS_API_KEY` is also supported by Pi. Radius
catalogs are read from Pi's cache and can refresh over the network at startup.

The adapter supports TF tools, permission approval, tool-batch context, live
follow-up messages, interruption, usage reporting, and Pi session resume. Pi and
Claude sessions are not interchangeable. Pi sessions live under the run's
`_tfac/pi/sessions` directory. Operator/repository extensions, skills, prompt
templates, context files, and settings are not automatically loaded; TF supplies
the system prompt and tool policy. Restricted Bash grants deliberately require
approval for compound shell commands rather than approximating Claude's shell
parser.

**Pi retains TF's default-on Linux local bubblewrap sandbox for delegated runs.**
It masks the operator's home, TF state, temporary directories and `/run`, keeps a
private PID namespace, and exposes only this run's tree and explicit grants.
Pi's `auth.json` is bound writable for OAuth refresh; `models.json` and the cached
model catalog are read-only. Settings/extensions and Claude credentials remain
masked. `PI_CODING_AGENT_DIR` selects a non-default Pi configuration directory;
prefer keeping it under your home so its parent is private inside the sandbox.
Catalog updates stay in memory, while session transcripts persist in the run tree.
As with TF's Claude local runtime, this is courtesy isolation (same user, shared
network), not the multi-tenant gVisor security boundary. Toolless background jobs
remain direct calls. Pi is still unsupported in multi-tenant mode. Set
`TF_AGENT_RUNTIME=claude` (or unset it) to return to the existing runtime.

## Security & isolation

Triage Factory runs code written by an AI agent acting on untrusted input such as repository contents, issue text, and tool output. Confining that agent, isolating tenants from one another, and keeping real credentials out of reach is central to the product's design. The model below is the self-hosted, multi-tenant posture on Linux; local mode is a single user on their own machine, where runs execute in isolated git worktrees and most security features don't apply.

**The agent is the most confined process in the system.** In multi-tenant mode, each run executes in its own gVisor sandbox: non-root, zero ambient capabilities, a tailored seccomp allowlist, and a per-run memory ceiling. The elevated privileges Triage Factory needs from the host exist only to _build_ that sandbox — never to run the agent inside it. And no single process holds both a dangerous privilege and exposure to the agent's output: the part that can configure the kernel holds no credentials, and the part that holds credentials can't touch the kernel.

**Credentials never enter the sandbox.** The agent's environment is built from scratch with placeholders — a per-run proxy URL and a throwaway token that dies with the run. Your real keys (Anthropic, the GitHub App, the database) live only on the host and are attached on the upstream hop. Dump a live agent's environment and nothing within is sensitive and usable — no real provider key, GitHub token, or database password.

**Tenants are isolated by construction.** Data is fenced with per-org Postgres row-level security. And unlike the usual container setup — every run hanging off one shared Linux bridge, a single ARP-spoof away from its neighbors — each run gets its own point-to-point virtual link on its own private subnet, with no layer-2 path between concurrent runs. Cross-tenant snooping isn't "blocked by a rule"; there's no shared wire to snoop. Each run's egress is a fail-closed allowlist that reaches only the hosts it needs.

**Evaluating Triage Factory for your own infrastructure?** The full threat model, the privilege-separation process model, and what a compromise of each component yields — every claim anchored to something you can run against the binary — are in [docs/users/security/security-overview.md](docs/users/security/security-overview.md).

## Documentation

Everything starts at [docs/README.md](docs/README.md):

- [Local mode](docs/users/local/README.md) — install, run and configure the single-user binary
- [Self-hosting](docs/users/self-hosting/README.md) — deploy and operate the multi-tenant stack
- [Security](docs/users/security/README.md) — the isolation model, for evaluating a deployment
- [Contributing](CONTRIBUTING.md) — contribution terms; contributor docs are in [docs/contributors/](docs/contributors/README.md) and design specs in [specs/](specs/)

## License

Source-available under the [Triage Factory License 1.0](LICENSE): free to use, copy, and modify for your own internal business purposes, but not to redistribute or offer as a hosted service. The [`ee/`](ee/) subtree (Enterprise Edition) is under the same license, with its features gated behind a commercial license key. See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution terms.

## Disclaimer

Triage Factory is provided "as is", without warranty of any kind; see the [LICENSE](LICENSE) for the full terms.

Triage Factory delegates work to autonomous agents that read and write to your source repositories, ticket trackers, and local filesystem. You are solely responsible for reviewing what agents do on your behalf, for the credentials you configure, and for the consequences of any automated actions taken against your systems or third-party services.

**Self-hosted multi-tenant deployments.** When you self-host Triage Factory to serve multiple organizations from one deployment, tenant isolation depends on your infrastructure, configuration, network topology, secrets management, and patching cadence, as well as on the correctness of the upstream software itself — which, like all software, may contain isolation, sandboxing, or row-level-security defects, known or unknown. See [docs/users/security/](docs/users/security/README.md) for the isolation model to evaluate.
