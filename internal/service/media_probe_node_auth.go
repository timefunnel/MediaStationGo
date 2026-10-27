package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const probeAccountMaxRequests = 29 // One QR + image, 20 status checks, 7 grant/identity requests.
const probeAccountMaxPolls = 20

type ProbeAccountView struct {
	Available        bool      `json:"available"`
	Configured       bool      `json:"configured"`
	AccountID        string    `json:"account_id,omitempty"`
	PendingAccountID string    `json:"pending_account_id,omitempty"`
	SessionID        string    `json:"session_id,omitempty"`
	Phase            string    `json:"phase"`
	Image            string    `json:"image,omitempty"`
	ExpiresAt        time.Time `json:"expires_at,omitempty"`
	Requests         int       `json:"requests"`
	Error            string    `json:"error,omitempty"`
}

type probeAccountQR struct {
	UID  string          `json:"uid"`
	Time probeWireNumber `json:"time"`
	Sign string          `json:"sign"`
}

type probeAccountSession struct {
	view          ProbeAccountView
	aID, verifier string
	qr            probeAccountQR
	polls         int
	candidate     probeWorkerCredentials // RAM only until both grants are verified.
	timer         *time.Timer
}

type probeAccountLogin struct {
	session                       *probeAccountSession
	last                          time.Time
	qrBase, passportBase, webBase string
	appID                         string
	now                           func() time.Time
}

func newProbeAccountLogin() *probeAccountLogin {
	return &probeAccountLogin{qrBase: "https://qrcodeapi.115.com", passportBase: "https://passportapi.115.com", webBase: "https://webapi.115.com", appID: "100197303", now: time.Now}
}

func probeAccountPending(phase string) bool {
	switch phase {
	case "cookie_wait", "open_prepare", "open_confirm", "open_token":
		return true
	}
	return false
}

func (n *ProbeNode) accountActive() bool {
	s := n.login.session
	if s == nil || !probeAccountPending(s.view.Phase) {
		return false
	}
	if !n.login.now().Before(s.view.ExpiresAt) {
		n.failAccount(errors.New("授权已过期，请重新开始"))
		return false
	}
	return true
}

func (n *ProbeNode) accountView() (ProbeAccountView, error) {
	view := ProbeAccountView{Phase: "logged_out"}
	credentials, err := readProbeWorkerCredentials(n.credentials)
	if err == nil {
		if err := validateProbeWorkerCookie(credentials.Cookie, credentials.UserID); err != nil {
			return view, err
		}
		view.Configured, view.AccountID, view.Phase = true, credentials.UserID, "ready"
	} else if _, statErr := os.Lstat(n.credentials); !errors.Is(statErr, os.ErrNotExist) {
		return view, err
	}
	if s := n.login.session; s != nil {
		n.accountActive() // Expire partial grants before exposing local state.
		current := s.view
		current.Configured = view.Configured
		current.AccountID = view.AccountID
		if current.Phase == "ready" && !view.Configured {
			current.Phase = "logged_out"
		}
		return current, nil
	}
	return view, nil
}

func (n *ProbeNode) failAccount(err error) {
	s := n.login.session
	if s == nil {
		return
	}
	s.view.Phase, s.view.Error, s.view.Image = "failed", safeMediaProbeQueueError(err), ""
	s.view.PendingAccountID = ""
	s.clearSecrets()
}

func (s *probeAccountSession) clearSecrets() {
	if s.timer != nil {
		s.timer.Stop()
	}
	s.candidate = probeWorkerCredentials{}
	s.verifier = ""
	s.qr = probeAccountQR{}
}

func (n *ProbeNode) accountTake(ctx context.Context, poll bool) error {
	s := n.login.session
	if s == nil || !n.login.now().Before(s.view.ExpiresAt) {
		return errors.New("B 授权会话已过期")
	}
	if s.view.Requests >= probeAccountMaxRequests || poll && s.polls >= probeAccountMaxPolls {
		return errors.New("已达到本次 B 授权的请求或状态查询上限")
	}
	wait := n.wait
	if wait == nil {
		wait = waitMediaProbeQueue
	}
	if err := wait(ctx, n.login.last.Add(5*time.Second).Sub(n.login.now())); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !n.login.now().Before(s.view.ExpiresAt) {
		return errors.New("B 授权会话已过期")
	}
	s.view.Requests++
	if poll {
		s.polls++
	}
	n.login.last = n.login.now()
	return nil
}

