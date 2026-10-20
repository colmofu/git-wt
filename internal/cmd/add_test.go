package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestInferBranchFromPathThroughSymlink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}

	got, ok := inferBranchFromPath(root, filepath.Join(link, "hotfix", "x"))
	if got != "hotfix/x" || !ok {
		t.Fatalf("inferBranchFromPath(via symlink) = (%q, %v), want (%q, true)", got, ok, "hotfix/x")
	}
}

func TestSplitRemotePrefix(t *testing.T) {
	remotes := []string{"origin", "team", "team/up"}
	tests := []struct {
		name         string
		remote, rest string
		ok           bool
	}{
		{"origin/team/new", "origin", "team/new", true},
		{"team/up/feat", "team/up", "feat", true},
		{"team/feat", "team", "feat", true},
		{"hotfix/x", "", "", false},
		{"origin", "", "", false},
		{"origin/", "", "", false},
	}
	for _, tt := range tests {
		remote, rest, ok := splitRemotePrefix(remotes, tt.name)
		if remote != tt.remote || rest != tt.rest || ok != tt.ok {
			t.Errorf("splitRemotePrefix(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.name, remote, rest, ok, tt.remote, tt.rest, tt.ok)
		}
	}
}

func TestRemoteBranchRef(t *testing.T) {
	root := initGitRepo(t)
	gitIn(t, root, "remote", "add", "origin", "https://example.invalid/origin.git")
	gitIn(t, root, "remote", "add", "team/up", "https://example.invalid/up.git")
	gitIn(t, root, "update-ref", "refs/remotes/origin/hotfix/x", "HEAD")
	gitIn(t, root, "update-ref", "refs/remotes/team/up/feat", "HEAD")
	gitIn(t, root, "update-ref", "refs/remotes/stray/feat", "HEAD")

	tests := []struct {
		commitish   string
		ref, branch string
		ok          bool
	}{
		{"origin/hotfix/x", "origin/hotfix/x", "hotfix/x", true},
		{"refs/remotes/origin/hotfix/x", "origin/hotfix/x", "hotfix/x", true},
		{"team/up/feat", "team/up/feat", "feat", true},
		{"main", "", "", false},
		{"HEAD", "", "", false},
		{"origin/missing", "", "", false},
		{"stray/feat", "", "", false},
		{"--all", "", "", false},
	}
	for _, tt := range tests {
		ref, branch, ok := remoteBranchRef(root, tt.commitish)
		if ref != tt.ref || branch != tt.branch || ok != tt.ok {
			t.Errorf("remoteBranchRef(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.commitish, ref, branch, ok, tt.ref, tt.branch, tt.ok)
		}
	}
}

func TestRemoveEmptyParents(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	removeEmptyParents(root, filepath.Join(mk("a/b"), "wt"), root)
	if _, err := os.Stat(filepath.Join(root, "a")); !os.IsNotExist(err) {
		t.Errorf("empty parents a/b should be removed, stat err = %v", err)
	}

	mk("c/keep")
	removeEmptyParents(root, filepath.Join(mk("c/d"), "wt"), root)
	if _, err := os.Stat(filepath.Join(root, "c/d")); !os.IsNotExist(err) {
		t.Errorf("empty c/d should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "c/keep")); err != nil {
		t.Errorf("c with another child should be kept: %v", err)
	}

	cwd := mk("e/f")
	removeEmptyParents(root, filepath.Join(cwd, "wt"), cwd)
	if _, err := os.Stat(cwd); err != nil {
		t.Errorf("cwd must be kept: %v", err)
	}

	if _, err := os.Stat(root); err != nil {
		t.Errorf("root must be kept: %v", err)
	}
}

func TestInferBranchFromPath(t *testing.T) {
	root := "/repo"
	tests := []struct {
		name   string
		path   string
		want   string
		wantOK bool
	}{
		{"slash branch", "hotfix/x", "hotfix/x", true},
		{"nested slash branch", "a/b/c", "a/b/c", true},
		{"flat branch", "feature", "feature", true},
		{"absolute path inside root", "/repo/hotfix/x", "hotfix/x", true},
		{"parent escape", "../x", "", false},
		{"absolute path outside root", "/elsewhere/x", "", false},
		{"root itself", "/repo", "", false},
		{"double dot ref", "foo..bar", "", false},
		{"lock suffix", "x.lock", "", false},
		{"trailing slash", "hotfix/x/", "hotfix/x", true},
		{"repeated trailing slashes", "hotfix//", "hotfix", true},
		{"leading dash", "-x", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := inferBranchFromPath(root, tt.path)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("inferBranchFromPath(%q, %q) = (%q, %v), want (%q, %v)", root, tt.path, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestPathFromRoot(t *testing.T) {
	tests := []struct {
		root, path string
		want       string
		wantOK     bool
	}{
		{"/repo", "/repo/hotfix/x", "hotfix/x", true},
		{"/repo", "/repo/hotfix", "hotfix", true},
		{"/repo", "/repo", "", false},
		{"/repo", "/", "", false},
		{"/repo", "/repo-other/x", "", false},
		{"", "/repo/x", "", false},
	}
	for _, tt := range tests {
		got, ok := pathFromRoot(tt.root, tt.path)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("pathFromRoot(%q, %q) = (%q, %v), want (%q, %v)", tt.root, tt.path, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestInferredAddGitArgs(t *testing.T) {
	tests := []struct {
		name string
		plan inferredAdd
		want []string
	}{
		{"existing branch", inferredAdd{branch: "hotfix/x"}, []string{"--", "/repo/hotfix/x", "hotfix/x"}},
		{"new from HEAD", inferredAdd{branch: "hotfix/x", create: true}, []string{"-b", "hotfix/x", "--", "/repo/hotfix/x"}},
		{"new from remote", inferredAdd{branch: "hotfix/x", create: true, start: "origin/hotfix/x"}, []string{"-b", "hotfix/x", "--", "/repo/hotfix/x", "origin/hotfix/x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.plan.gitArgs("/repo/hotfix/x")
			if !slices.Equal(got, tt.want) {
				t.Fatalf("gitArgs() = %q, want %q", got, tt.want)
			}
		})
	}
}
