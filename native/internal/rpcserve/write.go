package rpcserve

// 第 2 批的 35 个方法（规格-Bun后台第2批 第二、三节）。只有 WithWrites 的服务端才分派到这里。
// 后台对象没实现对应的 api 可选接口回 1001；哨兵错误按 3.2 的表回各自的码，客户端还原成同一个哨兵。

import (
	"context"
	"encoding/json"
	"errors"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/rpcconv"
	"sumika-native/internal/rpcwire"
)

// readMethods 是第 1 批的方法：不论带不带 WithWrites 都开
var readMethods = map[string]bool{
	rpcwire.MethodInitialize:              true,
	rpcwire.MethodRuntimeRead:             true,
	rpcwire.MethodRuntimeSubscribe:        true,
	rpcwire.MethodRuntimeUnsubscribe:      true,
	rpcwire.MethodOverviewRead:            true,
	rpcwire.MethodOverviewSubscribe:       true,
	rpcwire.MethodOverviewUnsubscribe:     true,
	rpcwire.MethodMessagesRead:            true,
	rpcwire.MethodConversationSubscribe:   true,
	rpcwire.MethodConversationUnsubscribe: true,
	rpcwire.MethodMediaRead:               true,
	rpcwire.MethodFacesIndex:              true,
	rpcwire.MethodFacesPNG:                true,
	rpcwire.MethodSettingsRead:            true,
}

func impl[T any](r api.Reader) bool {
	_, ok := r.(T)
	return ok
}

// writeCaps：第 2 批每个方法需要后台对象实现哪个接口
var writeCaps = map[string]func(api.Reader) bool{
	rpcwire.MethodAvatarRead:   impl[api.AvatarReader],
	rpcwire.MethodForwardRead:  impl[api.ForwardReader],
	rpcwire.MethodReadMarkSet:  impl[api.ReadMarker],
	rpcwire.MethodVideoPrepare: impl[api.VideoReader],

	rpcwire.MethodLocalSend:                 impl[api.LocalChat],
	rpcwire.MethodLocalStop:                 impl[api.LocalChat],
	rpcwire.MethodLocalApprovalsSubscribe:   impl[api.LocalChat],
	rpcwire.MethodLocalApprovalsUnsubscribe: impl[api.LocalChat],
	rpcwire.MethodLocalDecide:               impl[api.LocalChat],
	rpcwire.MethodLocalSendImages:           impl[api.LocalImageChat],
	rpcwire.MethodLocalSaveImage:            impl[api.LocalImageSaver],
	rpcwire.MethodLocalSendParts:            impl[api.LocalPartsChat],

	rpcwire.MethodWhisperSend:           impl[api.WhisperChat],
	rpcwire.MethodWhisperLogRead:        impl[api.WhisperLogReader],
	rpcwire.MethodWhisperLogEntry:       impl[api.WhisperLogReader],
	rpcwire.MethodWhisperLogSubscribe:   impl[api.WhisperLogReader],
	rpcwire.MethodWhisperLogUnsubscribe: impl[api.WhisperLogReader],
	rpcwire.MethodWhisperLogCancel:      impl[api.WhisperLogReader],

	rpcwire.MethodLocalStatusRead:        impl[api.LocalStatusReader],
	rpcwire.MethodLocalStatusSubscribe:   impl[api.LocalStatusReader],
	rpcwire.MethodLocalStatusUnsubscribe: impl[api.LocalStatusReader],
	rpcwire.MethodLoopAdd:                impl[api.LoopControl],
	rpcwire.MethodLoopEdit:               impl[api.LoopControl],
	rpcwire.MethodLoopPause:              impl[api.LoopControl],
	rpcwire.MethodLoopResume:             impl[api.LoopControl],
	rpcwire.MethodLoopRemove:             impl[api.LoopControl],
	rpcwire.MethodReminderOptions:        impl[api.ReminderControl],
	rpcwire.MethodReminderCreate:         impl[api.ReminderControl],
	rpcwire.MethodReminderUpdate:         impl[api.ReminderControl],
	rpcwire.MethodReminderFinish:         impl[api.ReminderControl],
	rpcwire.MethodReminderDelete:         impl[api.ReminderControl],

	rpcwire.MethodLocalGroupCreate: impl[api.LocalGroupWriter],
	rpcwire.MethodLocalGroupSend:   impl[api.LocalGroupWriter],
	rpcwire.MethodLocalGroupStop:   impl[api.LocalGroupWriter],

	rpcwire.MethodLocalSubmit:         impl[api.LocalInputs],
	rpcwire.MethodLocalStopRun:        impl[api.LocalInputs],
	rpcwire.MethodLocalRunSubscribe:   impl[api.LocalInputs],
	rpcwire.MethodLocalRunUnsubscribe: impl[api.LocalInputs],

	rpcwire.MethodHeldList:      impl[api.LocalHeld],
	rpcwire.MethodHeldRelease:   impl[api.LocalHeld],
	rpcwire.MethodHeldDiscard:   impl[api.LocalHeld],
	rpcwire.MethodLocalWithdraw: impl[api.LocalHeld],

	rpcwire.MethodLocalQuestionsSubscribe:   impl[api.LocalQuestions],
	rpcwire.MethodLocalQuestionsUnsubscribe: impl[api.LocalQuestions],
	rpcwire.MethodLocalAnswer:               impl[api.LocalQuestions],

	rpcwire.MethodApprovalRulesList:   impl[api.ApprovalRules],
	rpcwire.MethodApprovalRulesRevoke: impl[api.ApprovalRules],
	rpcwire.MethodApprovalRulesAdd:    impl[api.ApprovalRuleEditor],

	rpcwire.MethodConversationApprovalRead: impl[api.ConversationApproval],
	rpcwire.MethodConversationApprovalSet:  impl[api.ConversationApproval],

	rpcwire.MethodWorkspaceRead:   impl[api.LocalWorkspaces],
	rpcwire.MethodWorkspaceSet:    impl[api.LocalWorkspaces],
	rpcwire.MethodWorkspaceClear:  impl[api.LocalWorkspaces],
	rpcwire.MethodWorkspaceRecent: impl[api.LocalWorkspaces],

	rpcwire.MethodCheckpointsList:               impl[api.CheckpointActions],
	rpcwire.MethodCheckpointsPreview:            impl[api.CheckpointActions],
	rpcwire.MethodCheckpointsRestore:            impl[api.CheckpointActions],
	rpcwire.MethodCheckpointsProgress:           impl[api.CheckpointActions],
	rpcwire.MethodCheckpointsCancel:             impl[api.CheckpointActions],
	rpcwire.MethodCheckpointProgressSubscribe:   impl[api.CheckpointActions],
	rpcwire.MethodCheckpointProgressUnsubscribe: impl[api.CheckpointActions],
}

