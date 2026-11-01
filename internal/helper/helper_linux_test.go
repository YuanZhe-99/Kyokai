package helper

import "testing"

// TestGroupGID: the kyokai group is read from /etc/group's text, by its
// exact name.
func TestGroupGID(t *testing.T) {
	const group = "root:x:0:\nkyokai-old:x:998:\nkyokai:x:999:alice,bob\n"
	if id, ok := groupGID(group, "kyokai"); !ok || id != 999 {
		t.Errorf("kyokai: %d, %v", id, ok)
	}
	if _, ok := groupGID(group, "kyok"); ok {
		t.Error("a prefix matched")
	}
	if _, ok := groupGID("kyokai:x:bad:\n", "kyokai"); ok {
		t.Error("a malformed id was taken")
	}
}
