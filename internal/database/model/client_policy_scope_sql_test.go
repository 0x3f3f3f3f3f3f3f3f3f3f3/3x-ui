package model

import (
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestClientPolicyScopeSQLPreservesAbsence(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "scope.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := db.AutoMigrate(&ClientRecord{}); err != nil {
		t.Fatal(err)
	}
	global := ClientPolicyScopeGlobal
	for i, policy := range []*ClientPolicyOptions{nil, {Multiplier: "1"}, {Scope: &global, Multiplier: "2"}} {
		row := ClientRecord{Email: string(rune('a' + i)), Policy: policy}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		var found ClientRecord
		if err := db.First(&found, row.Id).Error; err != nil {
			t.Fatal(err)
		}
		if policy == nil && found.Policy != nil {
			t.Fatal("all-null SQL policy became an explicit policy")
		}
		if policy != nil && (found.Policy == nil || found.Policy.Multiplier != policy.Multiplier || found.Policy.EffectiveScope() != policy.EffectiveScope()) {
			t.Fatalf("SQL lost explicit policy/scope: %+v", found.Policy)
		}
		if err := SaveClientRecord(db, &found); err != nil {
			t.Fatal(err)
		}
		var saved ClientRecord
		if err := db.First(&saved, row.Id).Error; err != nil {
			t.Fatal(err)
		}
		if policy == nil && saved.Policy != nil {
			t.Fatal("ordinary save activated absent policy")
		}
		if policy != nil && saved.Policy.EffectiveScope() != policy.EffectiveScope() {
			t.Fatal("ordinary save erased explicit scope")
		}
	}
}