// Fixed endpoints only. Errors never include upstream bodies, credentials or QR
// parameters. There is no redirect, refresh, retry, CookieJar or page warm-up.
func (n *ProbeNode) accountRequest(ctx context.Context, endpoint string, form url.Values, cookie, token string, poll, image bool) ([]byte, error) {
	method := http.MethodGet
	var body io.Reader
	if form != nil {
		method = http.MethodPost
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, errors.New("无法构造 B 授权请求")
	}
	req.Header.Set("User-Agent", probeWorkerShareUA)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if err := n.accountTake(ctx, poll); err != nil {
		return nil, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("B 授权连接失败，已停止且不重试")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("B 授权 HTTP %d，已停止", resp.StatusCode)
	}
	limit := int64(64 << 10)
	if image {
		limit = 256 << 10
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("B 授权响应读取失败或超过大小限制")
	}
	return data, nil
}

func (n *ProbeNode) accountJSON(ctx context.Context, endpoint string, form url.Values, cookie, token string, poll bool) (json.RawMessage, error) {
	data, err := n.accountRequest(ctx, endpoint, form, cookie, token, poll, false)
	if err != nil {
		return nil, err
	}
	data, err = decodeProbeAccountResponse(data)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || string(data) == "null" {
		return nil, errors.New("B 授权响应缺少数据")
	}
	return data, nil
}

