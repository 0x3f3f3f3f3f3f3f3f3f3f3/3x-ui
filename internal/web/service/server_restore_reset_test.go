package service

import (
	"context"
	"errors"
	"testing"
)

func TestDatabaseRestoreRejectsResetBeforeRuntimeInspection(t *testing.T) {
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := ResetLocalClientPolicy(context.Background(), "client-before-restore", "request-during-restore"); !errors.Is(err, ErrDatabaseRestoreInProgress) {
		t.Fatalf("reset entered runtime inspection during restore: %v", err)
	}
	if err := reconcileLocalClientPolicies(context.Background(), nil); !errors.Is(err, ErrDatabaseRestoreInProgress) {
		t.Fatalf("background reconciliation entered runtime inspection during restore: %v", err)
	}
}
