// visible-events 只读枚举一个会话的可见事件身份，供人工冒烟对照，不打印正文。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"sumika-native/internal/datalock"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("visible-events", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	data := fs.String("data", "", "已有 Go 数据目录")
	conv := fs.String("conversation", "", "完整会话身份")
	chars := fs.String("characters", "", "可查看的角色 ID，逗号分隔")
	character := fs.String("character", "", "可选观察角色过滤")
	if err := fs.Parse(args); err != nil {
		return errors.New("参数错误")
	}
	if *data == "" || *conv == "" || *chars == "" || fs.NArg() != 0 {
		return errors.New("要给 -data、-conversation、-characters")
	}
	p, err := paths.Open(*data)
	if err != nil {
		return errors.New("数据目录不可读")
	}
	lock, err := datalock.LockData(p.Root)
	if err != nil {
		return errors.New("数据组正在使用")
	}
	defer lock.Close()
	set, err := store.OpenGoReadOnly(ctx, p)
	if err != nil {
		return errors.New("已有 Go 数据库不可读")
	}
	defer set.Close()
	platform, err := store.PlatformOfConversation(*conv)
	if err != nil {
		return store.ErrDisplayScope
	}
	st, err := set.ForConversation(*conv)
	if err != nil {
		return store.ErrDisplayScope
	}
	scope := store.DisplayScope{Platform: platform, Conversation: *conv, Characters: strings.Split(*chars, ","), Character: *character}
	cursor := ""
	for {
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		page, err := st.ReadDisplay(queryCtx, scope, cursor, 200)
		cancel()
		if err != nil {
			return err
		}
		for _, row := range page.Items {
			if _, err := fmt.Fprintf(out, "%s\t%s\t%d\t%d\t%t\n", platform, row.ID, row.Seq, row.CreatedAt, row.Recalled); err != nil {
				return err
			}
		}
		if !page.HasMore {
			return nil
		}
		cursor = page.NextCursor
	}
}
