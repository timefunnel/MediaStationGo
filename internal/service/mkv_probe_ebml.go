package service

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	mkvEBML        = 0x1a45dfa3
	mkvSegment     = 0x18538067
	mkvSeekHead    = 0x114d9b74
	mkvInfo        = 0x1549a966
	mkvTracks      = 0x1654ae6b
	mkvTags        = 0x1254c367
	mkvAttachments = 0x1941a469
	mkvCluster     = 0x1f43b675
)

type mkvElement struct {
	id               uint64
	start, body, end int64
	unknown          bool
}

func mkvVInt(data []byte, strip bool) (uint64, int, bool, error) {
	if len(data) == 0 || data[0] == 0 {
		return 0, 0, false, errors.New("MKV: 无效的 EBML 整数")
	}
	n := 1
	for data[0]&(0x80>>uint(n-1)) == 0 {
		n++
	}
	if n > len(data) || (!strip && n > 4) {
		return 0, 0, false, errors.New("MKV: 不完整或过长的 EBML 整数")
	}
	value := uint64(data[0])
	if strip {
		value &= uint64(0xff >> uint(n))
	}
	for _, b := range data[1:n] {
		value = value<<8 | uint64(b)
	}
	return value, n, strip && value == (uint64(1)<<uint(7*n))-1, nil
}

func mkvElementAt(data []byte, pos, limit int64) (mkvElement, error) {
	id, n, _, err := mkvVInt(data, false)
	if err != nil {
		return mkvElement{}, err
	}
	size, m, unknown, err := mkvVInt(data[n:], true)
	if err != nil {
		return mkvElement{}, err
	}
	body := pos + int64(n+m)
	end := limit
	if !unknown {
		if body > limit || size > uint64(limit-body) {
			return mkvElement{}, errors.New("MKV: EBML 元素长度越界")
		}
		end = body + int64(size)
	}
	if body > limit || (unknown && id != mkvSegment && id != mkvCluster) {
		return mkvElement{}, errors.New("MKV: 不支持此元素的未知长度")
	}
	return mkvElement{id: id, start: pos, body: body, end: end, unknown: unknown}, nil
}

func (r *mkvProbeReader) element(pos, limit int64) (mkvElement, error) {
	count := int64(12)
	if r.size > 0 {
		count = min(count, limit-pos)
	}
	data, err := r.readAt(pos, count)
	if err != nil {
		return mkvElement{}, err
	}
	return mkvElementAt(data, pos, min(limit, r.size))
}

func mkvChildren(data []byte, visit func(mkvElement, []byte) error) error {
	for pos := int64(0); pos < int64(len(data)); {
		e, err := mkvElementAt(data[pos:], pos, int64(len(data)))
		if err != nil {
			return err
		}
		if e.unknown {
			return errors.New("MKV: 元数据含未知长度子元素")
		}
		if err := visit(e, data[e.body:e.end]); err != nil {
			return err
		}
		pos = e.end
	}
	return nil
}

func mkvUInt(data []byte) (uint64, error) {
	if len(data) == 0 || len(data) > 8 {
		return 0, errors.New("MKV: 无效的无符号整数")
	}
	var value uint64
	for _, b := range data {
		value = value<<8 | uint64(b)
	}
	return value, nil
}

func mkvDuration(data []byte) (int, error) {
	scale := uint64(1000000)
	duration := float64(0)
	err := mkvChildren(data, func(e mkvElement, body []byte) error {
		var err error
		switch e.id {
		case 0x2ad7b1:
			scale, err = mkvUInt(body)
		case 0x4489:
			switch len(body) {
			case 4:
				duration = float64(math.Float32frombits(binary.BigEndian.Uint32(body)))
			case 8:
				duration = math.Float64frombits(binary.BigEndian.Uint64(body))
			default:
				return errors.New("MKV: 无效的原始时长")
			}
		}
		return err
	})
	seconds := duration * float64(scale) / 1e9
	if err != nil || scale == 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > math.MaxInt32 {
		return 0, errors.New("MKV: 原始时长或时间戳刻度无效")
	}
	// A missing Duration stays unknown; the end of a sample is not a duration.
	return int(seconds), nil
}

type mkvProbeTrack struct {
	number, uid, kind, defaultDuration uint64
}

func mkvReadTracks(data []byte) ([]mkvProbeTrack, error) {
	var tracks []mkvProbeTrack
	seen := map[uint64]bool{}
	err := mkvChildren(data, func(e mkvElement, body []byte) error {
		if e.id != 0xae {
			return nil
		}
		var track mkvProbeTrack
		hasEncodings := false
		if err := mkvChildren(body, func(child mkvElement, value []byte) error {
			var target *uint64
			switch child.id {
			case 0xd7:
				target = &track.number
			case 0x73c5:
				target = &track.uid
			case 0x83:
				target = &track.kind
			case 0x23e383:
				target = &track.defaultDuration
			case 0x6d80:
				if hasEncodings {
					return errors.New("MKV: 轨道 ContentEncodings 重复，未进行轻量探测")
				}
				hasEncodings = true
				return mkvValidateContentEncodings(value)
			}
			if target != nil {
				var err error
				*target, err = mkvUInt(value)
				return err
			}
			return nil
		}); err != nil {
			return err
		}
		if track.number == 0 || track.kind == 0 || seen[track.number] {
			return errors.New("MKV: 轨道编号缺失或重复")
		}
		seen[track.number] = true
		tracks = append(tracks, track)
		return nil
	})
	if err == nil && len(tracks) == 0 {
		err = errors.New("MKV: 未找到轨道")
	}
	return tracks, err
}

