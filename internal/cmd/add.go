package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ahmedelgabri/git-wt/internal/git"
	"github.com/ahmedelgabri/git-wt/internal/hook"
	"github.com/ahmedelgabri/git-wt/internal/picker"
	"github.com/ahmedelgabri/git-wt/internal/ui"
	"github.com/ahmedelgabri/git-wt/internal/worktree"
	"github.com/spf13/cobra"
)

var addCmd = &cobra.Command{
	Use:   "add [options] [<path>] [<commit-ish>]",
	Short: "Create a new worktree",
	Long: `Create a new worktree. With no arguments, opens an interactive picker to
select from remote branches or create a new branch. Common git worktree add
flags are supported; see the options below.

Always fetches from the remote before creating the worktree. When using -b/-B,
upstream tracking is set automatically if the branch exists on the remote.

Without -b/-B/--detach/--orphan, the branch is named after the full path from
the repository root, so hotfix/x checks out hotfix/x, not x. An existing local
branch is used as is; otherwise the branch is created to track the remote
branch of that name when one exists, or from HEAD. A path starting with a
remote name, such as origin/team/x, checks out team/x tracking origin/team/x.
Given a remote branch as <commit-ish>, a local branch of the same name is
checked out (created to track it if needed) instead of a detached HEAD, unless
that branch is checked out in another worktree.

On success, prints the absolute created worktree path to stdout. Human-oriented
progress, prompts, and hints are written to stderr.`,
	Example: `  git wt add                               # Interactive selection
  git wt add feature origin/feature        # Local feature tracking origin/feature
  git wt add hotfix/memory-leak-fix        # Branch hotfix/memory-leak-fix
  git wt add -b new-feature new-feature    # New branch
  git wt add --detach hotfix HEAD~5        # Detached HEAD worktree`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runAdd,
}

func init() {
	f := addCmd.Flags()
	f.StringP("branch", "b", "", "Create a new branch")
	f.StringP("force-branch", "B", "", "Create or reset a branch")
	f.BoolP("detach", "d", false, "Detach HEAD at the new worktree")
	f.BoolP("force", "f", false, "Checkout even if branch is checked out in another worktree")
	f.Bool("lock", false, "Lock the worktree after creation")
	f.String("reason", "", "Lock reason (use with --lock)")
	f.BoolP("quiet", "q", false, "Suppress feedback messages")
	f.Bool("no-checkout", false, "Don't populate the worktree")
	f.Bool("no-track", false, "Don't set up upstream tracking")
	f.Bool("guess-remote", false, "Try to match new branch with remote-tracking branch")
	f.Bool("orphan", false, "Create worktree with an orphan branch")
	rootCmd.AddCommand(addCmd)
}

func runAdd(cmd *cobra.Command, args []string) error {
	// Resolve paths once without changing the process working directory.
	root, err := worktree.BareRoot()
	if err != nil {
		return err
	}

	var createdPath string

	interactive := len(args) == 0 && !cmd.Flags().Changed("branch") && !cmd.Flags().Changed("force-branch")
	if interactive {
		createdPath, err = runAddInteractive(root)
		// Cancelling the picker or a prompt is a choice, not a failure.
		if ui.IsCanceled(err) {
			ui.Cancelled()
			return nil
		}
		if err != nil {
			return err
		}
	} else {
		remote := worktree.DefaultRemoteIn(root)
		if remote != "" {
			if err := ui.SpinWithOutputRawContext(fmt.Sprintf("Fetching from %s", remote), func(ctx context.Context, w io.Writer) error {
				return git.RunInToContext(ctx, root, w, "fetch", remote, "--prune")
			}); err != nil {
				return err
			}
		}

		createdPath, err = runAddDirect(cmd, args, remote, root)
		if err != nil {
			return err
		}
	}

	return printCreatedWorktreePath(createdPath)
}

