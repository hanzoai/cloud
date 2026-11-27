package main

import (
	"net"
	"testing"
	"time"
)

// A deployment that runs its object store as a separate service names it in
// S3_ADMIN_ENDPOINT, and that address belongs to another host. The plugin must
// use the store answering there rather than die trying to bind it.
func TestAStoreAlreadyAnsweringIsUsed(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	t.Setenv("S3_ADMIN_ENDPOINT", l.Addr().String())
	t.Setenv("S3_ADMIN_ACCESS_KEY", "ak")
	t.Setenv("S3_ADMIN_SECRET_KEY", "sk")

	done := make(chan error, 1)
	go func() { done <- startStore(t.TempDir()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a store answering at the endpoint was refused: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("startStore tried to start a second store over one that answers")
	}
}
