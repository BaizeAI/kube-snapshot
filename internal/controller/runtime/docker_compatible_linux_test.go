package runtime

import (
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDockerConfigWriteFailure(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("DOCKER_CONFIG", "original-config")
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	// A file size limit makes config.json fail to write even when tests run as
	// root. Restore the process limit before assertions or any other file I/O.
	wasIgnored := signal.Ignored(syscall.SIGXFSZ)
	signal.Ignore(syscall.SIGXFSZ)
	if !wasIgnored {
		defer signal.Reset(syscall.SIGXFSZ)
	}
	limited := original
	limited.Cur = 0
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatal(err)
	}
	var restoreErr error
	err := func() error {
		defer func() { restoreErr = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original) }()
		rt := NewDockerCompatibleRuntime("must-not-run")
		return rt.Push(t.Context(), "registry.test/snapshot:tag", &Auth{ConfigJSON: `{}`}, true)
	}()
	if restoreErr != nil {
		t.Fatal(restoreErr)
	}
	var pathErr *os.PathError
	if !errors.Is(err, syscall.EFBIG) || !errors.As(err, &pathErr) || filepath.Base(pathErr.Path) != "config.json" {
		t.Fatalf("expected config.json write error, got %v", err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary Docker config was not removed: %v", entries)
	}
	if got := os.Getenv("DOCKER_CONFIG"); got != "original-config" {
		t.Fatalf("parent Docker config changed to %q", got)
	}
}