// noTimeout：可能跑很久的方法，不套 30 秒上限（整轮模型回复、检查点还原）；跟着连接的寿命走，连接断了就取消
var noTimeout = map[string]bool{
	rpcwire.MethodLocalSend:          true,
	rpcwire.MethodLocalSendImages:    true,
	rpcwire.MethodLocalSendParts:     true,
	rpcwire.MethodLocalGroupSend:     true,
	rpcwire.MethodCheckpointsRestore: true,
}

// wireErr 把后台返回的错误变成协议错误：哨兵按表回各自的码（errors.Is 判），其余 1002 带原文
func wireErr(err error) *rpcwire.Error {
	switch {
	case errors.Is(err, api.ErrLocalChatUnsupported):
		return errMsg(rpcwire.CodeLocalChatUnsupported, err.Error())
	case errors.Is(err, api.ErrLocalChatBusy):
		return errMsg(rpcwire.CodeLocalChatBusy, err.Error())
	case errors.Is(err, api.ErrLocalGroupUnsupported):
		return errMsg(rpcwire.CodeLocalGroupUnsupported, err.Error())
	case errors.Is(err, api.ErrLocalGroupBusy):
		return errMsg(rpcwire.CodeLocalGroupBusy, err.Error())
	case errors.Is(err, api.ErrApprovalGone):
		return errMsg(rpcwire.CodeApprovalGone, err.Error())
	case errors.Is(err, api.ErrQuestionGone):
		return errMsg(rpcwire.CodeQuestionGone, err.Error())
	case errors.Is(err, api.ErrQuoteTarget):
		return errMsg(rpcwire.CodeQuoteTarget, err.Error())
	case errors.Is(err, api.ErrPart):
		return errMsg(rpcwire.CodeBadPart, err.Error())
	case errors.Is(err, api.ErrReadMarkUnsupported):
		return errMsg(rpcwire.CodeReadMarkUnsupported, err.Error())
	case errors.Is(err, api.ErrNoAvatar):
		return errMsg(rpcwire.CodeNoAvatar, err.Error())
	case errors.Is(err, api.ErrNotFound):
		return errMsg(rpcwire.CodeNotFound, err.Error())
	}
	return callErr(err)
}

