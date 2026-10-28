package service

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	mkvProbeMaxRequests = 6
	mkvProbeMaxBytes    = 4 << 20
	mkvProbeReadAhead   = 64 << 10
	mkvProbeClusterMax  = 2 << 20
)

type mkvProbeRange struct {
	start int64
	data  []byte
}

// Each GET has a finite Range, no redirects, no transport retries and no
// compressed response. Limits include unsuccessful requests and error bodies.
type mkvProbeReader struct {
	ctx                           context.Context
	client                        *http.Client
	url                           string
	headers                       map[string]string
	size, expectedSize, readBytes int64
	requests                      int
	etag, modified                string
	lastEnd                       time.Time
	interval                      time.Duration
	cache                         []mkvProbeRange
}

func newMKVProbeReader(ctx context.Context, rawURL string, headers map[string]string, expectedSize int64) *mkvProbeReader {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true // A reused connection can silently retry GET.
	transport.DisableCompression = true
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	transport.ResponseHeaderTimeout = 10 * time.Second
	return &mkvProbeReader{ctx: ctx, url: rawURL, headers: headers, expectedSize: expectedSize, interval: time.Second,
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (r *mkvProbeReader) readAt(pos, count int64) ([]byte, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	if pos < 0 || count <= 0 || count > mkvProbeMaxBytes || pos > int64(^uint64(0)>>1)-count || (r.size > 0 && (pos >= r.size || count > r.size-pos)) {
		return nil, errors.New("MKV Range: 无效或越界的读取范围")
	}
	for _, cached := range r.cache {
		if pos >= cached.start && pos-cached.start <= int64(len(cached.data))-count {
			return cached.data[pos-cached.start : pos-cached.start+count], nil
		}
	}
	fetch := max(count, int64(mkvProbeReadAhead))
	if r.size > 0 {
		fetch = min(fetch, r.size-pos)
	}
	remaining := int64(mkvProbeMaxBytes) - r.readBytes
	if count > remaining || r.requests >= mkvProbeMaxRequests {
		return nil, errors.New("MKV Range: 已达到探测预算（最多 6 次请求 / 4 MiB），未重试或全量读取")
	}
	fetch = min(fetch, remaining)
	if !r.lastEnd.IsZero() {
		if err := waitMediaProbeQueue(r.ctx, max(time.Duration(0), r.interval-time.Since(r.lastEnd))); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return nil, errors.New("MKV Range: 无效的媒体地址")
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, errors.New("MKV Range: 只支持 HTTP(S)")
	}
	for key, value := range r.headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", pos, pos+fetch-1))
	req.Header.Set("Accept-Encoding", "identity")
	if r.requests > 0 {
		if r.etag != "" && !strings.HasPrefix(r.etag, "W/") {
			req.Header.Set("If-Range", r.etag)
		} else if r.modified != "" {
			req.Header.Set("If-Range", r.modified)
		}
	}
	r.requests++
	defer func() { r.lastEnd = time.Now() }()
	resp, err := r.client.Do(req)
	if err != nil {
		// Do not include a signed URL from net/http's *url.Error.
		if r.ctx.Err() != nil {
			return nil, r.ctx.Err()
		}
		return nil, fmt.Errorf("MKV Range 请求失败: %s", safeMediaProbeQueueError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		if resp.StatusCode == http.StatusOK || resp.StatusCode < 400 {
			return nil, fmt.Errorf("MKV Range: HTTP status %d，上游未提供所需范围；已停止读取", resp.StatusCode)
		}
		// Preserve provider risk codes for the existing alert classifier; bound
		// and redact the body before it can enter a task or application log.
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, min(int64(4096), remaining)))
		r.readBytes += int64(len(body))
		failure := errors.Join(fmt.Errorf("MKV Range: HTTP status %d: %s", resp.StatusCode, redactMediaProbeError(string(body))), readErr)
		// Classify the complete bounded diagnostic before the display limit can
		// hide a provider code near the end of a long response.
		return nil, &mediaProbeCommandError{cause: failure, detail: safeMediaProbeQueueError(failure), failure: classifyMediaProbeFailure(failure)}
	}
	start, end, total, err := parseMKVContentRange(resp.Header.Get("Content-Range"))
	if err != nil || start != pos || end != min(pos+fetch-1, total-1) || total <= end || end < start || (r.size > 0 && total != r.size) || (r.expectedSize > 0 && total != r.expectedSize) {
		return nil, errors.New("MKV Range: Content-Range 或源文件大小不一致，结果未使用")
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return nil, errors.New("MKV Range: 上游压缩了范围响应，无法验证文件偏移")
	}
	etag, modified := resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	if r.size > 0 && (etag != r.etag || modified != r.modified) {
		return nil, errors.New("MKV Range: 源文件响应标记已变化，结果未使用")
	}
	r.size, r.etag, r.modified = total, etag, modified
	want := end - start + 1
	if want < count || (resp.ContentLength >= 0 && resp.ContentLength != want) {
		return nil, errors.New("MKV Range: 响应长度与范围不一致")
	}
	body := make([]byte, want)
	n, err := io.ReadFull(resp.Body, body)
	r.readBytes += int64(n)
	if err != nil {
		return nil, fmt.Errorf("MKV Range: 响应未完整读取: %w", err)
	}
	// Read no extra byte: range, Content-Length (if available), file identity,
	// and the application byte cap have already been checked.
	r.cache = append(r.cache, mkvProbeRange{pos, body})
	return body[:count], nil
}

func parseMKVContentRange(value string) (int64, int64, int64, error) {
	span, totalText, ok := strings.Cut(strings.TrimPrefix(value, "bytes "), "/")
	startText, endText, spanOK := strings.Cut(span, "-")
	if !strings.HasPrefix(value, "bytes ") || !ok || !spanOK {
		return 0, 0, 0, errors.New("invalid Content-Range")
	}
	start, errA := strconv.ParseInt(startText, 10, 64)
	end, errB := strconv.ParseInt(endText, 10, 64)
	total, errC := strconv.ParseInt(totalText, 10, 64)
	if errA != nil || errB != nil || errC != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, errors.New("invalid Content-Range")
	}
	return start, end, total, nil
}
