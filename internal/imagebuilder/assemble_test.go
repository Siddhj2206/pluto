package imagebuilder_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Siddhj2206/pluto/internal/imagebuilder"
)

func TestAssemble(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "rootfs.tar")
	write(t, tarPath, "tar-bytes", 0o644)
	agent := filepath.Join(dir, "pluto-agent")
	write(t, agent, "agent-bytes", 0o755)
	image := filepath.Join(dir, "rootfs.img")
	rootfs := filepath.Join(dir, "rootfs")

	sh := newFakeShell()
	op := imagebuilder.AssembleOptions{
		Tar:    tarPath,
		Dir:    dir,
		Agent:  agent,
		Image:  image,
		DiskMB: 2048,
		UUID:   "11111111-2222-3333-4444-555555555555",
		Label:  "pluto-root",
	}
	if err := imagebuilder.Assemble(context.Background(), sh, op); err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	info, err := os.Stat(image)
	if err != nil {
		t.Fatalf("rootfs.img not created: %v", err)
	}
	if got, want := info.Size(), int64(2048)<<20; got != want {
		t.Errorf("rootfs.img size = %d, want %d", got, want)
	}

	want := []struct {
		args []string
	}{
		{[]string{"unshare", "rm", "-rf", rootfs}},
		{[]string{"unshare", "mkdir", "-p", rootfs}},
		{[]string{"unshare", "tar", "-x", "--numeric-owner", "-f", tarPath, "-C", rootfs}},
		{[]string{"unshare", "install", "-m", "0755", "-o", "root", "-g", "root", agent, filepath.Join(rootfs, "usr", "local", "bin", "pluto-agent")}},
		{[]string{"unshare", "mkfs.ext4", "-q", "-F", "-L", "pluto-root", "-U", "11111111-2222-3333-4444-555555555555", "-d", rootfs, image}},
		{[]string{"unshare", "rm", "-rf", rootfs}},
	}
	cmds := sh.all()
	if len(cmds) != len(want) {
		t.Fatalf("ran %d commands, want %d:\n%v", len(cmds), len(want), cmds)
	}
	for i, w := range want {
		if cmds[i].name != "podman" || !reflect.DeepEqual(cmds[i].args, w.args) {
			t.Errorf("command %d = %s %q, want podman %q", i, cmds[i].name, cmds[i].args, w.args)
		}
	}
}

func TestAssembleDefaultsIdentity(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "rootfs.tar"), "tar", 0o644)
	write(t, filepath.Join(dir, "pluto-agent"), "agent", 0o755)
	sh := newFakeShell()
	op := imagebuilder.AssembleOptions{
		Tar:    filepath.Join(dir, "rootfs.tar"),
		Dir:    dir,
		Agent:  filepath.Join(dir, "pluto-agent"),
		Image:  filepath.Join(dir, "rootfs.img"),
		DiskMB: 1,
	}
	if err := imagebuilder.Assemble(context.Background(), sh, op); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	cmds := sh.all()
	mkfs := cmds[len(cmds)-2]
	if got := mkfs.args[5]; got != imagebuilder.DefaultLabel {
		t.Errorf("label = %q, want default %q", got, imagebuilder.DefaultLabel)
	}
	if got := mkfs.args[7]; got != imagebuilder.DefaultUUID {
		t.Errorf("uuid = %q, want default %q", got, imagebuilder.DefaultUUID)
	}
}

func TestAssembleRejectsNonPositiveDisk(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "rootfs.tar"), "tar", 0o644)
	write(t, filepath.Join(dir, "pluto-agent"), "agent", 0o755)
	err := imagebuilder.Assemble(context.Background(), newFakeShell(), imagebuilder.AssembleOptions{
		Tar:   filepath.Join(dir, "rootfs.tar"),
		Dir:   dir,
		Agent: filepath.Join(dir, "pluto-agent"),
		Image: filepath.Join(dir, "rootfs.img"),
	})
	if err == nil {
		t.Fatal("Assemble succeeded with disk size 0, want an error")
	}
}