func fetchInteractiveBranches() error {
	remotes, err := git.QueryLines("remote")
	if err != nil || len(remotes) == 0 {
		return nil
	}

	return ui.SpinWithOutputRawContext("Fetching from all remotes", func(ctx context.Context, w io.Writer) error {
		return git.RunToContext(ctx, w, "fetch", "--all", "--prune")
	})
}

func runAddInteractive(root string) (string, error) {
	items, err := loadInteractiveAddItems(context.Background())
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", nil
	}

	result, err := picker.Run(picker.Config{
		Items:      items,
		Prompt:     "Select branch or create new: ",
		PreviewCmd: previewBranchCmdStr(),
	})
	if err != nil {
		return "", err
	}

	if result.Canceled || len(result.Items) == 0 {
		return "", context.Canceled
	}

	selected := result.Items[0]

	if selected.Value == createNewBranchValue {
		return createNewBranch(root)
	}

	remote, branch := splitRemoteBranchRef(selected.Value)
	if remote == "" || branch == "" {
		return "", fmt.Errorf("invalid remote branch ref: %s", selected.Value)
	}

	wtPath, err := promptWorktreePath(branch)
	if err != nil {
		return "", err
	}

	wtPath = addPath(root, wtPath)
	_, existsErr := git.QueryIn(root, "show-ref", "--verify", "refs/heads/"+branch)
	existing := existsErr == nil
	err = runAddLifecycle(root, wtPath, branch, func() error {
		// Create worktree from selected remote branch.
		if err := ui.SpinWithOutputContext(fmt.Sprintf("Creating worktree for %s", ui.Accent(branch)), func(ctx context.Context, w io.Writer) error {
			if existing {
				return git.RunInToContext(ctx, root, w, "worktree", "add", "--", wtPath, branch)
			}
			return git.RunInToContext(ctx, root, w, "worktree", "add", "-b", branch, "--", wtPath, selected.Value)
		}); err != nil {
			return err
		}

		// Existing branches keep their tip and configured upstream.
		if existing {
			warnReusedLocalBranch(root, branch, selected.Value)
			return nil
		}
		// Set upstream tracking.
		return ui.SpinWithOutputContext(fmt.Sprintf("Setting upstream to %s", ui.Accent(selected.Value)), func(ctx context.Context, w io.Writer) error {
			return git.RunInToContext(ctx, root, w, "branch", "--set-upstream-to="+selected.Value, branch)
		})
	})
	if err != nil {
		return "", err
	}
	return wtPath, nil
}

// The picker showed a remote ref, so say when the worktree got a local branch
// at a different commit instead.
func warnReusedLocalBranch(root, branch, selected string) {
	local, err := git.QueryIn(root, "rev-parse", "--short", "refs/heads/"+branch)
	if err != nil {
		return
	}
	remote, err := git.QueryIn(root, "rev-parse", "--short", selected)
	if err != nil || local == remote {
		return
	}
	ui.Warnf("Using existing local branch %s at %s; %s is at %s. Compare with: git log --oneline --left-right %s...%s",
		branch, local, selected, remote, branch, selected)
}

func createNewBranch(root string) (string, error) {
	branchName, err := ui.PromptInputResult("Enter new branch name:")
	if err != nil {
		return "", err
	}

	if branchName == "" {
		return "", fmt.Errorf("branch name cannot be empty")
	}

	wtPath, err := promptWorktreePath(branchName)
	if err != nil {
		return "", err
	}

	wtPath = addPath(root, wtPath)
	err = runAddLifecycle(root, wtPath, branchName, func() error {
		return ui.SpinWithOutputContext(fmt.Sprintf("Creating worktree for %s", ui.Accent(branchName)), func(ctx context.Context, w io.Writer) error {
			return git.RunInToContext(ctx, root, w, "worktree", "add", "-b", branchName, "--", wtPath)
		})
	})
	if err != nil {
		return "", err
	}
	return wtPath, nil
}

func promptWorktreePath(defaultPath string) (string, error) {
	wtPath, err := ui.PromptInputResult(fmt.Sprintf("Enter worktree path (optional, default: %s):", defaultPath))
	if err != nil {
		return "", err
	}
	if wtPath == "" {
		return defaultPath, nil
	}
	return wtPath, nil
}

