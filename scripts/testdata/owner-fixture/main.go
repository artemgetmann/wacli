//go:build !windows

// This offline process fixture owns a real kernel lock and a ping-only socket.
// It never opens a WhatsApp database or a network connection.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	if len(os.Args) != 6 || os.Args[1] != "--store" || os.Args[3] != "sync" || os.Args[4] != "--follow" || os.Args[5] != "--events" {
		os.Exit(2)
	}
	dir := os.Args[2]
	f, err := os.OpenFile(filepath.Join(dir, "LOCK"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		os.Exit(3)
	}
	if err = f.Truncate(0); err != nil {
		panic(err)
	}
	fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	listener, err := net.Listen("unix", filepath.Join(dir, ".send.sock"))
	if err != nil {
		panic(err)
	}
	defer listener.Close()
	if err = os.Chmod(filepath.Join(dir, ".send.sock"), 0600); err != nil {
		panic(err)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() { <-signals; listener.Close() }()
	fmt.Println(`{"event":"connected"}`)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		var request struct {
			Kind string `json:"kind"`
		}
		if json.NewDecoder(conn).Decode(&request) == nil && request.Kind == "ping" {
			json.NewEncoder(conn).Encode(map[string]any{"ok": true, "action": "ping", "owner_pid": os.Getpid()})
		}
		conn.Close()
	}
}
