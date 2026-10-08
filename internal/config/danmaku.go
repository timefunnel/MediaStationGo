package config

import (
	"fmt"
	"net/url"
	"strings"
)

func (c *DanmakuConfig) validate() error {
	c.URL = strings.TrimRight(strings.TrimSpace(c.URL), "/")
	c.Token = strings.TrimSpace(c.Token)
	if !c.Enabled {
		return nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("danmaku.url must be an absolute HTTP(S) service URL without credentials, query or fragment")
	}
	if len(c.Token) < 16 {
		return fmt.Errorf("danmaku.token must contain at least 16 characters")
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 120
	}
	return nil
}
