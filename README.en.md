# Claude Environment Evidence

**Understand your Claude Code environment before changing it.**

A local macOS tool that brings installation, configuration, proxy selection and a limited reference connection into one view. See what passed, what failed and what remains unknown.

[中文](README.md) · [Install](INSTALL.md) · [For AI Agents](#for-ai-agents) · [Validation](references/validation.md)

## What you can do

- Locate Claude Code and check its version in a restricted sandbox.
- Review basic JSON/env configuration and observed proxy conflicts.
- After confirmation, inspect one reference connection across DNS, TCP/CONNECT, TLS and HTTP.
- Read an explainable score alongside evidence coverage and explicit unknowns.
- Preview a loopback HTTP proxy plan for a supported new native Claude process, without changing the original shell or system settings.

There is no active outbound probe by default. Local inspection, a network probe and a new Claude session require separate consent. The tool can start independently of Claude Code.

## For AI Agents

Use **Codex**, **Kimi Code**, or another Coding Agent with local file and terminal tools, including agents using **DeepSeek** models. Claude Code is the subject of inspection; use a separate agent to install and diagnose it.

Copy this request into your agent:

```text
Install “Claude Environment Evidence” from:
https://github.com/TIANTIAN-ZHAN/cc-anti-ban-skill

Read INSTALL.md and SKILL.md in the complete repository first. Follow INSTALL.md
to check prerequisites and build with scripts/build-safe.zsh. Run --help only
after this build succeeds. Preserve the full repository and existing directories.
Report missing access or dependencies without requesting plaintext tokens.
Install the standalone tool only; do not install an Agent Skill automatically.
Do not run inspections, serve, network probes, Claude or model calls, or change
system settings. Report the repository/binary paths and build/help results,
then wait for me to select a project and authorize inspection.
```

**[INSTALL.md is the authoritative procedure](INSTALL.md)** (Chinese, with executable shell blocks). Installation ends at a successful build and `--help`. Optional Codex Skill linking is documented separately. Kimi and other agents can read the complete repository directly; client loading and end-to-end compatibility have not been verified. Do not install this diagnostic Skill into `.claude/skills` or rely on Claude to inspect itself.

## Current interface · simulated data

![Current Chinese UI with fictional fixture data, not a real machine report](docs/assets/current-ui-fixture.png)

Captured from the current isolated UI test fixture. All environment data are fictional; no real configuration or external service was accessed. The UI currently uses Chinese. A missing reference probe remains unknown rather than receiving an invented score.

## Get started

1. Build the tool and verify `--help` using [INSTALL.md](INSTALL.md).
2. After consent, select a project and open the local interface. Read the disclosure before starting inspection.
3. Review or export the redacted report. Confirm a reference probe or new-session proxy plan only when needed.

## Scope and requirements

Requires macOS, Go 1.25+, clang/SDK and a trusted, already-running zsh. Tested on macOS 27 arm64; the 13.0 deployment target is not proof of older macOS or Intel compatibility. No prebuilt packages are provided. Full compatibility with the official Claude CLI remains unverified.

Local inspection enumerates the full process environment and reads a bounded user settings file. These may contain credentials and enter local memory. Raw contents are not sent to external services, logged or exported; immediate memory erasure is not guaranteed. [Privacy and consent](references/safety-and-privacy.md)

Scores describe limited environment and reference-link evidence. They do not validate accounts, region eligibility, quota, models or Claude's effective route. Normal Claude sessions can make their own network requests, write files and run hooks/MCP. [Report contract](references/report-contract.md) · [Agent workflow](SKILL.md)

Independent project, not affiliated with Anthropic. The canonical repository and Skill name is `cc-anti-ban-skill`; “anti-ban” is not a promise to prevent bans or recover accounts. [MIT License](LICENSE) · [Notices](NOTICE.md)
