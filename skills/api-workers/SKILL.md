---
name: api-workers
description: 把边界明确的代码与文档任务交给用户配置的外部模型，可只返回修改建议，或通过隔离 Codex worker 直接改文件、执行命令和测试。适合批量修改、定点排查及用户指定的模型委派；不为简单问答或小改动增加调度。
---

Use the plugin's API workers to replace parent work, not add another full review. Honor user restrictions on models, budget and external data. Do not infer price from model brands.

## Delegate economically

- Read `list_models` once per task and reuse it unless configuration changes. `model` is the configured connection name; optional `model_id` selects an exact entry from its `availableModels`, otherwise the connection default is used. All configured model brands use the same mechanism, but coding requires compatible tool calls. If none is permitted or available, continue locally; never silently switch providers or models.
- Offload bounded routine work that would otherwise require substantial parent reading or repetitive generation. Keep architecture, ambiguous requirements and final acceptance with the parent. A trivial one-line fix rarely benefits from delegation.
- Give one worker relevant paths, the exact change, constraints and acceptance checks. Let it read those files itself. Do not first read the entire scope or repeat its investigation while it runs. Batch related edits; avoid elaborate plans and duplicate reviewers.
- Request a short outcome and necessary verification, not source dumps or a work diary. Choose the execution mode below before dispatching.

## Choose execution mode

- Default `execution_mode: proposal` uses bounded file tools and returns proposals. It cannot execute commands or write files. Prefer `propose_edit` for existing-file changes; the parent reviews and applies them.
- Use `execution_mode: codex` when the user wants an external model to implement and verify a bounded task. It starts a separate, ephemeral Codex App Server worker without switching the parent's provider. Use explicit `access: workspace-write` for edits/tests that write files; omitted access is `read-only`. The worker uses the chosen connection's protocol adapter, not native cross-provider subagent handoff.
- Execution permissions are independent of the parent session: never request more than the user's authorization and the parent's permitted scope. Command network access is off by default; set `network_access: true` only with explicit authorization and `workspace-write`. There is no full-access or automatic approval fallback. If sandbox setup, permissions or tools fail, report the blocker instead of retrying without restrictions.
- Execution workers directly share the specified workspace; they do not create worktrees or roll back. Do not edit overlapping files while they run. The server serializes overlapping workspaces within one MCP process; for parallel writing use separate worktrees and explicit integration. Other Codex tasks/processes are not covered by this lock.
- `max_steps` caps proposal loop rounds or execution-worker model requests (1–30, default 8). Use a sufficient explicit cap for implementation/testing. A Codex worker is a self-contained job: `continuation_id` is currently proposal-only. Do not relaunch a running job as a substitute for continuation.

## Receive and apply

- `delegate_task` and `wait_task` default to a single blocking wait of up to 10 minutes (`600000` ms), returning immediately on completion. Use `wait_ms: 0` when useful independent work exists, do that work first, then wait with the default. Short waits repeatedly return control to the parent; do not use them or `task_status` polling merely to check progress. Compact results are the default.
- A wait timeout with a running status is not a failed task. Keep the same `task_id` and use `nextWaitMs` for the next wait if still blocked; do not resubmit, restart workers, or narrate unchanged progress. If the host rejects or cuts off a long wait, report the timeout/configuration problem instead of entering a short retry loop. Waiting does not cancel background work; use `cancel_task` when the task itself should stop. A running or failed status is not completion.
- For proposals, inspect returned edit fragments and run focused checks once. For execution results (`changeDelivery: workspace`), inspect actual diffs and the returned command statuses/exit codes: files were changed directly, so do not apply them again. `changedFiles` records patch events, not a complete inventory of shell-written files; a successful turn does not mean every test command passed. Avoid repeating verification unless evidence is missing or a concrete discrepancy requires it. For omitted details use `task_status` with `detail: full` once, or inspect the local `resultFile`.
- Only for proposals: after reviewing changes within the user's authorized scope, execute the returned `applyCommand` argument array through parent shell tools with proper shell quoting. Optional relative paths after the workspace select reviewed files only. This helper checks workspace, original SHA and redaction before applying stored proposals; workers never execute it. Avoid copying whole files through the parent.
- Never apply `redacted: true` content verbatim. Reconcile concurrent changes first. The helper can report partially applied paths after a filesystem failure; inspect those before retrying.
- Continue a completed same-model/workspace proposal task only for a specific unresolved gap. Stop when acceptance checks pass. Report API errors without open-ended retries. Use `cancel_task` when cancelled. Execution failure/cancellation may leave partial file changes; inspect them before a new task. Cancellation stops local processes but does not promise that the upstream provider stops billing.

This reduces orchestration and returned content; it cannot measure or guarantee savings in Codex quota. External models have separate API costs. Streaming improves responsiveness, not token pricing.
