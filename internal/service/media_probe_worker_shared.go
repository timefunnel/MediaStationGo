package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func lookupProbeWorkerShared(ctx context.Context, client *http.Client, gate *probeWorkerGate, endpoint string, files map[string]probeWorkerFile) (map[string]*ProbeResult, error) {
	hashes := make([]string, 0, len(files))
	byHash := map[string]probeWorkerFile{}
	for _, file := range files {
		hash := strings.ToUpper(file.SHA1)
		if previous, exists := byHash[hash]; exists {
			if previous.Size != file.Size {
				return nil, errors.New("相同 SHA1 对应不同文件大小")
			}
			continue
		}
		byHash[hash] = file
		hashes = append(hashes, hash)
	}
	if len(hashes) == 0 {
		return map[string]*ProbeResult{}, nil
	}
	if len(hashes) > 100 {
		return nil, errors.New("共享查询超过当前有限批次数量")
	}
	sort.Strings(hashes)
	data, err := json.Marshal(map[string]any{"sha1s": hashes})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("共享服务地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", probeWorkerUA)
	if err := gate.take(ctx, "shared"); err != nil {
		return nil, err
	}
	gate.mu.Lock()
	gate.stats.SharedLookups += len(hashes)
	gate.mu.Unlock()
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("共享服务连接失败，未降级为整批 probe")
	}
	data, err = readProbeWorkerResponse(resp, 16<<20)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		SHA1 string  `json:"sha1"`
		Data *string `json:"data"`
	}
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != len(hashes) {
		return nil, errors.New("共享服务响应缺项或结构无效，未降级为 probe")
	}
	result := map[string]*ProbeResult{}
	seen := map[string]bool{}
	for _, row := range rows {
		hash := strings.ToUpper(row.SHA1)
		file, exists := byHash[hash]
		if !exists || seen[hash] {
			return nil, errors.New("共享响应包含重复或未知 SHA1")
		}
		seen[hash] = true
		if row.Data == nil {
			result[hash] = nil
			continue
		}
		if len(*row.Data) > 8<<20 {
			return nil, errors.New("共享媒体信息压缩数据超限")
		}
		compressed, err := base64.StdEncoding.DecodeString(*row.Data)
		if err != nil {
			return nil, errors.New("共享媒体信息 Base64 无效")
		}
		reader, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return nil, errors.New("共享媒体信息 Gzip 无效")
		}
		decoded, readErr := io.ReadAll(io.LimitReader(reader, (4<<20)+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || len(decoded) > 4<<20 {
			return nil, errors.New("共享媒体信息解压失败或超限")
		}
		probe, err := parseProbeWorkerEmby(decoded, file.Size)
		if err != nil {
			return nil, err
		}
		result[hash] = probe
	}
	return result, nil
}

type probeWorkerEmbySource struct {
	Size         int64  `json:"Size"`
	RunTimeTicks int64  `json:"RunTimeTicks"`
	Container    string `json:"Container"`
	Bitrate      int64  `json:"Bitrate"`
	MediaStreams []struct {
		Type             string  `json:"Type"`
		Codec            string  `json:"Codec"`
		Profile          string  `json:"Profile"`
		Width            int     `json:"Width"`
		Height           int     `json:"Height"`
		BitRate          int64   `json:"BitRate"`
		AverageFrameRate float64 `json:"AverageFrameRate"`
		RealFrameRate    float64 `json:"RealFrameRate"`
		BitDepth         int     `json:"BitDepth"`
		ColorTransfer    string  `json:"ColorTransfer"`
		VideoRange       string  `json:"VideoRange"`
		VideoRangeType   string  `json:"VideoRangeType"`
		Channels         int     `json:"Channels"`
		ChannelLayout    string  `json:"ChannelLayout"`
		SampleRate       int     `json:"SampleRate"`
	} `json:"MediaStreams"`
}

// Convert the external schema to ffprobe's schema, then reuse MSG's existing
// parser instead of maintaining another codec/range/bit-depth parser.
func parseProbeWorkerEmby(data []byte, expectedSize int64) (*ProbeResult, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("共享媒体信息为空")
	}
	if data[0] == '[' {
		var rows []json.RawMessage
		if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 1 {
			return nil, errors.New("共享媒体信息不是单一文件记录")
		}
		data = rows[0]
	}
	var record struct {
		Source probeWorkerEmbySource `json:"MediaSourceInfo"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, errors.New("共享 MediaSourceInfo 结构无效")
	}
	source := record.Source
	if expectedSize <= 0 || source.Size != expectedSize {
		return nil, errors.New("共享记录的文件大小不匹配，不进行自动探测兜底")
	}
	streams := []map[string]any{}
	for _, stream := range source.MediaStreams {
		kind := strings.ToLower(stream.Type)
		if kind != "audio" && kind != "video" {
			continue
		}
		frameRate := stream.AverageFrameRate
		if frameRate == 0 {
			frameRate = stream.RealFrameRate
		}
		transfer := stream.ColorTransfer
		sideData := []map[string]string{}
		switch strings.ToUpper(stream.VideoRangeType) {
		case "HDR10", "HDR10PLUS":
			transfer = "smpte2084"
		case "HLG":
			transfer = "arib-std-b67"
		case "DOVI", "DOVIWITHHDR10", "DOVIWITHHLG", "DOVIWITHSDR":
			sideData = append(sideData, map[string]string{"side_data_type": "DOVI configuration record"})
		case "SDR":
			if transfer == "" {
				transfer = "bt709"
			}
		}
		streams = append(streams, map[string]any{"codec_type": kind, "codec_name": normalizeProbeWorkerCodec(stream.Codec),
			"profile": stream.Profile, "width": stream.Width, "height": stream.Height, "bit_rate": strconv.FormatInt(stream.BitRate, 10),
			"avg_frame_rate": strconv.FormatFloat(frameRate, 'f', -1, 64), "bits_per_sample": stream.BitDepth,
			"color_transfer": transfer, "side_data_list": sideData, "channels": stream.Channels,
			"channel_layout": stream.ChannelLayout, "sample_rate": strconv.Itoa(stream.SampleRate)})
	}
	wire := map[string]any{"format": map[string]any{"duration": strconv.FormatFloat(float64(source.RunTimeTicks)/10000000, 'f', -1, 64),
		"format_name": source.Container, "bit_rate": strconv.FormatInt(source.Bitrate, 10)}, "streams": streams}
	wireData, err := json.Marshal(wire)
	if err != nil {
		return nil, errors.New("共享媒体信息无法转换")
	}
	probe, err := parseProbeJSON(wireData)
	if err != nil {
		return nil, errors.New("共享媒体信息无法解析")
	}
	if err := validateExchangeProbe(probe); err != nil {
		return nil, err
	}
	return probe, nil
}

func normalizeProbeWorkerCodec(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "h265", "h.265":
		return "hevc"
	case "avc", "h.264":
		return "h264"
	case "e-ac-3", "e-ac3":
		return "eac3"
	case "ac-3":
		return "ac3"
	}
	return value
}
