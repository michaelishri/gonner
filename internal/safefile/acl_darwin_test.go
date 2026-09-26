package safefile

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDarwinACLMutationGrantsAreRejected(t *testing.T) {
	path := secureDir(t)
	file := filepath.Join(path, "log")
	if err := os.WriteFile(file, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("chmod", "+a", "everyone allow write,append,delete", file).CombinedOutput(); err != nil {
		t.Fatalf("setting ACL: %v %s", err, out)
	}
	dir, err := OpenDirectory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if f, err := dir.OpenRegular("log", os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		_ = f.Close()
		t.Fatal("ACL bypassed mode checks")
	}
	b, _ := os.ReadFile(file)
	if string(b) != "unchanged" {
		t.Fatal("file modified")
	}
	if out, err := exec.Command("chmod", "+a", "everyone allow add_file,delete_child", path).CombinedOutput(); err != nil {
		t.Fatalf("setting directory ACL: %v %s", err, out)
	}
	if d, err := OpenDirectory(path); err == nil {
		_ = d.Close()
		t.Fatal("directory ACL mutation accepted")
	}
}
