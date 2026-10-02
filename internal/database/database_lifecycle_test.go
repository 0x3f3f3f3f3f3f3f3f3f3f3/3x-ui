package database

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestDatabasePublicationDuringReplacement(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	if err := InitDB(filepath.Join(dir, "original.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	original := GetDB()
	started, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer func() { once.Do(func() { close(stop) }); <-done }()
	partial := make(chan error, 1)
	go func() {
		defer close(done)
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
			}
			current := GetDB()
			_ = Dialect()
			_ = IsPostgres()
			if current != nil && current != original && !current.Migrator().HasTable(&model.ClientPolicySource{}) {
				select {
				case partial <- fmt.Errorf("published replacement pool before its accounting schema was ready"):
				default:
				}
				return
			}
		}
	}()
	<-started
	if err := InitDB(filepath.Join(dir, "replacement.db")); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(stop) })
	<-done
	select {
	case err := <-partial:
		t.Fatal(err)
	default:
	}
	if GetDB() == original || !GetDB().Migrator().HasTable(&model.ClientPolicySource{}) {
		t.Fatal("completed replacement did not publish the new accounting schema")
	}
}
