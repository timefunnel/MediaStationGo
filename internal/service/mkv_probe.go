package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"go.uber.org/zap"
)

func mediaProbeSourceIsMKV(media *model.Media) bool {
	for _, value := range []string{media.Path, media.RelativePath, media.STRMURL} {
		if _, ref, ok := parseCloudMediaPlaybackURL(value); ok {
			value = ref
		} else if parsed, err := url.Parse(value); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https" || parsed.Scheme == "cloud") {
			value = parsed.Path
		}
		if strings.EqualFold(path.Ext(strings.ReplaceAll(value, `\`, "/")), ".mkv") {
			return true
		}
	}
	return false
}

type mkvProbeSample struct {
	data         []byte
	duration     int
	tracks       []mkvProbeTrack
	skippedBytes int64
}

func (f *FFprobeService) probeRemoteMKV(ctx context.Context, rawURL string, headers map[string]string, size int64) (result *ProbeResult, err error) {
	if f == nil {
		return nil, errors.New("ffprobe service nil")
	}
	token, err := f.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer f.release(token)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	reader := newMKVProbeReader(ctx, rawURL, headers, size)
	defer reader.client.CloseIdleConnections()
	started := time.Now()
	var sample *mkvProbeSample
	defer func() {
		if f.log != nil {
			skipped := int64(0)
			if sample != nil {
				skipped = sample.skippedBytes
			}
			f.log.Info("bounded MKV probe", zap.Int("cdn_requests", reader.requests), zap.Int64("read_bytes", reader.readBytes), zap.Int64("skipped_attachment_bytes", skipped), zap.Duration("elapsed", time.Since(started)), zap.Bool("success", err == nil))
		}
	}()
	sample, err = buildMKVProbeSample(reader)
	if err != nil {
		return nil, fmt.Errorf("轻量 MKV 探测（请求 %d/6，读取 %d/4194304 B）: %w", reader.requests, reader.readBytes, err)
	}
	file, err := os.CreateTemp("", "msg-mkv-probe-*.mkv")
	if err != nil {
		return nil, fmt.Errorf("创建 MKV 探测样本: %w", err)
	}
	defer func() {
		if removeErr := os.Remove(file.Name()); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("清理 MKV 探测样本失败: %w", removeErr))
		}
	}()
	_, writeErr := file.Write(sample.data)
	closeErr := file.Close()
	if err = errors.Join(writeErr, closeErr); err != nil {
		return nil, fmt.Errorf("写入 MKV 探测样本: %w", err)
	}
	result, err = f.probeLocalAcquired(ctx, file.Name(), false)
	if err != nil {
		return nil, err
	}
	// The original Info is authoritative. Format/sample and packet averages
	// cannot describe the whole source file; do not persist those estimates.
	result.DurationSec = sample.duration
	result.BitRate, result.VideoBitRate, result.AudioBitRate = 0, 0, 0
	result.FrameRate = 0
	for _, track := range sample.tracks {
		if track.kind == 1 {
			if track.defaultDuration > 0 {
				result.FrameRate = 1e9 / float64(track.defaultDuration)
			}
			break
		}
	}
	return result, nil
}

func buildMKVProbeSample(r *mkvProbeReader) (*mkvProbeSample, error) {
	header, err := r.element(0, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	if header.id != mkvEBML || header.end > mkvProbeReadAhead {
		return nil, errors.New("MKV: 未找到有效且有界的 EBML 文件头")
	}
	ebml, err := r.readAt(0, header.end)
	if err != nil {
		return nil, err
	}
	docType := ""
	if err := mkvChildren(ebml[header.body:], func(e mkvElement, body []byte) error {
		if e.id == 0x4282 {
			docType = string(body)
		}
		return nil
	}); err != nil || (docType != "matroska" && docType != "webm") {
		return nil, errors.New("MKV: 不支持的 EBML DocType")
	}
	segment, err := r.element(header.end, r.size)
	if err != nil {
		return nil, err
	}
	if segment.id != mkvSegment {
		return nil, errors.New("MKV: 缺少 Segment")
	}
	parts := map[uint64][][]byte{}
	visited := map[int64]uint64{}
	type location struct {
		id  uint64
		pos int64
	}
	var seeks []location
	sample := &mkvProbeSample{}
	var consume func(mkvElement) error
	consume = func(e mkvElement) error {
		if visited[e.start] != 0 {
			return nil
		}
		visited[e.start] = e.id
		switch e.id {
		case mkvAttachments:
			sample.skippedBytes += e.end - e.body
		case mkvSeekHead, mkvInfo, mkvTracks, mkvTags:
			if e.end-e.start > 1<<20 {
				return errors.New("MKV: 元数据元素超过 1 MiB 轻量探测上限")
			}
			data, err := r.readAt(e.start, e.end-e.start)
			if err != nil {
				return err
			}
			if e.id != mkvSeekHead {
				parts[e.id] = append(parts[e.id], data)
				return nil
			}
			return mkvChildren(data[e.body-e.start:], func(entry mkvElement, body []byte) error {
				if entry.id != 0x4dbb {
					return nil
				}
				var id, offset uint64
				var hasOffset bool
				if err := mkvChildren(body, func(field mkvElement, value []byte) error {
					var err error
					if field.id == 0x53ab {
						id, err = mkvUInt(value)
					} else if field.id == 0x53ac {
						offset, err = mkvUInt(value)
						hasOffset = true
					}
					return err
				}); err != nil {
					return err
				}
				if id == mkvSeekHead || id == mkvInfo || id == mkvTracks || id == mkvTags {
					if !hasOffset || offset >= uint64(segment.end-segment.body) || len(seeks) >= 64 {
						return errors.New("MKV: SeekHead 偏移越界或条目过多")
					}
					seeks = append(seeks, location{id, segment.body + int64(offset)})
				}
				return nil
			})
		}
		return nil
	}
	var cluster mkvElement
	pos := segment.body
	for steps := 0; pos < segment.end; steps++ {
		if steps >= 256 {
			return nil, errors.New("MKV: 首个 Cluster 之前的元素过多")
		}
		e, err := r.element(pos, segment.end)
		if err != nil {
			return nil, err
		}
		if e.id == mkvCluster {
			cluster = e
			break
		}
		if err := consume(e); err != nil {
			return nil, err
		}
		pos = e.end
	}
	if cluster.id == 0 {
		return nil, errors.New("MKV: 未找到 Cluster")
	}
	for i := 0; i < len(seeks); i++ {
		location := seeks[i]
		if id := visited[location.pos]; id != 0 {
			if id != location.id {
				return nil, errors.New("MKV: SeekHead 指向的元素与声明不符")
			}
			continue
		}
		e, err := r.element(location.pos, segment.end)
		if err != nil {
			return nil, err
		}
		if e.id != location.id {
			return nil, errors.New("MKV: SeekHead 指向的元素与声明不符")
		}
		if err := consume(e); err != nil {
			return nil, err
		}
	}
	if len(parts[mkvInfo]) != 1 || len(parts[mkvTracks]) != 1 {
		return nil, errors.New("MKV: Info / Tracks 缺失或重复，未猜测参数")
	}
	info, err := mkvElementAt(parts[mkvInfo][0], 0, int64(len(parts[mkvInfo][0])))
	if err != nil {
		return nil, err
	}
	sample.duration, err = mkvDuration(parts[mkvInfo][0][info.body:])
	if err != nil {
		return nil, err
	}
	tracks, err := mkvElementAt(parts[mkvTracks][0], 0, int64(len(parts[mkvTracks][0])))
	if err != nil {
		return nil, err
	}
	sample.tracks, err = mkvReadTracks(parts[mkvTracks][0][tracks.body:])
	if err != nil {
		return nil, err
	}
	seen := map[uint64]bool{}
	var clusters []byte
	for i := 0; i < 4; i++ {
		count := min(cluster.end-cluster.body, int64(mkvProbeClusterMax))
		if count <= 0 {
			return nil, errors.New("MKV: Cluster 为空")
		}
		data, err := r.readAt(cluster.body, count)
		if err != nil {
			return nil, err
		}
		partial := cluster.unknown || count != cluster.end-cluster.body
		compact, err := mkvSampleCluster(data, !partial, seen)
		if err != nil {
			return nil, err
		}
		clusters = append(clusters, compact...)
		err = mkvRequireAVPackets(sample.tracks, seen)
		if err == nil {
			break
		}
		if partial || i == 3 || cluster.end == segment.end {
			return nil, err
		}
		// Walk level-one elements by length, without reading skipped payloads.
		pos := cluster.end
		for steps := 0; ; steps++ {
			if pos >= segment.end || steps >= 64 {
				return nil, err
			}
			next, readErr := r.element(pos, segment.end)
			if readErr != nil {
				return nil, readErr
			}
			if next.id == mkvCluster {
				cluster = next
				break
			}
			if readErr := consume(next); readErr != nil {
				return nil, readErr
			}
			pos = next.end
		}
	}
	body := append([]byte{}, parts[mkvInfo][0]...)
	body = append(body, parts[mkvTracks][0]...)
	for _, tag := range parts[mkvTags] {
		body = append(body, tag...)
	}
	body = append(body, clusters...)
	sample.data = append(append([]byte{}, ebml...), mkvWrap(mkvSegment, body)...)
	return sample, nil
}
