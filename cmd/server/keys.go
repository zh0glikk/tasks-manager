package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/zh0glikk/tasks-manager/internal/store"
)

const keysUsage = `usage:
  app keys create NAME [--read]   issue a key not tied to Telegram (shown once)
  app keys list                   active keys
  app keys revoke ID              revoke a key`

// keysCommand manages MCP keys from the command line (e.g. the DigitalOcean console).
func keysCommand(ctx context.Context, st *store.Store, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, keysUsage)
		return 2
	}
	switch args[0] {
	case "create":
		scope, nameParts := store.ScopeWrite, []string{}
		for _, a := range args[1:] {
			if a == "--read" {
				scope = store.ScopeRead
			} else {
				nameParts = append(nameParts, a)
			}
		}
		name := strings.TrimSpace(strings.Join(nameParts, " "))
		if name == "" {
			fmt.Fprintln(os.Stderr, keysUsage)
			return 2
		}
		key, k, err := st.CreateKey(ctx, name, nil, scope)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		fmt.Printf("key #%d for %s (%s):\n%s\n", k.ID, k.Name, k.Scope, key)
	case "list":
		keys, err := st.ListKeys(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		for _, k := range keys {
			tg, used := "-", "never"
			if k.TGUserID != nil {
				tg = strconv.FormatInt(*k.TGUserID, 10)
			}
			if k.LastUsedAt != nil {
				used = k.LastUsedAt.Format("2006-01-02 15:04")
			}
			fmt.Printf("#%d\t%s\t%s\ttg:%s\tlast used: %s\n", k.ID, k.Name, k.Scope, tg, used)
		}
	case "revoke":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, keysUsage)
			return 2
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			fmt.Fprintln(os.Stderr, keysUsage)
			return 2
		}
		if err := st.RevokeKey(ctx, id); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		fmt.Printf("key #%d revoked\n", id)
	default:
		fmt.Fprintln(os.Stderr, keysUsage)
		return 2
	}
	return 0
}
