package main

import (
	"context"
	"fmt"
	"os"

	"github.com/zh0glikk/tasks-manager/internal/store"
	"github.com/zh0glikk/tasks-manager/internal/telegram"
)

const importUsage = `usage:
  app import-telegram FILE [--watch]   import a Telegram Desktop chat export (result.json)
                                       and allow the chat as main (or watch)`

func importCommand(ctx context.Context, st *store.Store, args []string) int {
	var file string
	kind := store.ChatMain
	for _, a := range args {
		switch a {
		case "--watch":
			kind = store.ChatWatch
		default:
			file = a
		}
	}
	if file == "" {
		fmt.Fprintln(os.Stderr, importUsage)
		return 2
	}
	f, err := os.Open(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer f.Close()
	exp, err := telegram.ParseExport(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	chatID, err := exp.ChatID()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if _, _, err := st.TouchChat(ctx, chatID, exp.Name, store.ChatAllowed); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := st.AllowChat(ctx, chatID, kind); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	msgs := exp.ChatMessages(chatID)
	n, err := st.ImportMessages(ctx, chatID, msgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("chat %d %q (%s): imported %d of %d messages\n", chatID, exp.Name, kind, n, len(msgs))
	if n < len(msgs) {
		fmt.Println("skipped messages are newer than the first one the bot already stored live")
	}
	return 0
}