func unsupportedErr(method string) *rpcwire.Error {
	return errMsg(rpcwire.CodeUnsupported, "这个后台没有 "+method)
}

// groupProcessFn：同上，推 local_group.process；和草稿走同一条连接，先后顺序就是后台回调的顺序
func (c *conn) groupProcessFn(stream string) func(string, []api.LocalProcessStep) {
	if stream == "" {
		return nil
	}
	return func(turn string, steps []api.LocalProcessStep) {
		c.notify(rpcwire.MethodLocalGroupProcess, rpcwire.LocalGroupProcessNotice{Stream: stream, Turn: turn, Steps: rpcconv.StepsOut(steps)})
	}
}

// draftFn：stream 非空时，每次回调推一条 local.draft 通知；空就不推（传 nil）。
// 同一条连接上通知先于回复发出，所以"回调都在返回之前"的顺序不变
func (c *conn) draftFn(stream string) func(api.LocalDraft) {
	if stream == "" {
		return nil
	}
	return func(d api.LocalDraft) {
		c.notify(rpcwire.MethodLocalDraft, rpcwire.LocalDraftNotice{Stream: stream, Draft: rpcconv.DraftOut(d)})
	}
}

// do 解参数、取接口、调用：后台没实现接口回 1001，调用出错走 wireErr
func do[T, P any](c *conn, method string, params json.RawMessage, f func(T, P) (any, error)) (any, *rpcwire.Error) {
	var p P
	if e := decode(params, &p); e != nil {
		return nil, e
	}
	t, ok := c.s.reader.(T)
	if !ok {
		return nil, unsupportedErr(method)
	}
	res, err := f(t, p)
	if err != nil {
		return nil, wireErr(err)
	}
	return res, nil
}

// sub：订阅类方法的取接口部分
func capOf[T any](c *conn, method string) (T, *rpcwire.Error) {
	t, ok := c.s.reader.(T)
	if !ok {
		var z T
		return z, unsupportedErr(method)
	}
	return t, nil
}

