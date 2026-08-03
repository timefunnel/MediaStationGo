package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/service/cloud"
)

type cloudResolveCacheEntry struct {
	link         *cloud.DirectLink
	expiresAt    time.Time
	staleUntil   time.Time
	refreshAfter time.Time
	hits         int
	lastHit      time.Time
}

type cloudResolveCall struct {
	done       chan struct{}
	generation uint64
	link       *cloud.DirectLink
	err        error
}

const (
	cloudResolveHotHitThreshold      = 3
	cloudResolveBackgroundRefreshMax = 30 * time.Second
	cloudResolveColdMaxDuration      = 2 * time.Second
	cloudResolveRefreshMinInterval   = 30 * time.Second
	cloudResolveRetryAttempts        = 2
	cloudResolveRetryDelay           = 300 * time.Millisecond
	cloudResolveRetryMaxAttemptDur   = 2 * time.Second
)

// CloudResolve resolves a cloud file reference to a direct link.
//
// clientUA is the User-Agent of the playback client that will follow the 302
// redirect. Some provider CDN links are bound to the UA used to request them,
// so we resolve with the client's own UA. When clientUA is empty the provider's
// default UA is used.
func (s *StorageConfigService) CloudResolve(ctx context.Context, typ, fileRef, clientUA string) (*cloud.DirectLink, error) {
	if s == nil {
		return nil, errors.New("storage config service unavailable")
	}
	cacheKey := s.resolveCacheKey(typ, fileRef, clientUA)
	if link, ok, refresh := s.cachedResolve(cacheKey, typ); ok {
		if refresh {
			s.refreshResolveInBackground(cacheKey, typ, fileRef, clientUA)
		}
		return link, nil
	}
	call, owner := s.beginResolve(cacheKey, typ)
	if owner {
		s.runResolve(cacheKey, typ, fileRef, clientUA, call, cloudResolveColdMaxDuration)
	}
	select {
	case <-call.done:
		if call.err != nil {
			return nil, call.err
		}
		return cloneDirectLink(call.link), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *StorageConfigService) resolveCacheKey(typ, fileRef, clientUA string) string {
	return strings.TrimSpace(typ) + "\x00" + strings.TrimSpace(fileRef) + "\x00" + strings.TrimSpace(clientUA)
}

func (s *StorageConfigService) cachedResolve(key, typ string) (*cloud.DirectLink, bool, bool) {
	s.resolveMu.Lock()
	defer s.resolveMu.Unlock()
	if s.resolveCache == nil {
		s.resolveCache = make(map[string]cloudResolveCacheEntry)
		return nil, false, false
	}
	entry, ok := s.resolveCache[key]
	now := time.Now()
	if !ok {
		return nil, false, false
	}
	if entry.staleUntil.IsZero() {
		entry.staleUntil = entry.expiresAt
	}
	if now.After(entry.staleUntil) {
		if ok {
			delete(s.resolveCache, key)
		}
		return nil, false, false
	}
	entry.hits++
	entry.lastHit = now
	fresh := !now.After(entry.expiresAt)
	refreshWindow := cloudResolveHotRefreshWindow(cloudResolveCacheTTL(typ))
	shouldRefresh := !fresh || (entry.hits >= cloudResolveHotHitThreshold &&
		refreshWindow > 0 &&
		now.Add(refreshWindow).After(entry.expiresAt))
	if shouldRefresh && !entry.refreshAfter.IsZero() && now.Before(entry.refreshAfter) {
		shouldRefresh = false
	}
	if shouldRefresh {
		entry.refreshAfter = now.Add(cloudResolveRefreshMinInterval)
	}
	s.resolveCache[key] = entry
	return cloneDirectLink(entry.link), true, shouldRefresh
}

func (s *StorageConfigService) beginResolve(key, typ string) (*cloudResolveCall, bool) {
	s.resolveMu.Lock()
	defer s.resolveMu.Unlock()
	if s.resolveFlight == nil {
		s.resolveFlight = make(map[string]*cloudResolveCall)
	}
	if s.resolveGen == nil {
		s.resolveGen = make(map[string]uint64)
	}
	if call := s.resolveFlight[key]; call != nil {
		return call, false
	}
	call := &cloudResolveCall{
		done:       make(chan struct{}),
		generation: s.resolveGen[strings.TrimSpace(typ)],
	}
	s.resolveFlight[key] = call
	return call, true
}

func (s *StorageConfigService) runResolve(key, typ, fileRef, clientUA string, call *cloudResolveCall, maxDuration time.Duration) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), maxDuration)
		defer cancel()
		p, err := s.cloudProviderWithUA(ctx, typ, clientUA)
		if err != nil {
			s.completeResolve(key, typ, call, nil, err)
			return
		}
		link, err := s.resolveCloudDirectLinkWithRetry(ctx, p, fileRef)
		s.completeResolve(key, typ, call, link, err)
	}()
}

