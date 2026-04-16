// Package db menyediakan koneksi SQLite dan menjalankan migration otomatis.
package db

import (
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	_ "github.com/mattn/go-sqlite3" // driver SQLite via CGO
	"github.com/xenvoid404/userbot-promotion/migrations"
)

// Open membuka koneksi SQLite, mengonfigurasi connection pool, dan menjalankan
// semua migration yang ditemukan di embed.FS secara berurutan.
//
// DSN parameters:
//   - _foreign_keys=on     : enforce referential integrity (CASCADE, RESTRICT, dll)
//   - _journal_mode=WAL    : Write-Ahead Logging — concurrent readers + satu writer
//     tanpa blocking; performa tulis jauh lebih baik dari default DELETE journal
//   - _busy_timeout=5000   : tunggu hingga 5 detik sebelum kembalikan
//     "database is locked" saat writer lain sedang aktif
//   - _synchronous=NORMAL  : cukup aman untuk WAL; lebih cepat dari FULL
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL",
		path,
	)

	sqlDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// SQLite hanya support satu writer pada satu waktu.
	// MaxOpenConns(1): cegah "database is locked" akibat race antar goroutine writer.
	// MaxIdleConns(1): pertahankan satu koneksi di pool agar tidak ada overhead
	//                  open/close file setiap query.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	if err := runMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrations: %w", err)
	}

	return sqlDB, nil
}

// runMigrations membaca semua file *.sql dari embed.FS lalu mengeksekusinya
// secara berurutan (sorted by filename). Setiap file menggunakan
// CREATE TABLE IF NOT EXISTS sehingga aman dijalankan ulang.
func runMigrations(sqlDB *sql.DB) error {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil // tidak ada migration embedded — schema diasumsikan sudah ada
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		data, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := sqlDB.Exec(string(data)); err != nil {
			return fmt.Errorf("exec migration %s: %w", name, err)
		}
	}

	return nil
}
