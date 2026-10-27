package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type mediaProbeFailure struct {
	Kind         string
	HTTPStatus   int
	ProviderCode string
}

var (
	probeHTTPStatusPattern   = regexp.MustCompile(`(?i)\b(?:http(?:\s+(?:error|status(?:\s+code)?))?|server returned)\s*[:=]?\s*(\d{3})\b`)
	probeDeniedCodePattern   = regexp.MustCompile(`(?i)\bdenied\s*\((401|403)\)`)
	probeFrequentCodePattern = regexp.MustCompile(`(?:^|[\s:="<(])40140117(?:$|[\s",}>)])`)
	probeRiskPattern         = regexp.MustCompile(`(?i)风控|触发安全验证|请求过于频繁|访问过于频繁|操作过于频繁|操作频繁|risk[ _-]?control`)
	probeSecretHeaderPattern = regexp.MustCompile(`(?im)\b(cookie|set-cookie|authorization|proxy-authorization)\s*:\s*[^\r\n]+`)
	probeSecretValuePattern  = regexp.MustCompile(`(?i)(["']?(?:cookie|set-cookie|authorization|proxy-authorization|access_token|refresh_token|token|password|passwd|client_secret|secret|api_key|sign|signature|UID|CID|SEID|KID)["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;}\]]+)`)
)

func classifyMediaProbeFailure(err error) mediaProbeFailure {
	if err == nil {
		return mediaProbeFailure{}
	}
	var commandErr *mediaProbeCommandError
	if errors.As(err, &commandErr) {
		return commandErr.failure
	}
	text := redactMediaProbeError(err.Error())
	failure := mediaProbeFailure{Kind: "probe_failed"}
	var response interface{ ResponseCode() int }
	if errors.As(err, &response) {
		failure.ProviderCode = strconv.Itoa(response.ResponseCode())
	}
	if match := probeDeniedCodePattern.FindStringSubmatch(text); failure.ProviderCode == "" && len(match) > 1 {
		failure.ProviderCode = match[1]
	}
	if match := probeHTTPStatusPattern.FindStringSubmatch(text); len(match) > 1 {
		failure.HTTPStatus, _ = strconv.Atoi(match[1])
	}
	switch {
	case failure.ProviderCode == "40140117" || probeFrequentCodePattern.MatchString(text):
		failure.Kind, failure.ProviderCode = "risk_control", "40140117"
	case probeRiskPattern.MatchString(text):
		failure.Kind = "risk_control"
	case failure.HTTPStatus == 429 || failure.ProviderCode == "429" || strings.Contains(strings.ToLower(text), "too many requests"):
		failure.Kind = "rate_limited"
	case failure.HTTPStatus == 403 || failure.ProviderCode == "403":
		failure.Kind = "access_denied"
	case failure.HTTPStatus == 401 || failure.ProviderCode == "401":
		failure.Kind = "unauthorized"
	case errors.Is(err, context.DeadlineExceeded):
		failure.Kind = "timeout"
	case errors.Is(err, context.Canceled):
		failure.Kind = "canceled"
	}
	return failure
}

// Keep the bounded, redacted ffprobe diagnostics, including HTTP errors that
// would otherwise disappear behind exec.ExitError's "exit status" message.
func mediaProbeCommandFailure(ctx context.Context, operation string, err error) error {
	detail := err.Error()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		detail += ": " + string(exitErr.Stderr)
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			detail = "探测超时: " + detail
		}
	}
	failure := classifyMediaProbeFailure(fmt.Errorf("%s: %w", detail, err))
	return &mediaProbeCommandError{cause: err, detail: operation + " failed: " + safeMediaProbeQueueError(errors.New(detail)), failure: failure}
}

type mediaProbeCommandError struct {
	cause   error
	detail  string
	failure mediaProbeFailure
}

func (e *mediaProbeCommandError) Error() string { return e.detail }
func (e *mediaProbeCommandError) Unwrap() error { return e.cause }

func redactMediaProbeError(text string) string {
	text = mediaProbeURLPattern.ReplaceAllString(text, "[媒体地址已隐藏]")
	text = probeSecretHeaderPattern.ReplaceAllString(text, "$1: [凭据已隐藏]")
	return probeSecretValuePattern.ReplaceAllString(text, "$1[凭据已隐藏]")
}
