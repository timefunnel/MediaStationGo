package service

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// directRemoteImageURL only offloads public TMDB CDN images. Sources that need
// request headers, local/cloud files, and exact height resizing stay proxied.
func (p *ImageProxy) directRemoteImageURL(raw string, variant imageVariantOptions) (string, bool) {
	if p == nil || p.cfg == nil || p.cfg.Cache.RemoteImageDelivery != "direct" || variant.maxHeight > 0 {
		return "", false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "image.tmdb.org") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "t" || parts[1] != "p" || parts[3] == "" {
		return "", false
	}
	size := parts[2]
	sourceWidth := 0
	if size != "original" {
		if !strings.HasPrefix(size, "w") {
			return "", false
		}
		sourceWidth, err = strconv.Atoi(strings.TrimPrefix(size, "w"))
		if err != nil || sourceWidth <= 0 {
			return "", false
		}
	}
	isSVG := strings.HasSuffix(strings.ToLower(parts[3]), ".svg")
	if isSVG && size != "original" {
		return "", false
	}
	// SVGs have no raster size variants. Leave their original CDN URL intact.
	if variant.maxWidth > 0 && !isSVG {
		target := variant.maxWidth
		if sourceWidth > 0 && sourceWidth < target {
			target = sourceWidth
		}
		width := 0
		for _, candidate := range []int{45, 92, 154, 185, 300, 342, 500, 780, 1280} {
			if candidate > target {
				break
			}
			width = candidate
		}
		if width == 0 {
			// No supported CDN size fits this bound; use the existing resizer.
			return "", false
		}
		parts[2] = "w" + strconv.Itoa(width)
		u.Path = "/" + strings.Join(parts, "/")
	}
	return u.String(), true
}

func (p *ImageProxy) serveDirectRemoteImage(w http.ResponseWriter, r *http.Request, raw string) bool {
	if r == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	target, ok := p.directRemoteImageURL(raw, imageVariantFromRequest(r))
	if !ok {
		return false
	}
	// A short redirect lifetime lets operators change delivery mode without
	// clients retaining the old routing decision for the image's full lifetime.
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusFound)
	return true
}