func addPath(root, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, path)
}

// inferredAdd is how direct mode checks out a branch when no branch flag is
// given. Git would name the branch after the path's last element, or detach
// HEAD at a remote ref; both lose names like hotfix/x.
type inferredAdd struct {
	branch string
	// create makes branch with -b, starting at start, or HEAD when empty.
	create bool
	start  string
	// upstream is the remote ref the new branch tracks.
	upstream string
	// requested is the remote ref asked for when an existing local branch is
	// used instead, so a differing tip can be reported.
	requested string
	// detached checks out start with a detached HEAD, because its branch is
	// checked out in another worktree.
	detached bool
}

func (p inferredAdd) gitArgs(path string) []string {
	switch {
	case p.detached:
		return []string{"--detach", "--", path, p.start}
	case !p.create:
		return []string{"--", path, p.branch}
	}
	args := []string{"-b", p.branch, "--", path}
	if p.start != "" {
		args = append(args, p.start)
	}
	return args
}

// planInferredAdd handles `add <path>` and `add <path> <remote>/<branch>`. It
// reports false when git's own behaviour should apply unchanged.
func planInferredAdd(root, remote string, args []string) (inferredAdd, bool, error) {
	switch len(args) {
	case 1:
		branch, ok := inferBranchFromPath(root, args[0])
		if !ok {
			return inferredAdd{}, false, nil
		}
		// A path like origin/team/new names a remote branch, not a local one.
		if r, b, ok := splitRemotePrefix(remoteNames(root), branch); ok {
			if !refExists(root, "refs/remotes/"+r+"/"+b) {
				return inferredAdd{}, false, fmt.Errorf("%s is not a remote branch, and %s would be an ambiguous local branch name; did you mean `git wt add %s`?", branch, branch, b)
			}
			return planFromRemoteBranch(root, r+"/"+b, b), true, nil
		}
		if localBranchExists(root, branch) {
			return inferredAdd{branch: branch}, true, nil
		}
		if remote != "" && refExists(root, "refs/remotes/"+remote+"/"+branch) {
			ref := remote + "/" + branch
			return inferredAdd{branch: branch, create: true, start: ref, upstream: ref}, true, nil
		}
		return inferredAdd{branch: branch, create: true}, true, nil
	case 2:
		ref, branch, ok := remoteBranchRef(root, args[1])
		if !ok {
			return inferredAdd{}, false, nil
		}
		return planFromRemoteBranch(root, ref, branch), true, nil
	}
	return inferredAdd{}, false, nil
}

// planFromRemoteBranch checks out branch for the remote ref <remote>/<branch>,
// as the interactive picker does.
func planFromRemoteBranch(root, ref, branch string) inferredAdd {
	if !localBranchExists(root, branch) {
		return inferredAdd{branch: branch, create: true, start: ref, upstream: ref}
	}
	if path := checkedOutAt(branch); path != "" {
		ui.Warnf("Branch %s is checked out at %s; creating the worktree with a detached HEAD at %s", branch, path, ref)
		return inferredAdd{start: ref, detached: true}
	}
	return inferredAdd{branch: branch, requested: ref}
}

func checkedOutAt(branch string) string {
	entries, err := worktree.List()
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Branch == branch && !e.Detached {
			return e.Path
		}
	}
	return ""
}

// inferBranchFromPath names the branch after the worktree path relative to the
// bare root, so hotfix/x checks out hotfix/x rather than x.
func inferBranchFromPath(root, path string) (string, bool) {
	full := addPath(root, path)
	name, ok := pathFromRoot(root, full)
	if !ok {
		// root has symlinks resolved; the path may reach it through one.
		name, ok = pathFromRoot(root, resolveExistingPrefix(full))
	}
	if !ok || !validBranchName(name) {
		return "", false
	}
	return name, true
}