func (s *StorageConfigService) completeResolve(key, typ string, call *cloudResolveCall, link *cloud.DirectLink, resolveErr error) {
	typ = strings.TrimSpace(typ)
	ttl, staleTTL := cloudResolveCacheDurations(typ, link)
	if resolveErr != nil && s.log != nil {
		s.log.Debug("resolve cloud direct link failed", zap.String("provider", typ), zap.Error(resolveErr))
	}

	s.resolveMu.Lock()
	if s.resolveGen == nil {
		s.resolveGen = make(map[string]uint64)
	}
	if s.resolveGen[typ] != call.generation {
		call.err = fmt.Errorf("%s storage config changed", typ)
	} else if resolveErr != nil {
		call.err = resolveErr
	} else if link == nil || strings.TrimSpace(link.URL) == "" {
		call.err = errors.New("cloud resolve returned empty link")
	} else {
		call.link = cloneDirectLink(link)
		s.storeResolvedLinkLocked(key, link, ttl, staleTTL)
	}
	if current := s.resolveFlight[key]; current == call {
		delete(s.resolveFlight, key)
	}
	close(call.done)
	s.resolveMu.Unlock()
}

func (s *StorageConfigService) refreshResolveInBackground(key, typ, fileRef, clientUA string) {
	if s == nil {
		return
	}
	call, owner := s.beginResolve(key, typ)
	if !owner {
		return
	}
	s.runResolve(key, typ, fileRef, clientUA, call, cloudResolveBackgroundRefreshMax)
}

func (s *StorageConfigService) storeResolvedLinkLocked(key string, link *cloud.DirectLink, ttl, staleTTL time.Duration) {
	if link == nil || strings.TrimSpace(link.URL) == "" || ttl <= 0 {
		return
	}
	if s.resolveCache == nil {
		s.resolveCache = make(map[string]cloudResolveCacheEntry)
	}
	now := time.Now()
	hits := 0
	if existing, ok := s.resolveCache[key]; ok {
		hits = existing.hits
	}
	expiresAt := now.Add(ttl)
	staleUntil := now.Add(staleTTL)
	if staleUntil.Before(expiresAt) {
		staleUntil = expiresAt
	}
	s.resolveCache[key] = cloudResolveCacheEntry{link: cloneDirectLink(link), expiresAt: expiresAt, staleUntil: staleUntil, hits: hits, lastHit: now}
}

func cloudResolveHotRefreshWindow(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return 0
	}
	window := ttl / 4
	if window < 15*time.Second {
		window = 15 * time.Second
	}
	if window > 2*time.Minute {
		window = 2 * time.Minute
	}
	return window
}

func cloudResolveCacheTTL(typ string) time.Duration {
	switch typ {
	case cloud.Type115, cloud.TypeCloudDrive2, cloud.TypeOpenList:
		return 2 * time.Minute
	default:
		return 5 * time.Minute
	}
}

