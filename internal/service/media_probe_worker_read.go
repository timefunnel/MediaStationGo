package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type probeWorkerReadGuard struct {
	mu          sync.Mutex
	ctx         context.Context
	gate        *probeWorkerGate
	client      *http.Client
	file        probeWorkerFile
	url, route  string
	validateURL func(string) error
	requests    int
	bytes       int64
	err         error
}

func (g *probeWorkerReadGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != g.route || r.URL.RawQuery != "" {
		w.WriteHeader(404)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		w.WriteHeader(429)
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.FormatInt(g.file.Size, 10))
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(200)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	start, end, err := probeWorkerRange(r.Header.Get("Range"), g.file.Size)
	if err != nil {
		g.err = err
		w.WriteHeader(416)
		return
	}
	g.gate.mu.Lock()
	remaining := min(g.gate.limits.MediaBytes-g.gate.stats.MediaBytes, g.gate.limits.MediaBytesFile-g.bytes)
	g.gate.mu.Unlock()
	if remaining <= 0 {
		g.err = errProbeWorkerBudget
		w.WriteHeader(429)
		return
	}
	end = start + min(end-start+1, remaining) - 1
	current := g.url
	ctx, cancel := context.WithCancel(g.ctx)
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	defer cancel()
	for {
		if err := g.validateURL(current); err != nil {
			g.err = err
			w.WriteHeader(502)
			return
		}
		if g.requests >= g.gate.limits.MediaRequestsFile {
			g.err = errProbeWorkerBudget
			w.WriteHeader(429)
			return
		}
		if err := g.gate.take(ctx, "media"); err != nil {
			g.err = err
			w.WriteHeader(429)
			return
		}
		g.requests++
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			g.err = errors.New("无法构造媒体读取请求")
			w.WriteHeader(502)
			return
		}
		req.Header.Set("User-Agent", probeWorkerUA)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := g.client.Do(req)
		if err != nil {
			if r.Context().Err() == nil && g.ctx.Err() == nil {
				g.err = errors.New("B 媒体读取连接失败")
			}
			w.WriteHeader(502)
			return
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
			resp.Body.Close()
			base, parseErr := url.Parse(current)
			next, nextErr := url.Parse(location)
			if parseErr != nil || nextErr != nil || location == "" {
				g.err = errors.New("媒体重定向无效")
				w.WriteHeader(502)
				return
			}
			current = base.ResolveReference(next).String()
			continue
		}
		defer resp.Body.Close()
		actualEnd := end
		if resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity" {
			g.err = errors.New("媒体响应使用了非预期内容编码")
			w.WriteHeader(502)
			return
		}
		switch resp.StatusCode {
		case http.StatusPartialContent:
			var from, to, total int64
			if n, _ := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &from, &to, &total); n != 3 || from != start || to < from || to > end || total != g.file.Size {
				g.err = errors.New("媒体 Range 或文件大小校验失败")
				w.WriteHeader(502)
				return
			}
			actualEnd = to
		case http.StatusOK:
			if start != 0 || end != g.file.Size-1 || resp.ContentLength != g.file.Size {
				g.err = errors.New("上游忽略了有限 Range，未读取完整大文件")
				w.WriteHeader(502)
				return
			}
		default:
			g.err = fmt.Errorf("B 媒体 HTTP %d，已停止", resp.StatusCode)
			w.WriteHeader(502)
			return
		}
		expected := actualEnd - start + 1
		if resp.ContentLength >= 0 && resp.ContentLength != expected {
			g.err = errors.New("媒体响应长度与 Range 不一致")
			w.WriteHeader(502)
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(expected, 10))
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "application/octet-stream")
		if r.Header.Get("Range") != "" || start != 0 || actualEnd != g.file.Size-1 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, actualEnd, g.file.Size))
			w.WriteHeader(206)
		} else {
			w.WriteHeader(200)
		}
		buffer := make([]byte, 8192)
		for expected > 0 {
			n, readErr := resp.Body.Read(buffer[:min(int64(len(buffer)), expected)])
			if n > 0 {
				g.gate.mu.Lock()
				g.gate.stats.MediaBytes += int64(n)
				g.gate.mu.Unlock()
				g.bytes += int64(n)
				expected -= int64(n)
				if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
					return
				}
				if err := http.NewResponseController(w).Flush(); err != nil {
					return
				}
			}
			if readErr != nil {
				if expected > 0 && r.Context().Err() == nil && g.ctx.Err() == nil {
					g.err = errors.New("B 媒体响应提前结束")
				}
				return
			}
		}
		return
	}
}