// resolveExistingPrefix resolves symlinks in the deepest existing ancestor of
// path, which need not exist yet.
func resolveExistingPrefix(path string) string {
	for dir := path; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			rest, err := filepath.Rel(dir, path)
			if err != nil {
				return path
			}
			return filepath.Join(resolved, rest)
		}
		if filepath.Dir(dir) == dir {
			return path
		}
	}
}

func validBranchName(name string) bool {
	if name == "" || name == "HEAD" || strings.HasPrefix(name, "-") {
		return false
	}
	out, err := git.Query("check-ref-format", "--branch", name)
	return err == nil && out == name
}

// remoteBranchRef reports whether commitish names a remote-tracking branch,
// returning it as <remote>/<branch> along with the branch name.
func remoteBranchRef(root, commitish string) (ref, branch string, ok bool) {
	// rev-parse would read a leading dash as an option, and echoes
	// --end-of-options back, so reject such input here instead.
	if strings.HasPrefix(commitish, "-") {
		return "", "", false
	}
	full, err := git.QueryIn(root, "rev-parse", "--symbolic-full-name", commitish)
	if err != nil {
		return "", "", false
	}
	short, found := strings.CutPrefix(full, "refs/remotes/")
	if !found {
		return "", "", false
	}
	remote, branch, ok := splitRemotePrefix(remoteNames(root), short)
	if !ok || !validBranchName(branch) {
		return "", "", false
	}
	return remote + "/" + branch, branch, true
}

