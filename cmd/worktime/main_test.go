package main

import (
	"errors"
	"net"
	"testing"
)

func TestOccupiedPortTriggersBootstrapRollbackBeforeCommit(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupied.Close()
	rollbackMarker := errors.New("rollback invoked")
	rolledBack := false
	listener, err := acquireStartupListener(occupied.Addr().String(), true, func() error {
		rolledBack = true
		return rollbackMarker
	})
	if listener != nil {
		listener.Close()
		t.Fatal("occupied address returned a listener")
	}
	if !rolledBack || !errors.Is(err, rollbackMarker) {
		t.Fatalf("bind failure did not invoke rollback: rolledBack=%v err=%v", rolledBack, err)
	}
}

func TestOrdinaryOccupiedPortDoesNotInvokeUpdateRollback(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupied.Close()
	rolledBack := false
	listener, err := acquireStartupListener(occupied.Addr().String(), false, func() error {
		rolledBack = true
		return nil
	})
	if listener != nil {
		listener.Close()
	}
	if err == nil || rolledBack {
		t.Fatalf("ordinary bind failure rollback=%v err=%v", rolledBack, err)
	}
}
