package harnessmin_test

// The one piece of ptah that was actually running when the fault reproduced.
//
// The goroutine dump from the reproduction names exactly this path and nothing
// else doing work:
//
//	TestNewFSMigrationProvider_AtlasRepeatableFilesUseFilenameOrder
//	  -> NewFSMigrationProvider -> load -> loadAtlas -> loadAtlasFile
//	  -> loadAtlasUp -> atlasSQLMigrationFileFromSQLContentWithMetadata
//	  -> migrationFuncFromSQLStringWithMetadata
//
// No SQLite, no cgo, no child process: an in-memory filesystem, text scanned
// for directives, and per file a struct holding a few strings and a closure
// that captures them. That is a dense stream of small pointer-bearing
// allocations -- funcvals among them -- which is what this arm was missing, and
// it needs nothing outside the standard library.

import (
	"fmt"
	"io/fs"
	"strings"
	"testing/fstest"
)

type migrationFile struct {
	path     string
	sql      string
	timeouts []string
	txMode   string
	// A closure capturing the fields above, the way the real loader stores one
	// per file. Each is a heap-allocated funcval that points at its captured
	// variables.
	run func() string
}

// buildFS makes an in-memory tree of migration-shaped files.
func buildFS(files int) fstest.MapFS {
	m := make(fstest.MapFS, files)
	for i := range files {
		var b strings.Builder
		fmt.Fprintf(&b, "-- atlas:txmode none\n-- ptah:timeout statement=%ds\n", 1+i%9)
		for s := range 12 {
			fmt.Fprintf(&b, "CREATE TABLE t_%d_%d (id INTEGER PRIMARY KEY, name TEXT, body TEXT);\n", i, s)
			fmt.Fprintf(&b, "CREATE INDEX idx_%d_%d ON t_%d_%d(name);\n", i, s, i, s)
		}
		dir := "up"
		if i%2 == 1 {
			dir = "down"
		}
		m[fmt.Sprintf("%06d_create_users.%s.sql", i, dir)] = &fstest.MapFile{Data: []byte(b.String())}
	}
	return m
}

// loadAll walks the filesystem and builds one migrationFile per entry, the way
// the loader in the reproduction does.
func loadAll(fsys fs.FS) ([]migrationFile, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	out := make([]migrationFile, 0, len(entries))
	for _, e := range entries {
		raw, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		sql := string(raw)

		var timeouts []string
		txMode := "unspecified"
		for line := range strings.SplitSeq(sql, "\n") {
			switch {
			case strings.HasPrefix(line, "-- atlas:txmode "):
				txMode = strings.TrimSpace(strings.TrimPrefix(line, "-- atlas:txmode "))
			case strings.HasPrefix(line, "-- ptah:timeout "):
				timeouts = append(timeouts, strings.TrimSpace(strings.TrimPrefix(line, "-- ptah:timeout ")))
			}
		}

		name := e.Name()
		mf := migrationFile{path: name, sql: sql, timeouts: timeouts, txMode: txMode}
		mf.run = func() string { return name + ":" + txMode + ":" + fmt.Sprint(len(sql)) }
		out = append(out, mf)
	}
	return out, nil
}