func decodeProbeAccountResponse(data []byte) (json.RawMessage, error) {
	var result struct {
		State json.RawMessage  `json:"state"`
		Code  *probeWireNumber `json:"code"`
		Errno probeWireNumber  `json:"errno"`
		Data  json.RawMessage  `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.New("B 授权响应结构无效")
	}
	stateOK := string(result.State) == "true" || string(result.State) == "1" || len(result.State) == 0 && result.Code != nil && *result.Code == 0
	if !stateOK || result.Code != nil && *result.Code != 0 || result.Errno != 0 {
		return nil, errors.New("B 授权未确认成功，已停止；请在 115 中核对登录和授权状态")
	}
	return result.Data, nil
}

// Scan/confirm are acknowledgements; unlike token responses, data may be absent.
func (n *ProbeNode) accountAck(ctx context.Context, endpoint string) error {
	body, err := n.accountRequest(ctx, endpoint, nil, n.login.session.candidate.Cookie, "", false, false)
	if err != nil {
		return err
	}
	_, err = decodeProbeAccountResponse(body)
	return err
}

func parseProbeAccountQR(data []byte) (probeAccountQR, error) {
	var qr probeAccountQR
	if err := json.Unmarshal(data, &qr); err != nil || qr.UID == "" || len(qr.UID) > 256 || qr.Time <= 0 || qr.Sign == "" || len(qr.Sign) > 256 {
		return qr, errors.New("B 授权二维码响应无效")
	}
	return qr, nil
}

func (n *ProbeNode) accountQR(ctx context.Context) error {
	s := n.login.session
	endpoint := n.login.qrBase + "/api/1.0/web/1.0/token/"
	data, err := n.accountJSON(ctx, endpoint, nil, "", "", false)
	if err != nil {
		return err
	}
	qr, err := parseProbeAccountQR(data)
	if err != nil {
		return err
	}
	image, err := n.accountRequest(ctx, n.login.qrBase+"/api/1.0/web/1.0/qrcode?uid="+url.QueryEscape(qr.UID), nil, "", "", false, true)
	if err != nil {
		return err
	}
	info, err := png.DecodeConfig(bytes.NewReader(image))
	if err != nil || info.Width < 1 || info.Height < 1 || info.Width > 1024 || info.Height > 1024 {
		return errors.New("B 授权二维码不是有效的受限 PNG 图片")
	}
	s.qr, s.polls = qr, 0
	s.view.Phase = "cookie_wait"
	s.view.Image = "data:image/png;base64," + base64.StdEncoding.EncodeToString(image)
	return nil
}

func (n *ProbeNode) startAccount(ctx context.Context, aID string) (*ProbeAccountView, error) {
	if !probeDecimalID(aID) || aID == "0" {
		return nil, errors.New("无法确认现有 A 账号身份")
	}
	if n.accountActive() {
		return nil, errors.New("已有 B 授权会话，请完成或取消后再开始")
	}
	previous, err := n.accountView()
	if err != nil {
		return nil, err
	}
	n.login.session = &probeAccountSession{aID: aID, view: ProbeAccountView{SessionID: uuid.NewString(), Phase: "cookie_wait", ExpiresAt: n.login.now().Add(5 * time.Minute)}}
	session := n.login.session
	session.view.Configured, session.view.AccountID = previous.Configured, previous.AccountID
	session.timer = time.AfterFunc(5*time.Minute, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.login.session == session {
			n.accountActive()
		}
	})
	work, cancel := context.WithDeadline(ctx, n.login.session.view.ExpiresAt)
	defer cancel()
	if err := n.accountQR(work); err != nil {
		n.failAccount(err)
		return nil, err
	}
	view := n.login.session.view
	return &view, nil
}

func (n *ProbeNode) pollAccount(ctx context.Context, id string) (*ProbeAccountView, error) {
	s := n.login.session
	if s == nil || s.view.SessionID != id {
		return nil, errors.New("B 授权会话不存在或已被替换")
	}
	if !n.accountActive() {
		view, err := n.accountView()
		return &view, err
	}
	work, cancel := context.WithDeadline(ctx, s.view.ExpiresAt)
	defer cancel()
	// Advance one bounded step per UI request. Only cookie_wait polls 115.
	// This keeps each request cancellable and avoids a second human QR scan.
	if s.view.Phase != "cookie_wait" {
		var err error
		switch s.view.Phase {
		case "open_prepare":
			err = n.prepareAccountOpen(work)
		case "open_confirm":
			query := url.Values{"key": {s.qr.UID}, "uid": {s.qr.UID}, "client": {"0"}}
			err = n.accountAck(work, n.login.qrBase+"/api/2.0/slogin.php?"+query.Encode())
			if err == nil {
				s.view.Phase = "open_token"
			}
		case "open_token":
			err = n.finishAccountToken(work)
		}
		if err != nil {
			n.failAccount(err)
			return nil, err
		}
		view := s.view
		return &view, nil
	}
	query := url.Values{"uid": {s.qr.UID}, "time": {strconv.FormatInt(int64(s.qr.Time), 10)}, "sign": {s.qr.Sign}}
	data, err := n.accountJSON(work, n.login.qrBase+"/get/status/?"+query.Encode(), nil, "", "", true)
	if err != nil {
		n.failAccount(err)
		return nil, err
	}
	var state struct {
		Status *int `json:"status"`
	}
	if decodeErr := json.Unmarshal(data, &state); decodeErr != nil || state.Status == nil {
		err = errors.New("B 扫码状态响应无效")
	} else {
		switch *state.Status {
		case 0, 1:
			view := s.view
			return &view, nil
		case 2:
			err = n.finishAccountCookie(work)
		case -1, -2:
			err = errors.New("B 授权二维码已过期或已取消")
		default:
			err = errors.New("B 授权二维码返回未知状态")
		}
	}
	if err != nil {
		n.failAccount(err)
		return nil, err
	}
	view := s.view
	return &view, nil
}

func (n *ProbeNode) finishAccountCookie(ctx context.Context) error {
	s := n.login.session
	data, err := n.accountJSON(ctx, n.login.passportBase+"/app/1.0/web/1.0/login/qrcode/", url.Values{"account": {s.qr.UID}, "app": {"web"}}, "", "", false)
	if err != nil {
		return err
	}
	var result struct {
		Cookie map[string]string `json:"cookie"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return errors.New("B 登录凭据响应结构无效")
	}
	keys := []string{}
	for key := range result.Cookie {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := []string{}
	for _, key := range keys {
		if strings.ContainsAny(key+result.Cookie[key], "\r\n\x00;") {
			return errors.New("B 登录凭据格式无效")
		}
		parts = append(parts, key+"="+result.Cookie[key])
	}
	cookie := strings.Join(parts, "; ")
	id := probeCookieAccount(cookie)
	if !probeDecimalID(id) || id == "0" || id == s.aID {
		return errors.New("扫码账号是 A 或身份无效，请改用独立 B 账号")
	}
	if err := validateProbeWorkerCookie(cookie, id); err != nil {
		return err
	}
	if len(cookie) > 16384 {
		return errors.New("B 登录凭据超过大小限制")
	}
	// uploadinfo has a top-level user_id (unlike OpenAPI's data.user_id).
	body, err := n.accountRequest(ctx, n.login.webBase+"/files/uploadinfo", nil, cookie, "", false, false)
	if err != nil {
		return err
	}
	var actual struct {
		ID    probeWireNumber `json:"user_id"`
		Errno probeWireNumber `json:"errno"`
		State json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(body, &actual); err != nil || strconv.FormatInt(int64(actual.ID), 10) != id || actual.Errno != 0 || len(actual.State) > 0 && string(actual.State) != "true" && string(actual.State) != "1" {
		return errors.New("B 网页登录的实际账号核验失败")
	}
	s.candidate = probeWorkerCredentials{UserID: id, Cookie: cookie}
	s.view.PendingAccountID = id
	s.view.Phase, s.view.Image, s.qr = "open_prepare", "", probeAccountQR{}
	return nil
}

func (n *ProbeNode) prepareAccountOpen(ctx context.Context) error {
	s := n.login.session
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	s.verifier = hex.EncodeToString(secret)
	digest := sha256.Sum256([]byte(s.verifier))
	form := url.Values{"client_id": {n.login.appID}, "code_challenge": {base64.StdEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"sha256"}}
	data, err := n.accountJSON(ctx, n.login.qrBase+"/open/authDeviceCode", form, "", "", false)
	if err != nil {
		return err
	}
	qr, err := parseProbeAccountQR(data)
	if err != nil {
		return err
	}
	s.qr = qr
	if err := n.accountAck(ctx, n.login.qrBase+"/api/2.0/prompt.php?"+url.Values{"uid": {qr.UID}}.Encode()); err != nil {
		return err
	}
	s.view.Phase = "open_confirm"
	return nil
}

func (n *ProbeNode) finishAccountToken(ctx context.Context) error {
	s := n.login.session
	data, err := n.accountJSON(ctx, n.login.qrBase+"/open/deviceCodeToToken", url.Values{"uid": {s.qr.UID}, "code_verifier": {s.verifier}}, "", "", false)
	if err != nil {
		return err
	}
	var result struct {
		Access  string          `json:"access_token"`
		Refresh string          `json:"refresh_token"`
		Expires probeWireNumber `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.Access == "" || result.Refresh == "" || len(result.Access) > 8192 || len(result.Refresh) > 8192 || strings.ContainsAny(result.Access+result.Refresh, "\r\n\x00") || result.Expires < 60 || result.Expires > 31*86400 {
		return errors.New("B 开放平台授权响应缺少有效令牌或有效期")
	}
	identity, err := n.accountJSON(ctx, n.base+"/open/user/info", nil, "", result.Access, false)
	if err != nil {
		return err
	}
	var actual struct {
		ID probeWireNumber `json:"user_id"`
	}
	if err := json.Unmarshal(identity, &actual); err != nil || strconv.FormatInt(int64(actual.ID), 10) != s.candidate.UserID || s.candidate.UserID == s.aID {
		return errors.New("B 登录与开放平台授权的账号不一致，未保存任何新凭据")
	}
	s.candidate.AccessToken, s.candidate.RefreshToken, s.candidate.ExpiresAt = result.Access, result.Refresh, n.login.now().Unix()+int64(result.Expires)
	owner, err := os.OpenFile(n.credentials+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("B 凭据正被任务占用，未替换登录")
	}
	owner.Close()
	defer os.Remove(n.credentials + ".lock")
	if err := WriteProbeWorkerJSON(n.credentials, s.candidate); err != nil {
		return err
	}
	s.view.Phase, s.view.Image, s.view.Configured = "ready", "", true
	s.view.AccountID, s.view.PendingAccountID = s.candidate.UserID, ""
	s.clearSecrets()
	return nil
}

func (n *ProbeNode) serveAccount(w http.ResponseWriter, r *http.Request, respond func(any, error)) {
	if !n.mu.TryLock() {
		respond(nil, errors.New("节点正在执行任务，暂不能修改 B 登录"))
		return
	}
	defer n.mu.Unlock()
	if r.Method == http.MethodGet && r.URL.Path == "/v1/account" {
		view, err := n.accountView()
		respond(view, err)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var req struct {
		AAccountID string `json:"a_account_id"`
		SessionID  string `json:"session_id"`
		Confirmed  bool   `json:"confirmed"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		respond(nil, errors.New("B 授权请求结构无效"))
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		respond(nil, errors.New("B 授权请求包含额外内容"))
		return
	}
	switch r.URL.Path {
	case "/v1/account/start":
		if !req.Confirmed {
			respond(nil, errors.New("请先确认本次有限 B 授权范围"))
			return
		}
		view, err := n.startAccount(r.Context(), req.AAccountID)
		respond(view, err)
	case "/v1/account/poll":
		view, err := n.pollAccount(r.Context(), req.SessionID)
		respond(view, err)
	case "/v1/account/cancel":
		if n.login.session == nil || n.login.session.view.SessionID != req.SessionID {
			respond(nil, errors.New("B 授权会话不匹配"))
			return
		}
		n.login.session.clearSecrets()
		n.login.session = nil
		view, err := n.accountView()
		respond(view, err)
	case "/v1/account/logout":
		if !req.Confirmed {
			respond(nil, errors.New("请确认退出 B 登录"))
			return
		}
		owner, err := os.OpenFile(n.credentials+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			respond(nil, errors.New("B 凭据已被占用"))
			return
		}
		owner.Close()
		defer os.Remove(n.credentials + ".lock")
		if err := os.Remove(n.credentials); err != nil && !errors.Is(err, os.ErrNotExist) {
			respond(nil, err)
			return
		}
		if n.login.session != nil {
			n.login.session.clearSecrets()
		}
		n.login.session = nil
		respond(ProbeAccountView{Phase: "logged_out"}, nil)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}
