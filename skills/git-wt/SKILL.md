---
name: git-wt
description: Use the `git-wt` CLI to manage Git worktrees in the bare repository layout. Load when a task mentions `git wt`, `git-wt`, creating/removing/switching worktrees, or when the repository has a `.bare` directory with a `.git` file pointing to it.
---

# git-wt

Use this skill when the user asks to manage Git worktrees with `git-wt` or when a repository uses the bare worktree layout: Git data lives in `.bare`, `.git` is a file pointing at `./.bare`, and branch worktrees are sibling directories. `git-wt` needs Git 2.48.0 or newer.

## Detecting `git-wt` repositories

- From any directory in the repo, run:

  ```shell
  git rev-parse --git-common-dir
  ```

  If the result ends in `.bare`, treat the repo as a `git-wt` bare worktree layout.

- At a repository root, a `.git` file containing `gitdir: ./.bare` and a `.bare` directory are also strong signals.
- A `.git-wt-migrate/` directory means a migration was interrupted. Run `git wt migrate` to restore the original layout before doing anything else, and never delete that directory by hand.
- If uncertain, run:

  ```shell
  git wt doctor
  ```

## Operating rules for agents

- Prefer `git wt` (or `git-wt`) over raw `git worktree` commands in a `git-wt` layout so path resolution, fetching, branch cleanup, and safety checks stay consistent.
- Prefer explicit, non-interactive commands. Bare `git wt add`, `git wt remove`, and `git wt switch` open `fzf` pickers or prompts.
- Output contract:
  - `git wt add` prints the absolute created worktree path on stdout. Capture it if you need to `cd` into the new worktree.
  - `git wt switch` prints only the selected path on stdout.
  - Progress, prompts, warnings (`Warning:`), errors (`Error:`), and `Cancelled` go to stderr.
- Cancelling a picker, prompt, or confirmation prints `Cancelled` and exits 0. Do not treat exit 0 as proof that something changed; check the output or the repository.
- Without a terminal, prompts read answers from stdin. Pipe answers with `printf`, as in the examples below.
- Use dry-runs before destructive commands.
- Never delete remote branches. Do not pass `--delete-remote` or push a branch deletion, even if asked; tell the user to run it themselves.
- `DEBUG=1` before a mutating command prints the underlying `git` operations instead of executing them.

## Inspect

```shell
git wt doctor
git wt status
git wt list --json
git wt ls --json
git wt list --porcelain -z
```

`git wt list --json` returns an array of non-bare worktrees with `path`, `branch`, full `head` ID, and `detached`, `locked`, `locked_reason`, `prunable`, and `prunable_reason`. Empty results are `[]`. Do not combine `--json` with native options such as `--porcelain`.

## Clone

```shell
git wt clone <url> [folder]
```

If the remote's default branch cannot be discovered, clone asks for a branch name; no input skips worktree creation. If clone fails after the download, it keeps the repository, exits non-zero, and prints recovery commands. Do not delete the download; follow those commands.

## Migrate

```shell
git wt migrate --dry-run
printf 'y\n' | git wt migrate
```

Migration is experimental. Ask the user to stop editors, builds, and other Git commands first.

- It converts the repository in place with renames, so it needs no extra disk space and keeps file modes, xattrs, and ACLs. `.git` becomes `.bare`, and the working files move into a worktree for the current branch.
- Every step is journaled in `.git-wt-migrate/`. A failed step, failed verification, Ctrl-C, SIGTERM, or SIGHUP rolls everything back.
- After a crash, the next `git wt migrate` restores the original layout and exits non-zero; run it again to migrate.
- There is no backup copy afterwards. Verification compares config, remotes, refs, index, stashes, and `git status` at the final paths.
- It refuses in-progress operations, submodules, linked worktrees, sparse checkout, reftable, config includes whose paths break when `.git` moves, and a `core.hooksPath` inside `.git`. Report the refusal to the user. Do not strip settings or flatten config to get around it.
- If the default-branch worktree cannot be created, migration still succeeds and prints the `git wt add` command to create it.

## Create worktrees

```shell
git wt add feature origin/feature
git wt add hotfix/memory-leak-fix
git wt add -b new-feature new-feature
git wt add --detach hotfix HEAD~5
```

Without `-b`, `-B`, `--detach` or `--orphan`, the branch takes the full path from the repository root: `git wt add hotfix/x` checks out `hotfix/x`, tracking `origin/hotfix/x` when it exists. `git wt add <path> origin/<branch>` creates local `<branch>` tracking the remote, not a detached HEAD; if `<branch>` is already checked out in another worktree, it warns and falls back to a detached HEAD.

A path starting with a remote name, such as `git wt add origin/team/x`, checks out `team/x` tracking `origin/team/x`. If that remote branch does not exist and the prefix is the default remote, add fails and suggests `git wt add team/x`; pass that instead of guessing.

If add reuses an existing local branch that differs from the selected remote branch, it warns on stderr and keeps the local commit. Tell the user rather than resetting the branch.

## Update

```shell
git wt update
```

`update` runs `git fetch --all --prune`, then `git pull` in the default branch's worktree, following the user's Git config. Tag pruning follows `fetch.pruneTags`, `remote.<name>.pruneTags`, and refspecs, and may delete local-only tags. Pull follows `pull.rebase`, `branch.<name>.rebase`, and `pull.ff`. Do not assume fast-forward-only updates or override these settings without authorization.

## Switch

- For an interactive human workflow:

  ```shell
  cd "$(git wt switch)"
  ```

- For an agent workflow, parse `git wt list --json` and `cd` to an object's `path` instead of invoking the picker.

## Remove

Removal protects tracked changes, non-ignored untracked files, and commits that no other branch or tag has. Ignored files, including `.env` files and build output, do not block removal and are deleted with the worktree. Check for valuable ignored files before removing anything.

- Every target is checked before the confirmation prompt and before hooks run.
- On a terminal, a target with work to discard lists it and asks for the worktree name; Enter skips that target and the rest continue.
- Without a terminal, such targets are skipped with a `Warning: Skipped …` line, the rest are removed, and the command exits 1. Read the warnings and report skipped targets to the user.
- Never type a worktree name at that prompt, or retry with `--force`, unless the user explicitly authorizes discarding that work.

```shell
git wt remove feature --dry-run
printf 'y\n' | git wt remove feature
```

Cleanup filters select only safe candidates and never accept `--force`:

```shell
git wt remove --sweep --dry-run
printf 'cleanup\n' | git wt remove --sweep
```

- `--merged` and `--gone` require the branch to be fully merged into the cleanup base. `--stale` prunes missing, unlocked worktree metadata and keeps the branches.
- Without `wt.cleanupBase`, cleanup discovers the remote default branch, bounded by `wt.remoteTimeout` (default `10s`). If `branch.<name>.remote=.` blocks that, set an explicit base:

  ```shell
  git config wt.cleanupBase refs/heads/main
  git config wt.cleanupBase refs/remotes/origin/main
  ```

## Suggested workflow

1. Inspect first with `git wt doctor`, `git wt status`, or `git wt list --json`.
2. Explain the planned operation when it will create, remove, or migrate worktrees.
3. Prefer a dry-run or `DEBUG=1` preview where available.
4. Execute the explicit `git wt` command.
5. Report created paths, removed or skipped worktrees, deleted local branches, warnings, and any follow-up command the user should run.
