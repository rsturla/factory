package blob_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hummingbird-org/vuln-ingest/internal/blob"
)

func newTestStore(t *testing.T) blob.Store {
	t.Helper()
	s, err := blob.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLocalStore_PutGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "nvd/CVE-2024-0001.json", []byte(`{"id":"CVE-2024-0001"}`)); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, "nvd/CVE-2024-0001.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"id":"CVE-2024-0001"}` {
		t.Fatalf("got %q", got)
	}
}

func TestLocalStore_GetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get(context.Background(), "does/not/exist.json")
	if !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestLocalStore_PutCreatesParentDirs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "a/b/c/deep.json", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "a/b/c/deep.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ok" {
		t.Fatalf("got %q", got)
	}
}

func TestLocalStore_PathTraversal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	cases := []string{
		"../../etc/passwd",
		"../escape.txt",
		"foo/../../etc/shadow",
		"foo/../../../bar",
	}

	for _, key := range cases {
		_, err := s.Get(ctx, key)
		if err == nil {
			t.Errorf("Get(%q): expected error", key)
		}

		err = s.Put(ctx, key, []byte("bad"))
		if err == nil {
			t.Errorf("Put(%q): expected error", key)
		}

		_, err = s.Exists(ctx, key)
		if err == nil {
			t.Errorf("Exists(%q): expected error", key)
		}
	}
}

func TestLocalStore_Exists(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	exists, err := s.Exists(ctx, "missing.json")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("expected false for missing key")
	}

	if err := s.Put(ctx, "present.json", []byte("hi")); err != nil {
		t.Fatal(err)
	}

	exists, err = s.Exists(ctx, "present.json")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected true for existing key")
	}
}

func TestLocalStore_Overwrite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "key.json", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "key.json", []byte("v2")); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, "key.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2" {
		t.Fatalf("expected v2, got %q", got)
	}
}

func TestLocalStore_EmptyData(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "empty.json", []byte{}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, "empty.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(got))
	}
}

func TestLocalStore_EmptyKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.Put(ctx, "", []byte("bad"))
	if err == nil {
		t.Fatal("expected error for empty key on Put")
	}

	_, err = s.Get(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty key on Get")
	}

	_, err = s.Exists(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty key on Exists")
	}
}

func TestLocalStore_BaseDirKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, key := range []string{"/", ".", "//", "/."} {
		err := s.Put(ctx, key, []byte("bad"))
		if err == nil {
			t.Errorf("Put(%q): expected error for key resolving to base dir", key)
		}

		_, err = s.Get(ctx, key)
		if err == nil {
			t.Errorf("Get(%q): expected error", key)
		}

		_, err = s.Exists(ctx, key)
		if err == nil {
			t.Errorf("Exists(%q): expected error", key)
		}
	}
}

func TestLocalStore_NullByteInKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.Put(ctx, "foo\x00bar", []byte("bad"))
	if err == nil {
		t.Fatal("expected error for null byte in key")
	}
}

func TestLocalStore_TopLevelKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "flat.json", []byte("data")); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, "flat.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data" {
		t.Fatalf("got %q", got)
	}
}
