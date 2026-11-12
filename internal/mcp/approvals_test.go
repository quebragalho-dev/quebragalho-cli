package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

// Deleting the approvals file revokes every approval, even while a session
// holds the store open and approves another server: whatever is on disk wins.
func TestDeletedApprovalStaysDeleted(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	first := ProjectApproval{ProjectRoot: "/work/repo", ServerName: "a", Identity: "identity-a"}
	second := ProjectApproval{ProjectRoot: "/work/repo", ServerName: "b", Identity: "identity-b"}
	session, err := OpenProjectApprovals()
	if err != nil {
		t.Fatalf("OpenProjectApprovals() error = %v", err)
	}
	if err := session.Approve(first); err != nil {
		t.Fatalf("Approve(a) error = %v", err)
	}
	path, err := projectApprovalsPath()
	if err != nil {
		t.Fatalf("projectApprovalsPath() error = %v", err)
	}
	if err := os.Remove(filepath.Clean(path)); err != nil {
		t.Fatalf("delete the approvals file: %v", err)
	}
	if err := session.Approve(second); err != nil {
		t.Fatalf("Approve(b) error = %v", err)
	}

	fresh, err := OpenProjectApprovals()
	if err != nil {
		t.Fatalf("OpenProjectApprovals() error = %v", err)
	}
	if fresh.IsApproved(first) {
		t.Fatal("the approval deleted from disk came back when the open session approved another server")
	}
	if !fresh.IsApproved(second) {
		t.Fatal("the approval given after the deletion was lost")
	}
}
