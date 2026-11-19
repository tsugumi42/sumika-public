package main

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
)

type demoLinkSettings struct {
	mu    sync.Mutex
	raw   []byte
	known []api.KnownLinkConversation
}

func newDemoLinkSettings() *demoLinkSettings {
	d := &demoLinkSettings{raw: []byte("linkReader: {}\n")}
	for i := range 20 {
		kind, id := "group", fmt.Sprintf("qq:group:%d", 90001+i)
		name := fmt.Sprintf("纸船讨论室 %d", i+1)
		if i >= 12 {
			kind = "private"
			id = fmt.Sprintf("qq:private:91001:%d", 92001+i)
			name = fmt.Sprintf("纸灯朋友 %d", i-11)
		}
		d.known = append(d.known, api.KnownLinkConversation{ID: id, Name: name, Kind: kind, Account: "91***01"})
	}
	return d
}
func (d *demoLinkSettings) view() (api.LinkSettingsView, error) {
	c, e := config.Parse(d.raw, "演示配置")
	if e != nil {
		return api.LinkSettingsView{}, e
	}
	values := api.LinkValuesOf(c.LinkReader)
	return api.LinkSettingsView{Configured: values, Running: values, Version: config.ContentVersion(d.raw), RunningVersion: config.ContentVersion(d.raw), KnownConversations: slices.Clone(d.known), RunningReason: "演示设置只保留在当前窗口"}, nil
}
func (d *demoLinkSettings) LinkSettings(ctx context.Context) (api.LinkSettingsView, error) {
	if e := ctx.Err(); e != nil {
		return api.LinkSettingsView{}, e
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.view()
}
func (d *demoLinkSettings) SetLinkSettings(ctx context.Context, ch api.LinkSettingsChange, base string) (api.LinkSettingsView, error) {
	if e := ctx.Err(); e != nil {
		return api.LinkSettingsView{}, e
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if base != config.ContentVersion(d.raw) {
		v, e := d.view()
		if e != nil {
			return v, e
		}
		return v, config.ErrConfigVersion
	}
	out, _, e := config.PatchLink(d.raw, "演示配置", ch)
	if e != nil {
		v, _ := d.view()
		return v, e
	}
	d.raw = out
	return d.view()
}
