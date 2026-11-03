package cron

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupFileName(t *testing.T) {
	b := &Backup{Dir: "/backups"}
	ts := time.Date(2026, 8, 7, 12, 34, 56, 0, time.UTC)
	if got := b.fileName(ts); got != "/backups/backup_20260807T123456Z.sql" {
		t.Errorf("unexpected name: %s", got)
	}
}

func TestBackupRunUsesInjectedDump(t *testing.T) {
	dir := t.TempDir()
	var dumped string
	b := &Backup{
		DSN:  "postgres://user:pass@host/db",
		Dir:  dir,
		Keep: 2,
		Now:  func() time.Time { return time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC) },
		Dump: func(_ context.Context, dsn, file string) error {
			dumped = file
			if dsn != "postgres://user:pass@host/db" {
				t.Errorf("unexpected dsn: %s", dsn)
			}
			return os.WriteFile(file, []byte("dump"), 0o644)
		},
	}
	path, err := b.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if path == dumped {
		t.Error("dump must be published by atomic rename")
	}
	if got := b.LastSuccess(); !got.Equal(b.Now()) {
		t.Fatalf("persistent backup success marker = %v", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("backup file missing: %v", err)
	}
}

func TestBackupDumpErrorPropagated(t *testing.T) {
	b := &Backup{Dir: t.TempDir(), Dump: func(context.Context, string, string) error { return errBoom{} }}
	_, err := b.Run(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBackupPruneKeepsLatest(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"backup_20260801T000000Z.sql",
		"backup_20260802T000000Z.sql",
		"backup_20260803T000000Z.sql",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	b := &Backup{Dir: dir, Keep: 2}
	if err := b.prune(); err != nil {
		t.Fatalf("prune: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Name() == "backup_20260801T000000Z.sql" {
			t.Error("oldest backup should have been pruned")
		}
	}
}

func TestBackupPruneRespectsKeepZero(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		f := filepath.Join(dir, "backup_2026080"+string(rune('0'+i))+"T000000Z.sql")
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	b := &Backup{Dir: dir, Keep: 0}
	if err := b.prune(); err != nil {
		t.Fatalf("prune: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 5 {
		t.Errorf("keep=0 must retain all, got %d", len(entries))
	}
}

func TestFailedBackupDoesNotPublishOrPrune(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "backup_20260801T000000Z.sql")
	if err := os.WriteFile(old, []byte("good"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &Backup{Dir: dir, Keep: 1, Dump: func(_ context.Context, _ string, path string) error {
		if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
			return err
		}
		return errBoom{}
	}}
	if _, err := b.Run(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != filepath.Base(old) {
		t.Fatalf("failed backup affected published files: %v", entries)
	}
}
func TestBackupIsPrivateAndUploadFailurePreservesLocalCopy(t *testing.T) {
	dir := t.TempDir()
	b := &Backup{Dir: dir, Keep: 1, Dump: func(_ context.Context, _ string, path string) error { return os.WriteFile(path, []byte("good"), 0o644) }, Upload: func(context.Context, string) error { return errBoom{} }}
	path, err := b.Run(context.Background())
	if err == nil {
		t.Fatal("expected upload failure")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode: %o", st.Mode().Perm())
	}
}