func (c *conn) dispatchWrite(ctx context.Context, method string, params json.RawMessage) (any, *rpcwire.Error) {
	empty := rpcwire.Empty{}
	switch method {
	case rpcwire.MethodAvatarRead:
		return do(c, method, params, func(r api.AvatarReader, p rpcwire.AvatarReadParams) (any, error) {
			res, err := r.ReadAvatar(ctx, p.Key)
			if err != nil {
				return nil, err
			}
			return rpcconv.ResultOut(res, rpcconv.MediaOut), nil
		})
	case rpcwire.MethodForwardRead:
		return do(c, method, params, func(r api.ForwardReader, p rpcwire.ForwardRequest) (any, error) {
			res, err := r.ReadForward(ctx, rpcconv.ForwardRequestIn(p))
			if err != nil {
				// "太大"这类失败带着一个说明状态的值，界面靠它挑文案：随失败一起带过去
				if res.Value.State != "" {
					out := rpcconv.ResultOut(res, rpcconv.ForwardOut)
					v := rpcconv.ForwardOut(res.Value)
					out.Value = &v
					out.State = rpcwire.StateFailed
					out.Error = &rpcwire.ResultError{Code: rpcwire.StateFailed, Message: err.Error()}
					return out, nil
				}
				return nil, err
			}
			return rpcconv.ResultOut(res, rpcconv.ForwardOut), nil
		})
	case rpcwire.MethodReadMarkSet:
		return do(c, method, params, func(r api.ReadMarker, p rpcwire.ReadMarkParams) (any, error) {
			return empty, r.MarkRead(ctx, rpcconv.KeyIn(p.Conversation), p.UpTo)
		})
	case rpcwire.MethodVideoPrepare:
		return do(c, method, params, func(r api.VideoReader, p rpcwire.MediaRef) (any, error) {
			path, err := r.PrepareVideo(ctx, rpcconv.MediaRefIn(p))
			if err != nil {
				return nil, err
			}
			return rpcwire.PathResult{Path: path}, nil
		})

	case rpcwire.MethodLocalSend:
		return do(c, method, params, func(r api.LocalChat, p rpcwire.LocalSendParams) (any, error) {
			return empty, r.SendLocal(ctx, rpcconv.KeyIn(p.Conversation), p.Text, c.draftFn(p.Stream))
		})
	case rpcwire.MethodLocalSendImages:
		return do(c, method, params, func(r api.LocalImageChat, p rpcwire.LocalSendImagesParams) (any, error) {
			return empty, r.SendLocalImages(ctx, rpcconv.KeyIn(p.Conversation), p.Text, p.Images, c.draftFn(p.Stream))
		})
	case rpcwire.MethodLocalSendParts:
		return do(c, method, params, func(r api.LocalPartsChat, p rpcwire.LocalSendPartsParams) (any, error) {
			return empty, r.SendLocalParts(ctx, rpcconv.KeyIn(p.Conversation), rpcconv.PartsIn(p.Parts), c.draftFn(p.Stream))
		})
	case rpcwire.MethodWhisperSend:
		return do(c, method, params, func(r api.WhisperChat, p rpcwire.WhisperSendParams) (any, error) {
			rc, err := r.SendWhisper(ctx, rpcconv.KeyIn(p.Conversation), rpcconv.PartsIn(p.Parts))
			if err != nil {
				return nil, err
			}
			return rpcconv.WhisperReceiptOut(rc), nil
		})
	case rpcwire.MethodWhisperLogRead:
		return do(c, method, params, func(r api.WhisperLogReader, p rpcwire.WhisperLogReadParams) (any, error) {
			page, err := r.ReadWhisperLog(ctx, rpcconv.KeyIn(p.Conversation), p.Cursor, p.Limit)
			if err != nil {
				return nil, err
			}
			return rpcconv.LogPageOut(page), nil
		})
	case rpcwire.MethodWhisperLogEntry:
		return do(c, method, params, func(r api.WhisperLogReader, p rpcwire.WhisperLogEntryParams) (any, error) {
			d, err := r.ReadWhisperLogEntry(ctx, rpcconv.KeyIn(p.Conversation), p.ID, p.WithThinking)
			if err != nil {
				return nil, err
			}
			return rpcconv.LogDetailOut(d), nil
		})
	case rpcwire.MethodWhisperLogCancel:
		return do(c, method, params, func(r api.WhisperLogReader, p rpcwire.WhisperCancelParams) (any, error) {
			n, err := r.CancelWhisper(ctx, rpcconv.KeyIn(p.Conversation), p.Batch, p.Character)
			if err != nil {
				return nil, err
			}
			return rpcwire.WhisperCancelResult{Cancelled: n}, nil
		})
	case rpcwire.MethodWhisperLogSubscribe:
		var p rpcwire.ConversationParams
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		r, e := capOf[api.WhisperLogReader](c, method)
		if e != nil {
			return nil, e
		}
		return subscribe(c, rpcwire.MethodWhisperLogSnapshot, func(ctx context.Context) (api.Subscription[ctlproto.LogPage], error) {
			return r.WatchWhisperLog(ctx, rpcconv.KeyIn(p.Conversation))
		}, func(id string, v uint64, res api.Result[ctlproto.LogPage]) any {
			return rpcwire.WhisperLogSnapshot{Subscription: id, Version: int64(v), Result: rpcconv.ResultOut(res, rpcconv.LogPageOut)}
		})
	case rpcwire.MethodLocalStatusRead:
		return do(c, method, params, func(r api.LocalStatusReader, p rpcwire.ConversationParams) (any, error) {
			st, err := r.ReadLocalStatus(ctx, rpcconv.KeyIn(p.Conversation))
			if err != nil {
				return nil, err
			}
			return rpcconv.LocalStatusOut(st), nil
		})
	case rpcwire.MethodLocalStatusSubscribe:
		r, e := capOf[api.LocalStatusReader](c, method)
		if e != nil {
			return nil, e
		}
		return subscribe(c, rpcwire.MethodLocalStatusSnapshot, func(ctx context.Context) (api.Subscription[[]api.LocalStatus], error) {
			return r.WatchLocalStatus(ctx)
		}, func(id string, v uint64, res api.Result[[]api.LocalStatus]) any {
			return rpcwire.LocalStatusSnapshot{Subscription: id, Version: int64(v), Result: rpcconv.ResultOut(res, rpcconv.LocalStatusesOut)}
		})
	case rpcwire.MethodLoopAdd:
		return do(c, method, params, func(r api.LoopControl, p rpcwire.LoopAddParams) (any, error) {
			id, err := r.AddLoop(ctx, rpcconv.KeyIn(p.Conversation), rpcconv.LoopSpecIn(p.Spec))
			if err != nil {
				return nil, err
			}
			return rpcwire.LoopAddResult{ID: id}, nil
		})
	case rpcwire.MethodLoopEdit:
		return do(c, method, params, func(r api.LoopControl, p rpcwire.LoopEditParams) (any, error) {
			return empty, r.EditLoop(ctx, rpcconv.KeyIn(p.Conversation), p.ID, rpcconv.LoopSpecIn(p.Spec))
		})
	case rpcwire.MethodLoopPause:
		return do(c, method, params, func(r api.LoopControl, p rpcwire.ItemParams) (any, error) {
			return empty, r.PauseLoop(ctx, rpcconv.KeyIn(p.Conversation), p.ID)
		})
	case rpcwire.MethodLoopResume:
		return do(c, method, params, func(r api.LoopControl, p rpcwire.ItemParams) (any, error) {
			return empty, r.ResumeLoop(ctx, rpcconv.KeyIn(p.Conversation), p.ID)
		})
	case rpcwire.MethodLoopRemove:
		return do(c, method, params, func(r api.LoopControl, p rpcwire.ItemParams) (any, error) {
			return empty, r.RemoveLoop(ctx, rpcconv.KeyIn(p.Conversation), p.ID)
		})
	case rpcwire.MethodReminderOptions:
		return do(c, method, params, func(r api.ReminderControl, _ rpcwire.Empty) (any, error) {
			return rpcconv.ReminderOptionsOut(r.ReminderOptions()), nil
		})
	case rpcwire.MethodReminderCreate:
		return do(c, method, params, func(r api.ReminderControl, p rpcwire.ReminderCreateParams) (any, error) {
			return empty, r.CreateReminder(ctx, rpcconv.KeyIn(p.Conversation), rpcconv.ReminderDraftIn(p.Draft))
		})
	case rpcwire.MethodReminderUpdate:
		return do(c, method, params, func(r api.ReminderControl, p rpcwire.ReminderUpdateParams) (any, error) {
			return empty, r.UpdateReminder(ctx, rpcconv.KeyIn(p.Conversation), p.ID, rpcconv.ReminderPatchIn(p.Patch))
		})
	case rpcwire.MethodReminderFinish:
		return do(c, method, params, func(r api.ReminderControl, p rpcwire.ReminderFinishParams) (any, error) {
			return empty, r.FinishReminder(ctx, rpcconv.KeyIn(p.Conversation), p.ID, p.Done)
		})
	case rpcwire.MethodReminderDelete:
		return do(c, method, params, func(r api.ReminderControl, p rpcwire.ItemParams) (any, error) {
			return empty, r.DeleteReminder(ctx, rpcconv.KeyIn(p.Conversation), p.ID)
		})
	case rpcwire.MethodLocalGroupCreate:
		return do(c, method, params, func(r api.LocalGroupWriter, p rpcwire.LocalGroupCreateParams) (any, error) {
			v, err := r.CreateLocalGroup(ctx, api.LocalGroupCreate{Name: p.Name, Characters: p.Characters})
			if err != nil {
				return nil, err
			}
			return rpcconv.ConversationOut(v), nil
		})
	case rpcwire.MethodLocalGroupSend:
		return do(c, method, params, func(r api.LocalGroupWriter, p rpcwire.LocalGroupSendParams) (any, error) {
			return empty, r.SendLocalGroup(ctx, api.LocalGroupSend{Key: rpcconv.KeyIn(p.Conversation), Text: p.Text, Mentions: p.Mentions,
				Draft: c.draftFn(p.Stream), Process: c.groupProcessFn(p.Stream)})
		})
	case rpcwire.MethodLocalGroupStop:
		return do(c, method, params, func(r api.LocalGroupWriter, p rpcwire.ConversationParams) (any, error) {
			return empty, r.StopLocalGroup(ctx, rpcconv.KeyIn(p.Conversation))
		})
	case rpcwire.MethodLocalSaveImage:
		return do(c, method, params, func(r api.LocalImageSaver, p rpcwire.LocalSaveImageParams) (any, error) {
			a, err := r.SaveLocalImage(ctx, rpcconv.KeyIn(p.Conversation), p.PNG)
			if err != nil {
				return nil, err
			}
			return rpcconv.AttachmentOut(a), nil
		})
	case rpcwire.MethodLocalStop:
		return do(c, method, params, func(r api.LocalChat, p rpcwire.ConversationParams) (any, error) {
			return empty, r.StopLocal(ctx, rpcconv.KeyIn(p.Conversation))
		})
	case rpcwire.MethodLocalDecide:
		return do(c, method, params, func(r api.LocalChat, p rpcwire.LocalDecideParams) (any, error) {
			return empty, r.DecideLocal(ctx, rpcconv.KeyIn(p.Conversation), p.ID, api.ApprovalChoice(p.Choice))
		})
	case rpcwire.MethodLocalApprovalsSubscribe:
		if e := decode(params, &rpcwire.Empty{}); e != nil {
			return nil, e
		}
		r, e := capOf[api.LocalChat](c, method)
		if e != nil {
			return nil, e
		}
		return subscribe(c, rpcwire.MethodLocalApprovalsSnapshot, r.WatchLocalApprovals,
			func(id string, v uint64, res api.Result[[]api.LocalApproval]) any {
				return rpcwire.LocalApprovalsSnapshot{Subscription: id, Version: int64(v), Result: rpcconv.ResultOut(res, listOf(rpcconv.ApprovalsOut))}
			})

	case rpcwire.MethodLocalSubmit:
		return do(c, method, params, func(r api.LocalInputs, p rpcwire.LocalSubmitParams) (any, error) {
			rc, err := r.SubmitLocal(ctx, rpcconv.KeyIn(p.Conversation), rpcconv.SubmissionIn(p.Submission))
			if err != nil {
				return nil, err
			}
			return rpcconv.ReceiptOut(rc), nil
		})
	case rpcwire.MethodLocalStopRun:
		return do(c, method, params, func(r api.LocalInputs, p rpcwire.LocalStopRunParams) (any, error) {
			if p.Through < 0 {
				return nil, errors.New("through 不能是负数")
			}
			rc, err := r.StopLocalRun(ctx, rpcconv.KeyIn(p.Conversation), p.RunID, uint64(p.Through))
			if err != nil {
				return nil, err
			}
			return rpcconv.StopReceiptOut(rc), nil
		})
	case rpcwire.MethodLocalRunSubscribe:
		var p rpcwire.LocalRunParams
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		r, e := capOf[api.LocalInputs](c, method)
		if e != nil {
			return nil, e
		}
		return subscribe(c, rpcwire.MethodLocalRunSnapshot, func(ctx context.Context) (api.Subscription[api.LocalRunSnapshot], error) {
			return r.WatchLocalRun(ctx, rpcconv.KeyIn(p.Conversation), p.RunID)
		}, func(id string, v uint64, res api.Result[api.LocalRunSnapshot]) any {
			return rpcwire.LocalRunSnapshotNotice{Subscription: id, Version: int64(v), Result: rpcconv.ResultOut(res, rpcconv.RunOut)}
		})

	case rpcwire.MethodHeldList:
		return do(c, method, params, func(r api.LocalHeld, p rpcwire.Empty) (any, error) {
			l, err := r.HeldLocal(ctx)
			if err != nil {
				return nil, err
			}
			return rpcconv.HeldOut(l), nil
		})
	case rpcwire.MethodHeldRelease:
		return do(c, method, params, func(r api.LocalHeld, p rpcwire.InputParams) (any, error) {
			rc, err := r.ReleaseHeldLocal(ctx, rpcconv.KeyIn(p.Conversation), p.InputID)
			if err != nil {
				return nil, err
			}
			return rpcconv.ReceiptOut(rc), nil
		})
	case rpcwire.MethodHeldDiscard:
		return do(c, method, params, func(r api.LocalHeld, p rpcwire.InputParams) (any, error) {
			return empty, r.DiscardHeldLocal(ctx, rpcconv.KeyIn(p.Conversation), p.InputID)
		})
	case rpcwire.MethodLocalWithdraw:
		return do(c, method, params, func(r api.LocalHeld, p rpcwire.InputParams) (any, error) {
			return empty, r.WithdrawQueuedLocal(ctx, rpcconv.KeyIn(p.Conversation), p.InputID)
		})

	case rpcwire.MethodLocalQuestionsSubscribe:
		if e := decode(params, &rpcwire.Empty{}); e != nil {
			return nil, e
		}
		r, e := capOf[api.LocalQuestions](c, method)
		if e != nil {
			return nil, e
		}
		return subscribe(c, rpcwire.MethodLocalQuestionsSnapshot, r.WatchLocalQuestions,
			func(id string, v uint64, res api.Result[[]api.LocalQuestion]) any {
				return rpcwire.LocalQuestionsSnapshot{Subscription: id, Version: int64(v), Result: rpcconv.ResultOut(res, listOf(rpcconv.QuestionsOut))}
			})
	case rpcwire.MethodLocalAnswer:
		return do(c, method, params, func(r api.LocalQuestions, p rpcwire.LocalAnswerParams) (any, error) {
			return empty, r.AnswerLocal(ctx, rpcconv.KeyIn(p.Conversation), p.ID, p.Option)
		})

	case rpcwire.MethodApprovalRulesList:
		return do(c, method, params, func(r api.ApprovalRules, p rpcwire.Empty) (any, error) {
			l, err := r.ListApprovalRules(ctx)
			if err != nil {
				return nil, err
			}
			return rpcconv.RulesOut(l), nil
		})
	case rpcwire.MethodApprovalRulesRevoke:
		return do(c, method, params, func(r api.ApprovalRules, p rpcwire.ApprovalRule) (any, error) {
			return empty, r.RevokeApprovalRule(ctx, rpcconv.RuleIn(p))
		})
	case rpcwire.MethodApprovalRulesAdd:
		return do(c, method, params, func(r api.ApprovalRuleEditor, p rpcwire.ApprovalRuleAddParams) (any, error) {
			return empty, r.AddApprovalRule(ctx, p.Raw, p.Effect, p.Project, p.Session)
		})

	case rpcwire.MethodConversationApprovalRead:
		return do(c, method, params, func(r api.ConversationApproval, p rpcwire.ConversationParams) (any, error) {
			m, err := r.ConversationApprovalMode(ctx, rpcconv.KeyIn(p.Conversation))
			if err != nil {
				return nil, err
			}
			return rpcconv.ApprovalModeOut(m), nil
		})
	case rpcwire.MethodConversationApprovalSet:
		return do(c, method, params, func(r api.ConversationApproval, p rpcwire.ConversationApprovalSetParams) (any, error) {
			return empty, r.SetConversationApprovalMode(ctx, rpcconv.KeyIn(p.Conversation), p.Mode)
		})

	case rpcwire.MethodWorkspaceRead:
		return do(c, method, params, func(r api.LocalWorkspaces, p rpcwire.ConversationParams) (any, error) {
			w, err := r.ReadLocalWorkspace(ctx, rpcconv.KeyIn(p.Conversation))
			if err != nil {
				return nil, err
			}
			return rpcconv.WorkspaceOut(w), nil
		})
	case rpcwire.MethodWorkspaceSet:
		return do(c, method, params, func(r api.LocalWorkspaces, p rpcwire.WorkspaceSetParams) (any, error) {
			w, err := r.SetLocalWorkspace(ctx, rpcconv.KeyIn(p.Conversation), p.Dir)
			if err != nil {
				return nil, err
			}
			return rpcconv.WorkspaceOut(w), nil
		})
	case rpcwire.MethodWorkspaceClear:
		return do(c, method, params, func(r api.LocalWorkspaces, p rpcwire.ConversationParams) (any, error) {
			w, err := r.ClearLocalWorkspace(ctx, rpcconv.KeyIn(p.Conversation))
			if err != nil {
				return nil, err
			}
			return rpcconv.WorkspaceOut(w), nil
		})
	case rpcwire.MethodWorkspaceRecent:
		return do(c, method, params, func(r api.LocalWorkspaces, p rpcwire.Empty) (any, error) {
			l, err := r.RecentWorkspaces(ctx)
			if err != nil {
				return nil, err
			}
			return rpcwire.StringList{Items: append([]string(nil), l...)}, nil
		})

	case rpcwire.MethodCheckpointsList:
		return do(c, method, params, func(r api.CheckpointActions, p rpcwire.ConversationParams) (any, error) {
			l, err := r.ListCheckpoints(ctx, rpcconv.KeyIn(p.Conversation))
			if err != nil {
				return nil, err
			}
			return rpcconv.CheckpointListOut(l), nil
		})
	case rpcwire.MethodCheckpointsPreview:
		return do(c, method, params, func(r api.CheckpointActions, p rpcwire.CheckpointPreviewParams) (any, error) {
			plan, err := r.PreviewCheckpoint(ctx, rpcconv.KeyIn(p.Conversation), p.ID, p.Mode)
			if err != nil {
				return nil, err
			}
			return rpcconv.CheckpointPlanOut(plan), nil
		})
	case rpcwire.MethodCheckpointsRestore:
		return do(c, method, params, func(r api.CheckpointActions, p rpcwire.CheckpointRestoreParams) (any, error) {
			res, err := r.RestoreCheckpoint(ctx, rpcconv.KeyIn(p.Conversation), p.ID, p.Mode, p.Version)
			if err != nil {
				return nil, err
			}
			return rpcconv.CheckpointRestoreOut(res), nil
		})
	case rpcwire.MethodCheckpointsProgress:
		return do(c, method, params, func(r api.CheckpointActions, p rpcwire.CheckpointTargetParams) (any, error) {
			g, err := r.ReadCheckpointProgress(ctx, rpcconv.KeyIn(p.Conversation), p.TargetID)
			if err != nil {
				return nil, err
			}
			return rpcconv.CheckpointProgressOut(g), nil
		})
	case rpcwire.MethodCheckpointsCancel:
		return do(c, method, params, func(r api.CheckpointActions, p rpcwire.CheckpointTargetParams) (any, error) {
			g, err := r.CancelCheckpointRestore(ctx, rpcconv.KeyIn(p.Conversation), p.TargetID)
			if err != nil {
				return nil, err
			}
			return rpcconv.CheckpointProgressOut(g), nil
		})
	case rpcwire.MethodCheckpointProgressSubscribe:
		var p rpcwire.CheckpointTargetParams
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		r, e := capOf[api.CheckpointActions](c, method)
		if e != nil {
			return nil, e
		}
		return subscribe(c, rpcwire.MethodCheckpointProgressSnapshot, func(ctx context.Context) (api.Subscription[api.CheckpointProgress], error) {
			return r.WatchCheckpointProgress(ctx, rpcconv.KeyIn(p.Conversation), p.TargetID)
		}, func(id string, v uint64, res api.Result[api.CheckpointProgress]) any {
			return rpcwire.CheckpointProgressSnapshot{Subscription: id, Version: int64(v), Result: rpcconv.ResultOut(res, rpcconv.CheckpointProgressOut)}
		})

	case rpcwire.MethodLocalStatusUnsubscribe, rpcwire.MethodWhisperLogUnsubscribe, rpcwire.MethodLocalApprovalsUnsubscribe, rpcwire.MethodLocalRunUnsubscribe, rpcwire.MethodLocalQuestionsUnsubscribe, rpcwire.MethodCheckpointProgressUnsubscribe:
		if _, ok := writeCaps[method]; !ok || !writeCaps[method](c.s.reader) {
			return nil, unsupportedErr(method)
		}
		var p rpcwire.UnsubscribeParams
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		return c.unsubscribe(p.Subscription)
	}
	return nil, errMsg(rpcwire.CodeMethodNotFound, "没有这个方法："+method)
}

// listOf 让列表类的转换函数在空列表时也回 []（而不是 null），别的语言的客户端不用判 null
func listOf[A, B any](f func([]A) []B) func([]A) []B {
	return func(l []A) []B {
		out := f(l)
		if out == nil {
			out = []B{}
		}
		return out
	}
}
