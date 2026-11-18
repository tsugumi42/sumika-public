package app

import (
	"sumika-native/internal/applog"
	"sumika-native/internal/channel"
	"sumika-native/internal/forwardfetch"
)

func (a *App) openForwards() error {
	if !a.cfg.QQ.Forward.Fetch {
		return nil
	}
	if u, off := a.set.Unavailable()["qq"]; off {
		a.logf("QQ 的库没打开，引用和转发不取：%s", u.Reason)
		return nil
	}
	st, err := a.set.For("qq")
	if err != nil {
		return err
	}
	o := forwardfetch.Options{Store: st, Reader: a.adapter, Account: a.account, Logf: a.logfFor(applog.Channel)}
	if a.media != nil {
		o.Media = a.media
	}
	a.forwards = forwardfetch.New(o)
	a.forwards.Start()
	return nil
}
func (a *App) closeForwards() {
	if a.forwards != nil {
		a.forwards.Close()
	}
}
func (a *App) withForwards(next func(channel.Stored)) func(channel.Stored) {
	return func(s channel.Stored) {
		if a.forwards != nil && s.Inbound.Conv.Channel == "qq" && !s.Duplicate {
			a.forwards.Offer(s.Inbound.Conv.String(), s.Inbound.Segs)
		}
		next(s)
	}
}
