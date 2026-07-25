package service

import (
	"context"
	"time"
)

const (
	fd2PPVSessionCheckInterval = 30 * time.Minute
	fd2PPVSessionCheckTimeout  = 20 * time.Second
)

func (s *SchedulerService) jobCheckFD2PPVSession(ctx context.Context) error {
	if s == nil || s.adult == nil || !s.adult.FD2PPVEnabled() {
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, fd2PPVSessionCheckTimeout)
	defer cancel()
	return s.adult.CheckFD2PPVSession(checkCtx)
}
