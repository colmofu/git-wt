package cmd

import (
	"slices"
	"testing"
)

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
		{"trailing slash", "hotfix/", "", false},
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