func cloudResolveCacheDurations(typ string, link *cloud.DirectLink) (time.Duration, time.Duration) {
	ttl := cloudResolveCacheTTL(typ)
	staleTTL := ttl
	if link == nil {
		return ttl, staleTTL
	}
	expiry, ok := cloudResolve115DirectLinkExpiry(link.URL)
	if !ok {
		return ttl, staleTTL
	}
	untilExpiry := time.Until(expiry)
	if untilExpiry <= time.Minute {
		return ttl, staleTTL
	}
	usable := untilExpiry - time.Minute
	if usable > staleTTL {
		staleTTL = minDuration(usable, 50*time.Minute)
	}
	if staleTTL > ttl {
		ttl = minDuration(staleTTL, 30*time.Minute)
	}
	return ttl, staleTTL
}

func cloudResolve115DirectLinkExpiry(raw string) (time.Time, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || !strings.Contains(strings.ToLower(u.Host), "115cdn.net") {
		return time.Time{}, false
	}
	rawT := strings.TrimSpace(u.Query().Get("t"))
	if rawT == "" {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(rawT, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	if len(rawT) >= 13 {
		n /= 1000
	}
	expiry := time.Unix(n, 0)
	if !expiry.After(time.Now()) {
		return time.Time{}, false
	}
	return expiry, true
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (s *StorageConfigService) resolveCloudDirectLinkWithRetry(ctx context.Context, p cloud.Provider, fileRef string) (*cloud.DirectLink, error) {
	var lastErr error
	for attempt := 0; attempt < cloudResolveRetryAttempts; attempt++ {
		start := time.Now()
		link, err := p.Resolve(ctx, fileRef)
		if err == nil {
			return link, nil
		}
		lastErr = err
		if attempt == cloudResolveRetryAttempts-1 || ctx.Err() != nil || !cloudResolveErrorRetryable(err) || time.Since(start) > cloudResolveRetryMaxAttemptDur {
			break
		}
		timer := time.NewTimer(cloudResolveRetryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func cloudResolveErrorRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"timeout",
		"tls handshake",
		"connection reset",
		"connection refused",
		"temporary",
		"eof",
		"failed get link",
		"bad gateway",
		"returned http 5",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func cloneDirectLink(link *cloud.DirectLink) *cloud.DirectLink {
	if link == nil {
		return nil
	}
	out := &cloud.DirectLink{
		URL:     link.URL,
		Headers: make(map[string]string, len(link.Headers)),
		Proxy:   link.Proxy,
	}
	for k, v := range link.Headers {
		out.Headers[k] = v
	}
	return out
}

func (s *StorageConfigService) clearResolveCacheForType(typ string) {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return
	}
	prefix := typ + "\x00"
	s.resolveMu.Lock()
	defer s.resolveMu.Unlock()
	if s.resolveGen == nil {
		s.resolveGen = make(map[string]uint64)
	}
	s.resolveGen[typ]++
	for key := range s.resolveCache {
		if strings.HasPrefix(key, prefix) {
			delete(s.resolveCache, key)
		}
	}
	for key := range s.resolveFlight {
		if strings.HasPrefix(key, prefix) {
			delete(s.resolveFlight, key)
		}
	}
}

func (s *StorageConfigService) CloudResolveUncached(ctx context.Context, typ, fileRef, clientUA string) (*cloud.DirectLink, error) {
	p, err := s.cloudProviderWithUA(ctx, typ, clientUA)
	if err != nil {
		return nil, err
	}
	return p.Resolve(ctx, fileRef)
}

// cloudProviderWithUA builds a provider, overriding the request UA when a
// non-empty clientUA is supplied.
func (s *StorageConfigService) cloudProviderWithUA(ctx context.Context, typ, clientUA string) (cloud.Provider, error) {
	if !cloud.IsCloudType(typ) {
		return nil, fmt.Errorf("not a cloud provider: %q", typ)
	}
	view, err := s.Get(ctx, typ)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, fmt.Errorf("%s storage not configured", typ)
	}
	if !view.Enabled {
		return nil, fmt.Errorf("%s storage disabled", typ)
	}
	cfg := view.Config
	if strings.TrimSpace(clientUA) != "" {
		// Copy so we never mutate the cached view config.
		cp := make(map[string]any, len(cfg)+1)
		for k, v := range cfg {
			cp[k] = v
		}
		cp["ua"] = clientUA
		cfg = cp
	}
	return cloud.New(typ, cfg, s.clientForConfig(cfg))
}
