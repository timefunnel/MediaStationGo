package service

import (
	"context"
	"errors"
	"time"
)

func (s *MediaProbeCampaignService) Account(ctx context.Context) (*ProbeAccountView, error) {
	if !s.nodeConfigured() {
		return &ProbeAccountView{Phase: "unavailable"}, nil
	}
	work, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var view ProbeAccountView
	if err := s.node(work, "/v1/account", nil, &view); err != nil {
		return nil, err
	}
	view.Available = true
	return &view, nil
}

func (s *MediaProbeCampaignService) AccountAction(ctx context.Context, action, sessionID string, confirmed bool, cookie string) (*ProbeAccountView, error) {
	if action != "import-cookie" && cookie != "" {
		return nil, errors.New("仅导入已有登录操作可以接收 B Cookie")
	}
	if !s.nodeConfigured() {
		return nil, errors.New("独立补齐节点尚未配置")
	}
	s.mu.Lock()
	if s.active != "" {
		s.mu.Unlock()
		return nil, errors.New("请先停止补齐任务，再修改 B 登录")
	}
	// Polling/cancellation must not hold MSG's campaign mutex across network
	// waits. The node serializes grants with all B work under its own mutex.
	s.mu.Unlock()
	payload := map[string]any{}
	switch action {
	case "start", "import-cookie":
		if !confirmed {
			return nil, errors.New("请确认 B 授权的调用范围")
		}
		aID, _, err := s.sourceAccount(ctx)
		if err != nil {
			return nil, err
		}
		payload["a_account_id"], payload["confirmed"] = aID, true
		if action == "import-cookie" {
			if err := validateProbeAccountCookie(cookie, aID); err != nil {
				return nil, err
			}
			payload["cookie"] = cookie
		}
	case "poll", "cancel":
		if !validProbeNodeID(sessionID) {
			return nil, errors.New("B 授权会话标识无效")
		}
		payload["session_id"] = sessionID
	case "logout":
		if !confirmed {
			return nil, errors.New("请确认退出 B 登录")
		}
		payload["confirmed"] = true
	default:
		return nil, errors.New("B 授权操作无效")
	}
	work, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var view ProbeAccountView
	if err := s.node(work, "/v1/account/"+action, payload, &view); err != nil {
		return nil, err
	}
	view.Available = true
	return &view, nil
}