// Tracks and Block payloads are copied unchanged into the sample, so ffprobe
// can undo supported compression itself. Do not mistake compression for DRM
// or accept combinations ffprobe can silently ignore.
func mkvValidateContentEncodings(data []byte) error {
	count := 0
	err := mkvChildren(data, func(e mkvElement, body []byte) error {
		if e.id != 0x6240 {
			return nil
		}
		count++
		if count > 1 {
			return errors.New("不支持多层 ContentEncoding")
		}
		kind, scope, algo := uint64(0), uint64(1), uint64(0)
		compression, encryption := false, false
		if err := mkvChildren(body, func(field mkvElement, value []byte) error {
			var err error
			switch field.id {
			case 0x5032:
				scope, err = mkvUInt(value)
			case 0x5033:
				kind, err = mkvUInt(value)
			case 0x5035:
				encryption = true
			case 0x5034:
				compression = true
				err = mkvChildren(value, func(setting mkvElement, payload []byte) error {
					if setting.id == 0x4254 {
						var err error
						algo, err = mkvUInt(payload)
						return err
					}
					return nil
				})
			}
			return err
		}); err != nil {
			return err
		}
		if kind != 0 || encryption {
			return fmt.Errorf("不支持加密或未知编码（ContentEncodingType=%d，ContentEncryption=%t）", kind, encryption)
		}
		if scope == 0 || scope > 3 {
			return fmt.Errorf("不支持的压缩范围 ContentEncodingScope=%d", scope)
		}
		if !compression {
			return errors.New("压缩轨道缺少 ContentCompression")
		}
		if algo != 0 && algo != 3 {
			return fmt.Errorf("不支持的压缩算法 ContentCompAlgo=%d", algo)
		}
		return nil
	})
	if err == nil && count == 0 {
		err = errors.New("缺少 ContentEncoding")
	}
	if err != nil {
		return fmt.Errorf("MKV: 轨道 ContentEncodings: %w", err)
	}
	return nil
}

func mkvWrap(id uint64, body []byte) []byte {
	var idBytes [8]byte
	binary.BigEndian.PutUint64(idBytes[:], id)
	first := 0
	for first < 7 && idBytes[first] == 0 {
		first++
	}
	n := 1
	for uint64(len(body)) >= (uint64(1)<<uint(7*n))-1 {
		n++
	}
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(body))|(uint64(1)<<uint(7*n)))
	out := append([]byte{}, idBytes[first:]...)
	out = append(out, length[8-n:]...)
	return append(out, body...)
}

// Retain complete subelements only. The trailing partial block is never
// handed to ffprobe; offsets and the now-invalid Cluster CRC are discarded.
func mkvSampleCluster(data []byte, complete bool, seen map[uint64]bool) ([]byte, error) {
	var out []byte
	hasTimecode := false
	for pos := int64(0); pos < int64(len(data)); {
		e, err := mkvElementAt(data[pos:], pos, math.MaxInt64)
		if err != nil {
			if !complete && int64(len(data))-pos < 12 {
				break
			}
			return nil, err
		}
		if mkvSegmentChild(e.id) {
			if complete {
				return nil, errors.New("MKV: 有限长度 Cluster 内出现顶层元素")
			}
			break // Unknown-length Cluster ended at the next level-one element.
		}
		if e.unknown {
			return nil, errors.New("MKV: Cluster 子元素使用未知长度")
		}
		if e.end > int64(len(data)) {
			if complete {
				return nil, errors.New("MKV: Cluster 数据不完整")
			}
			break
		}
		body := data[e.body:e.end]
		switch e.id {
		case 0xe7:
			if _, err := mkvUInt(body); err != nil {
				return nil, err
			}
			hasTimecode = true
		case 0xa3:
			if err := mkvMarkBlock(body, seen); err != nil {
				return nil, err
			}
		case 0xa0:
			if err := mkvChildren(body, func(c mkvElement, value []byte) error {
				if c.id == 0xa1 {
					return mkvMarkBlock(value, seen)
				}
				return nil
			}); err != nil {
				return nil, err
			}
		}
		if e.id != 0xbf && e.id != 0xa7 && e.id != 0xab {
			out = append(out, data[e.start:e.end]...)
		}
		pos = e.end
	}
	if !hasTimecode {
		return nil, errors.New("MKV: 样本缺少 Cluster 时间戳")
	}
	return mkvWrap(mkvCluster, out), nil
}

func mkvMarkBlock(body []byte, seen map[uint64]bool) error {
	number, n, unknown, err := mkvVInt(body, true)
	if err != nil || unknown || number == 0 || len(body) <= n+3 {
		return errors.New("MKV: 无效的音视频 Block")
	}
	seen[number] = true
	return nil
}

func mkvSegmentChild(id uint64) bool {
	switch id {
	case mkvSeekHead, mkvInfo, mkvTracks, mkvTags, mkvAttachments, mkvCluster, 0x1c53bb6b, 0x1043a770:
		return true
	}
	return false
}

func mkvRequireAVPackets(tracks []mkvProbeTrack, seen map[uint64]bool) error {
	found := false
	for _, track := range tracks {
		if track.kind != 1 && track.kind != 2 {
			continue
		}
		found = true
		if !seen[track.number] {
			return fmt.Errorf("MKV: 有限样本尚未包含音视频轨道 %d 的完整数据包，未扩大读取或重试", track.number)
		}
	}
	if !found {
		return errors.New("MKV: 未找到音视频轨道")
	}
	return nil
}
