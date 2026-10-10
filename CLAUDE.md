# CLAUDE.md — Project Conventions for new-api

## MANDATORY: Read AGENTS.md with the Read tool

Do not treat `@AGENTS.md` as loaded. Claude Code does not reliably inline that import.

Before any planning, coding, reviewing, or answering a project question, you MUST call the Read tool on the repo-root file `AGENTS.md` and wait for the full contents. This is the first action of every session and every new task.

Rules:

- Do not start from memory, summaries, or this file alone.
- Do not skip the Read because a previous turn mentioned AGENTS.md.
- Do not replace the Read with a grep, glob, or partial skim.
- After reading, follow every rule in `AGENTS.md` for the rest of the work.
- If the task touches `web/`, also Read `web/AGENTS.md` before editing frontend files.
- If the task touches billing as defined under **Billing rules (mandatory read gate)** in `AGENTS.md`, also Read `.agents/rules/billing.md` in full before planning or editing. Tasks outside that definition may skip it.

## Agent skills

### Issue tracker

使用 a8ss/new-api 的 GitHub Issues；处理工单前读取 `docs/agents/issue-tracker.md`。

### Triage labels

使用五个默认分诊标签；分诊前读取 `docs/agents/triage-labels.md`。

### Domain docs

采用 single-context 布局；探索代码前读取 `docs/agents/domain.md`。
