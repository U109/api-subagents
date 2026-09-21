---
name: api-workers
description: 把明确、重复的代码与文档工作交给用户配置的外部模型，减少 Codex 主模型的阅读和生成量。适合批量补注释、机械修改、定点排查、整理资料，以及用户指定的模型委派；不为简单问答或已能立即完成的小改动增加调度。
---

Use the plugin's API workers to replace parent work, not add another full review. Honor user restrictions on models, budget and external data. Do not infer price from model brands.

## Delegate economically

- Read `list_models` once per task and reuse it unless configuration changes. Select a user-approved worker by purpose. If none is permitted or available, continue locally; never silently switch providers.
- Offload bounded routine work that would otherwise require substantial parent reading or repetitive generation. Keep architecture, ambiguous requirements and final acceptance with the parent. A trivial one-line fix rarely benefits from delegation.
- Give one worker relevant paths, the exact change, constraints and acceptance checks. Let it read those files itself. Do not first read the entire scope or repeat its investigation while it runs. Batch related edits; avoid elaborate plans and duplicate reviewers.
- Prefer `propose_edit` for existing-file changes. Request a short outcome and necessary checks, not source dumps or a work diary. Workers cannot run commands or apply changes.

## Receive and apply

- `delegate_task` and `wait_task` default to a single blocking wait of up to 10 minutes (`600000` ms), returning immediately on completion. Use `wait_ms: 0` when useful independent work exists, do that work first, then wait with the default. Short waits repeatedly return control to the parent; do not use them or `task_status` polling merely to check progress. Compact results are the default.
- A wait timeout with a running status is not a failed task. Keep the same `task_id` and use `nextWaitMs` for the next wait if still blocked; do not resubmit, restart workers, or narrate unchanged progress. If the host rejects or cuts off a long wait, report the timeout/configuration problem instead of entering a short retry loop. Waiting does not cancel background work; use `cancel_task` when the task itself should stop. A running or failed status is not completion.
- Inspect returned edit fragments and run focused checks once. Do not reread unchanged files or repeat a full audit unless a concrete discrepancy requires it. For omitted details use `task_status` with `detail: full` once, or inspect the local `resultFile`.
- After reviewing changes within the user's authorized scope, execute the returned `applyCommand` argument array through parent shell tools with proper shell quoting. Optional relative paths after the workspace select reviewed files only. This helper checks workspace, original SHA and redaction before applying stored proposals; workers never execute it. Avoid copying whole files through the parent.
- Never apply `redacted: true` content verbatim. Reconcile concurrent changes first. The helper can report partially applied paths after a filesystem failure; inspect those before retrying.
- Continue a completed same-model/workspace task only for a specific unresolved gap. Stop when acceptance checks pass. Report API errors without open-ended retries. Use `cancel_task` when cancelled.

This reduces orchestration and returned content; it cannot measure or guarantee savings in Codex quota. External models have separate API costs. Streaming improves responsiveness, not token pricing.
