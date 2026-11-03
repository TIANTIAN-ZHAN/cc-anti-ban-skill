# CC Anti-Ban

**Claude Code stopped working. Is it really a ban?**

Check the environment before drawing conclusions. A local macOS diagnostic tool for Claude Code that can start even when Claude itself is not working.

[中文](README.md) · [Install](INSTALL.md) · [For AI Agents](#for-ai-agents) · [Online check reference](#online-check-reference) · [Discuss an error](#discuss-an-error)

## Start with what you can verify

Requests time out. Connections fail. Switching proxies still leaves you stuck. Those symptoms alone do not tell you whether an account was suspended.

**CC Anti-Ban** checks installation, basic configuration and startup proxy selection. When you choose, run one limited reference connection probe. See what passed, what failed and what remains unknown before deciding what to try next.

**Received an explicit account suspension notice?** Use the [official appeal process](https://support.claude.com/en/articles/8241253-safeguards-warnings-and-appeals). This tool cannot determine or reverse an account suspension.

## What you can do

- Locate Claude Code and check its version in a restricted sandbox.
- Review basic JSON/env configuration and observed proxy conflicts.
- After confirmation, inspect one reference connection across DNS, TCP/CONNECT, TLS and HTTP.
- Read an explainable score alongside evidence coverage and explicit unknowns.
- Preview a loopback HTTP proxy plan for a supported new native Claude process, without changing the original shell or system settings.

A `403` can also reflect missing model or resource permissions; a status code alone does not establish an account ban. See [official Claude Code troubleshooting](https://support.claude.com/en/articles/14552646-troubleshoot-claude-code-installation-and-authentication) and [API errors](https://platform.claude.com/docs/en/api/errors).

There is no active outbound probe by default. Local inspection, a network probe and a new Claude session require separate consent. The tool can start independently of Claude Code.

## For AI Agents

Use **Codex**, **Kimi Code**, or another Coding Agent with local file and terminal tools, including agents using **DeepSeek** models. Claude Code is the subject of inspection; use a separate agent to install and diagnose it.

Copy this request into your agent:

```text
Install “CC Anti-Ban” from:
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

## Online check reference

> **[Net.Coffee · Claude AI IP risk check →](https://ip.net.coffee/claude/)**
>
> Thanks to Net.Coffee for the interface inspiration behind our early scoring demo.

This independent third-party site is not integrated into CC Anti-Ban. Its results are not an Anthropic determination of account status.

## Get started

1. Build the tool and verify `--help` using [INSTALL.md](INSTALL.md).
2. After consent, select a project and open the local interface. Read the disclosure before starting inspection.
3. Review or export the redacted report. Confirm a reference probe or new-session proxy plan only when needed.

## Discuss an error

Timeout, connection failure, or a specific error? [Share feedback in Issues](https://github.com/TIANTIAN-ZHAN/cc-anti-ban-skill/issues): describe the symptom, reproduction steps, and which environment checks passed, failed or remained unknown.

Share redacted excerpts only. Do not upload raw configuration, full environment variables, account details, email addresses, tokens, or proxy/subscription URLs containing credentials. They are not needed for feedback.

## Scope and requirements

Requires macOS, Go 1.25+, clang/SDK and a trusted, already-running zsh. Tested on macOS 27 arm64; the 13.0 deployment target is not proof of older macOS or Intel compatibility. No prebuilt packages are provided. Full compatibility with the official Claude CLI remains unverified.

Local inspection enumerates the full process environment and reads a bounded user settings file. These may contain credentials and enter local memory. Raw contents are not sent to external services, logged or exported; immediate memory erasure is not guaranteed. [Privacy and consent](references/safety-and-privacy.md)

Scores describe limited environment and reference-link evidence. They do not validate accounts, region eligibility, quota, models or Claude's effective route. Normal Claude sessions can make their own network requests, write files and run hooks/MCP. [Report contract](references/report-contract.md) · [Agent workflow](SKILL.md)

Independent project, not affiliated with Anthropic. The canonical repository and Skill name is `cc-anti-ban-skill`; the “Anti-Ban” brand is not a promise to prevent bans or recover accounts. [MIT License](LICENSE) · [Notices](NOTICE.md)
