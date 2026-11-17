package channel

import (
	"slices"
	"testing"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/memory"
)

// 听众的测试（规格 第四节、第十六节 B"听众"）：四种场合每个字段都点到，名字和号都是编的。

var (
	convGroup   = types.Conversation{Channel: chQQ, Scene: types.SceneGroup, ID: "900001"}
	convOwnerPM = types.Conversation{Channel: chQQ, Scene: types.ScenePrivate, ID: owner, Self: acctA}
	convFriend  = types.Conversation{Channel: chQQ, Scene: types.ScenePrivate, ID: friend, Self: acctA}
	convLocalG  = types.Conversation{Channel: "console", Scene: types.SceneGroup, ID: "homegroup"}
	convLocalPM = types.Conversation{Channel: "console", Scene: types.ScenePrivate, ID: "ca", Self: "ca"}
)

func msgBy(who string, addressed bool) types.Msg {
	return types.Msg{Seq: 1, Who: who, Addressed: addressed}
}

func batchOf(c types.Conversation, ms ...types.Msg) types.Batch {
	return types.Batch{Conv: c, Character: "ca", Msgs: ms}
}

func TestAudienceFourPlaces(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		b    types.Batch
		want audience.Audience
	}{
		{"本地私聊（主窗口）", batchOf(convLocalPM, msgBy(OwnerWho, true)), audience.Console(convLocalPM.String())},
		{"本地群聊，主人叫她", batchOf(convLocalG, msgBy(OwnerWho, true), msgBy("qq:"+friend, false)),
			audience.Audience{Channel: "console", Conversation: convLocalG.String(), Scene: "group", PersonIDs: []string{OwnerWho, "qq:" + friend}, PrivateOK: true, OwnerAsked: true}},
		{"QQ 私聊，对面是主人", batchOf(convOwnerPM, msgBy(OwnerWho, false)),
			audience.Audience{Channel: chQQ, Conversation: convOwnerPM.String(), Scene: "private", PersonIDs: []string{OwnerWho}, PrivateOK: true, OwnerAsked: true}},
		{"QQ 私聊，对面是朋友", batchOf(convFriend, msgBy("qq:"+friend, false)),
			audience.Audience{Channel: chQQ, Conversation: convFriend.String(), Scene: "private", PersonIDs: []string{"qq:" + friend}}},
		{"QQ 群，主人叫她", batchOf(convGroup, msgBy(OwnerWho, true)),
			audience.Audience{Channel: chQQ, Conversation: convGroup.String(), Scene: "group", PersonIDs: []string{OwnerWho}, OwnerAsked: true}},
		{"QQ 群，群友叫她", batchOf(convGroup, msgBy("qq:"+other, true)),
			audience.Audience{Channel: chQQ, Conversation: convGroup.String(), Scene: "group", PersonIDs: []string{"qq:" + other}}},
	}
	for _, c := range cases {
		got := AudienceOf(c.b, testRoster())
		if !audEqual(got, c.want) {
			t.Errorf("%s：\n 得到 %+v\n 应为 %+v", c.name, got, c.want)
		}
	}
}

func audEqual(a, b audience.Audience) bool {
	return a.Channel == b.Channel && a.Conversation == b.Conversation && a.Scene == b.Scene && slices.Equal(a.PersonIDs, b.PersonIDs) &&
		a.PrivateOK == b.PrivateOK && a.OwnerAsked == b.OwnerAsked
}

// 群里 OwnerAsked 按「触发这一轮的那几条」的发言人算：都是主人才为真（ADR-186 第 2 条）；没人被叫到取最后一条
func TestAudienceOwnerAskedInGroupFollowsTriggers(t *testing.T) {
	t.Parallel()
	friendQ := "qq:" + other
	cases := []struct {
		name string
		ms   []types.Msg
		want bool
	}{
		{"只有主人叫她", []types.Msg{msgBy(OwnerWho, true)}, true},
		{"主人叫她、群友没叫（群友的话只是在这一批里）", []types.Msg{msgBy(friendQ, false), msgBy(OwnerWho, true)}, true},
		{"群友叫她、主人没叫", []types.Msg{msgBy(OwnerWho, false), msgBy(friendQ, true)}, false},
		{"主人和群友同时叫她：混批不算主人在问", []types.Msg{msgBy(OwnerWho, true), msgBy(friendQ, true)}, false},
		{"两条都是主人叫的", []types.Msg{msgBy(OwnerWho, true), msgBy(OwnerWho, true)}, true},
		{"没人被叫到（看热闹放行），最后一条是主人", []types.Msg{msgBy(friendQ, false), msgBy(OwnerWho, false)}, true},
		{"没人被叫到（看热闹放行），最后一条是群友", []types.Msg{msgBy(OwnerWho, false), msgBy(friendQ, false)}, false},
		{"批里没有消息", nil, false},
		{"她自己的话不算触发", []types.Msg{{Seq: 2, Who: "ca", Self: true, Addressed: true}}, false},
	}
	for _, c := range cases {
		a := AudienceOf(batchOf(convGroup, c.ms...), testRoster())
		if a.OwnerAsked != c.want {
			t.Errorf("%s：OwnerAsked = %v，应为 %v", c.name, a.OwnerAsked, c.want)
		}
		if a.PrivateOK {
			t.Errorf("%s：QQ 群里 PrivateOK 必须是假", c.name)
		}
	}
}

// 私聊里 OwnerAsked 等于 PrivateOK，两种对面各一遍
func TestAudiencePrivateOwnerAskedEqualsPrivateOK(t *testing.T) {
	t.Parallel()
	for _, c := range []types.Conversation{convOwnerPM, convFriend} {
		a := AudienceOf(batchOf(c, msgBy("x", false)), testRoster())
		if a.OwnerAsked != a.PrivateOK {
			t.Errorf("%s：OwnerAsked %v 和 PrivateOK %v 不一样", c, a.OwnerAsked, a.PrivateOK)
		}
	}
}

// Scope：QQ 群里主人叫她和群友叫她是两档（两个实例，外部审计 F02）；同样的听众同样的档
func TestScopeOfOwnerAskedDiffersFromFriends(t *testing.T) {
	t.Parallel()
	r := testRoster()
	byOwner := ScopeOf(AudienceOf(batchOf(convGroup, msgBy(OwnerWho, true)), r))
	byFriend := ScopeOf(AudienceOf(batchOf(convGroup, msgBy("qq:"+other, true)), r))
	if byOwner == byFriend {
		t.Fatalf("主人问的和群友问的应该是两档，都是 %q", byOwner)
	}
	again := ScopeOf(AudienceOf(batchOf(convGroup, msgBy(OwnerWho, true)), r))
	if again != byOwner {
		t.Fatalf("同样的听众档不一样：%q、%q", byOwner, again)
	}
	if want := memory.ScopeKey(AudienceOf(batchOf(convGroup, msgBy(OwnerWho, true)), r)); byOwner != want {
		t.Fatalf("档应该就是记忆包算的：%q ≠ %q", byOwner, want)
	}
}