func remoteNames(root string) []string {
	out, err := git.QueryIn(root, "remote")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// splitRemotePrefix splits name into a configured remote and the rest.
// Remote names may contain slashes, so the longest match wins.
func splitRemotePrefix(remotes []string, name string) (remote, rest string, ok bool) {
	for _, r := range remotes {
		r = strings.TrimSpace(r)
		if r != "" && len(r) > len(remote) && strings.HasPrefix(name, r+"/") && len(name) > len(r)+1 {
			remote = r
		}
	}
	if remote == "" {
		return "", "", false
	}
	return remote, strings.TrimPrefix(name, remote+"/"), true
}

func localBranchExists(root, branch string) bool {
	return refExists(root, "refs/heads/"+branch)
}

func refExists(root, ref string) bool {
	_, err := git.QueryIn(root, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

func runAddDirect(cmd *cobra.Command, args []string, remote, root string) (string, error) {
	args = append([]string(nil), args...)
	var plan inferredAdd
	inferred := false
	if !cmd.Flags().Changed("branch") && !cmd.Flags().Changed("force-branch") && !boolFlag(cmd, "detach") && !boolFlag(cmd, "orphan") {
		var err error
		plan, inferred, err = planInferredAdd(root, remote, args)
		if err != nil {
			return "", err
		}
	}
	if len(args) > 0 {
		args[0] = addPath(root, args[0])
	}
	var gitArgs []string

	branch, _ := cmd.Flags().GetString("branch")
	if branch != "" {
		gitArgs = append(gitArgs, "-b", branch)
	}
	forceBranch, _ := cmd.Flags().GetString("force-branch")
	if forceBranch != "" {
		gitArgs = append(gitArgs, "-B", forceBranch)
	}
	if reason, _ := cmd.Flags().GetString("reason"); reason != "" {
		gitArgs = append(gitArgs, "--reason", reason)
	}

	for _, name := range []string{"detach", "force", "lock", "quiet", "no-checkout", "no-track", "guess-remote", "orphan"} {
		if v, _ := cmd.Flags().GetBool(name); v {
			gitArgs = append(gitArgs, "--"+name)
		}
	}

	createdPath := ""
	if len(args) > 0 {
		createdPath = args[0]
	}

	trackBranch := branch
	if trackBranch == "" {
		trackBranch = forceBranch
	}
	// A branch new from HEAD gets the same upstream handling as -b/-B.
	defaultUpstream := trackBranch != ""

	// Append positional args (path, commit-ish).
	if inferred {
		gitArgs = append(gitArgs, plan.gitArgs(createdPath)...)
		trackBranch = plan.branch
		defaultUpstream = plan.create && plan.start == ""
	} else {
		gitArgs = append(gitArgs, "--")
		gitArgs = append(gitArgs, args...)
	}

	fullArgs := append([]string{"worktree", "add"}, gitArgs...)
	err := runAddLifecycle(root, createdPath, trackBranch, func() error {
		// Create the worktree.
		if err := ui.SpinWithOutputContext("Creating worktree", func(ctx context.Context, w io.Writer) error {
			return git.RunInToContext(ctx, root, w, fullArgs...)
		}); err != nil {
			return err
		}

		if plan.requested != "" {
			warnReusedLocalBranch(root, plan.branch, plan.requested)
		}
		if plan.upstream != "" && !boolFlag(cmd, "no-track") {
			return ui.SpinWithOutputContext(fmt.Sprintf("Setting upstream to %s", ui.Accent(plan.upstream)), func(ctx context.Context, w io.Writer) error {
				return git.RunInToContext(ctx, root, w, "branch", "--set-upstream-to="+plan.upstream, plan.branch)
			})
		}

		// Set upstream tracking for a new branch.
		if defaultUpstream && remote != "" && !boolFlag(cmd, "no-track") {
			if _, err := git.QueryIn(root, "rev-parse", "--verify", "refs/remotes/"+remote+"/"+trackBranch); err == nil {
				if err := ui.SpinWithOutputContext(fmt.Sprintf("Setting upstream to %s", ui.Accent(remote+"/"+trackBranch)), func(ctx context.Context, w io.Writer) error {
					return git.RunInToContext(ctx, root, w, "branch", "--set-upstream-to="+remote+"/"+trackBranch, trackBranch)
				}); err != nil {
					return err
				}
			} else {
				fmt.Fprintln(os.Stderr)
				fmt.Fprintln(os.Stderr, renderCommandHintsSectionFor(os.Stderr, []commandHint{{
					Action:  fmt.Sprintf("Push %s and set upstream", ui.Accent(trackBranch)),
					Command: "git push -u " + remote + " " + trackBranch,
				}}))
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return createdPath, nil
}

func splitRemoteBranchRef(remoteRef string) (remote string, branch string) {
	remote, branch, _ = strings.Cut(remoteRef, "/")
	return remote, branch
}

func runAddLifecycle(bareRoot, wtPath, branch string, create func() error) error {
	if wtPath == "" {
		return create()
	}

	beforeHooks, err := hook.Load(hook.BeforeAdd)
	if err != nil {
		return err
	}
	afterHooks, err := hook.Load(hook.AfterAdd)
	if err != nil {
		return err
	}

	absPath, err := filepath.Abs(wtPath)
	if err != nil {
		return err
	}
	invocation := hook.Invocation{
		Event:        hook.BeforeAdd,
		Dir:          bareRoot,
		WorktreePath: absPath,
		Branch:       branch,
		BareRoot:     bareRoot,
	}
	if err := hook.Run(context.Background(), beforeHooks, invocation, os.Stderr); err != nil {
		return err
	}
	if err := create(); err != nil {
		return err
	}

	invocation.Event = hook.AfterAdd
	invocation.Dir = absPath
	if err := hook.Run(context.Background(), afterHooks, invocation, os.Stderr); err != nil {
		if resolvedPath, pathErr := resolveCreatedPath(wtPath); pathErr == nil {
			fmt.Fprintf(os.Stderr, "Worktree was created at %s, but wt.afteradd failed\n", resolvedPath)
		}
		return err
	}
	return nil
}

func resolveCreatedPath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolvedPath, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = resolvedPath
	}
	return absPath, nil
}

func printCreatedWorktreePath(path string) error {
	if path == "" {
		return nil
	}
	absPath, err := resolveCreatedPath(path)
	if err != nil {
		return err
	}
	fmt.Println(absPath)
	return nil
}