func probeWorkerRange(value string, size int64) (int64, int64, error) {
	if size <= 0 {
		return 0, 0, errors.New("媒体大小无效")
	}
	if value == "" {
		return 0, size - 1, nil
	}
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, 0, errors.New("不支持的媒体 Range")
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "bytes="), "-", 2)
	if len(parts) != 2 {
		return 0, 0, errors.New("媒体 Range 格式错误")
	}
	if parts[0] == "" {
		suffix, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, errors.New("媒体 Range 后缀无效")
		}
		return max(int64(0), size-suffix), size - 1, nil
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, errors.New("媒体 Range 起点无效")
	}
	end := size - 1
	if parts[1] != "" {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return 0, 0, errors.New("媒体 Range 终点无效")
		}
	}
	return start, min(end, size-1), nil
}

type probeWorkerOutput struct {
	bytes.Buffer
	limit int
}

func (b *probeWorkerOutput) Write(p []byte) (int, error) {
	limit := b.limit
	if limit == 0 {
		limit = 8 << 20
	}
	if b.Len()+len(p) > limit {
		return 0, fmt.Errorf("ffprobe 输出超过 %d 字节", limit)
	}
	return b.Buffer.Write(p)
}

type probeWorkerProber func(context.Context, string) (*ProbeResult, error)

func newProbeWorkerProber(binary string) (probeWorkerProber, error) {
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, errors.New("找不到指定 ffprobe 程序")
	}
	return func(ctx context.Context, localURL string) (*ProbeResult, error) {
		args := []string{"-v", "error", "-protocol_whitelist", "http,tcp", "-format_whitelist", "mov,matroska,avi,mpegts,mpeg,mpegvideo,hevc,h264,flv", "-print_format", "json", "-show_format", "-show_streams", localURL}
		cmd := exec.CommandContext(ctx, resolved, args...) // #nosec G204 -- explicit executable; media URL is a private loopback endpoint.
		var output probeWorkerOutput
		diagnostic := probeWorkerOutput{limit: 8192}
		cmd.Stdout, cmd.Stderr = &output, &diagnostic
		for _, item := range os.Environ() {
			name := strings.ToLower(strings.SplitN(item, "=", 2)[0])
			if name != "http_proxy" && name != "https_proxy" && name != "all_proxy" && name != "no_proxy" {
				cmd.Env = append(cmd.Env, item)
			}
		}
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			code := -1
			if cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			}
			return nil, fmt.Errorf("ffprobe 单次探测失败（exit %d）: %s", code, safeMediaProbeQueueError(errors.New(diagnostic.String())))
		}
		result, err := parseProbeJSON(output.Bytes())
		if err != nil {
			return nil, errors.New("ffprobe JSON 无效")
		}
		if err := validateExchangeProbe(result); err != nil {
			return nil, err
		}
		result.SubtitleStreams = nil
		return result, nil
	}, nil
}

func runProbeWorkerRead(ctx context.Context, file probeWorkerFile, rawURL string, gate *probeWorkerGate, client *http.Client, validate func(string) error, probe probeWorkerProber) (*ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(gate.limits.ProbeSeconds)*time.Second)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		cancel()
		return nil, errors.New("无法启动私有媒体读取入口")
	}
	guard := &probeWorkerReadGuard{ctx: ctx, gate: gate, client: client, file: file, url: rawURL, route: "/" + uuid.NewString(), validateURL: validate}
	server := &http.Server{Handler: guard, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	result, probeErr := probe(ctx, "http://"+listener.Addr().String()+guard.route)
	probeContextErr := ctx.Err()
	cancel()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	closeErr := server.Shutdown(closeCtx)
	closeCancel()
	if closeErr != nil {
		server.Close()
	}
	serveErr := <-done
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return nil, errors.New("媒体读取入口异常退出")
	}
	guard.mu.Lock()
	readErr := guard.err
	guard.mu.Unlock()
	if probeContextErr != nil {
		return nil, probeContextErr
	}
	if errors.Is(readErr, context.Canceled) && probeErr == nil {
		readErr = nil
	}
	if readErr != nil {
		return nil, readErr
	}
	if probeErr != nil {
		return nil, probeErr
	}
	if closeErr != nil {
		return nil, errors.New("媒体读取入口未能正常停止")
	}
	return result, nil
}
