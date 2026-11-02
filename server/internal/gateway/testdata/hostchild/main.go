package main

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	action := os.Args[1]
	if action == "list" {
		os.Exit(3)
	}
	request := domain.HostAdminRequest{Version: 1, Action: action}
	if action == "revoke" {
		request.Action = "journal-init"
	}
	session, err := gateway.BeginHostAdminChild(context.Background(), request, os.NewFile(4, "execution"))
	if err != nil {
		os.Exit(4)
	}
	defer func() { _ = session.Close() }()
	if action == "journal-init" {
		if session.CompleteWithCode(context.Background(), "conflict") != nil {
			os.Exit(7)
		}
		os.Exit(1)
	}
	if action == "policy" {
		os.Exit(5)
	}
	if action == "enroll" {
		f := os.NewFile(3, "credential")
		_, _ = f.Write([]byte("child-bearer-never-stdout"))
		_ = f.Close()
	}
	_, _ = os.Stdout.Write([]byte("{\"ok\":true}\n"))
	if session.Complete(context.Background()) != nil {
		os.Exit(6)
	}
}
