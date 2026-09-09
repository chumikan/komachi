package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestDatabaseCommandValidation(t *testing.T) {
	t.Setenv("LEAFWIKI_DATABASE_URL", "")
	for _, args := range [][]string{
		{"database"},
		{"database", "migrate"},
		{"database", "status"},
		{"database", "migrate", "unexpected"},
		{"database", "--database-url", "postgres://user:secret-password@host:invalid/db", "status"},
		{"database", "--database-url", "postgres://localhost/wiki", "--max-conns", "0", "status"},
		{"database", "--timeout", "0", "status"},
	} {
		cmd := newRootCommand()
		var output bytes.Buffer
		cmd.Writer, cmd.ErrWriter = &output, &output
		err := cmd.Run(context.Background(), append([]string{"leafwiki"}, args...))
		if err == nil {
			t.Fatalf("accepted invalid database command: %v", args)
		}
		if strings.Contains(err.Error()+output.String(), "secret-password") {
			t.Fatal("database command disclosed credentials")
		}
	}
}

func TestDatabaseCommandHelp(t *testing.T) {
	cmd := newRootCommand()
	var output bytes.Buffer
	cmd.Writer, cmd.ErrWriter = &output, &output
	if err := cmd.Run(context.Background(), []string{"leafwiki", "database", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"migrate", "status", "LEAFWIKI_DATABASE_URL", "--max-conns", "--connect-timeout", "--timeout"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing database help: %s", want)
		}
	}
}
