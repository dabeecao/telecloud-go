package webdav

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"telecloud/config"
	"telecloud/database"
)

// Paths stored in the database are POSIX (`/`), whoever wrote them — a Linux
// master replicates rows whose path column is `/Photos`. WebDAV must query with
// the same separator on every OS. filepath.Clean would turn it into `\` on
// Windows and the listing would come back empty.
func TestPathsStayPOSIX(t *testing.T) {
	if got := cleanPath(`\Photos\trip`); got != "/Photos/trip" {
		t.Fatalf("cleanPath: got %q", got)
	}
	if got := cleanPath(""); got != "/" {
		t.Fatalf("cleanPath empty: got %q", got)
	}

	dir, base := splitPath(`/Photos/trip`)
	if dir != "/Photos" || base != "trip" {
		t.Fatalf("splitPath: got %q %q", dir, base)
	}
	if dir, base := splitPath("/"); dir != "/" || base != "" {
		t.Fatalf("splitPath root: got %q %q", dir, base)
	}
	// A client that already sent backslashes (Windows UNC-style) must still
	// land on the slash form the database uses.
	if dir, base := splitPath(`\Photos\trip`); dir != "/Photos" || base != "trip" {
		t.Fatalf("splitPath backslash: got %q %q", dir, base)
	}

	if got := mapPath(`/Photos`, "alice", true); got != "/Photos" {
		t.Fatalf("mapPath admin: got %q", got)
	}
	if got := mapPath(`/Photos`, "alice", false); got != "/alice/Photos" {
		t.Fatalf("mapPath child: got %q", got)
	}
	if got := mapPath(`/`, "alice", false); got != "/alice" {
		t.Fatalf("mapPath child root: got %q", got)
	}
}

// Listing the root must find a folder stored with a POSIX path, which is what
// PROPFIND does first. This is the query that returned nothing on Windows.
func TestRootListingUsesPOSIXPath(t *testing.T) {
	if err := database.InitDB("sqlite", filepath.Join(t.TempDir(), "t.db"), ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.RWDB.Close()
		database.RODB.Close()
	})

	if _, err := database.DB.Exec("INSERT INTO files (filename, path, is_folder, owner) VALUES (?, ?, TRUE, ?)", "Photos", "/", "alice"); err != nil {
		t.Fatal(err)
	}

	fs := NewTelecloudFS(&config.Config{}).(*telecloudFS)
	ctx := context.WithValue(context.Background(), usernameKey, "alice")
	ctx = context.WithValue(ctx, isAdminKey, true)

	f, err := fs.OpenFile(ctx, "/", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	infos, err := f.Readdir(-1)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name() != "Photos" || !infos[0].IsDir() {
		t.Fatalf("root listing: got %+v", names(infos))
	}

	st, err := fs.Stat(ctx, "/Photos")
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsDir() || st.Name() != "Photos" {
		t.Fatalf("stat: %+v", st)
	}
}

func names(infos []os.FileInfo) []string {
	out := make([]string, len(infos))
	for i, fi := range infos {
		out[i] = fi.Name()
	}
	return out
}
