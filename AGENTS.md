# Repository agent instructions

## Worktree and artifact cleanup

Cleanup is part of task completion. First finish required validation and verify
delivery on the freshly fetched remote default branch under this repository's
integration and review rules. A feature-branch push alone is not delivery.

Before removing anything, inspect the target checkout's tracked, untracked,
and ignored files, unique commits, worktree ownership, live agents and builds,
and any locks. Never delete an active, shared, main, pinned, or permanent
checkout. Preserve dirty and untracked work, plus unique ignored evidence,
backups, databases, secrets, and models; `git status` alone cannot reveal
ignored data.

Archive useful reports, receipts, and source identifiers under
`/Volumes/Corellia/Development/Artifacts/<project>/<task>/`. Keep secrets and
private backups out of GitHub. Remove only task-owned, rebuildable outputs.
Use the owning app's managed archive or cleanup with snapshots when available;
otherwise, after verifying safety, use `git worktree remove` without
`--force`. Retain branches with unique commits. Never use broad `rm -rf`
or delete another task's caches.

If safety cannot be proved, retain the item and report the exact reason;
continue unrelated work. Put new heavy temporary builds and artifacts in
task-owned locations on Corellia while it is mounted. Do not globally change
`TMPDIR`, migrate active checkouts or models, or erase chat history. Leave a
concise cleanup receipt listing removed and retained items with evidence.
Do not install scheduling or retention daemons or change unrelated settings.
