package service

import (
	"bytes"
	"compress/zlib"
	"strings"
	"testing"
)

func TestMKVContentEncodingValidation(t *testing.T) {
	compression := func(algo uint64) []byte { return mkvWrap(0x5034, mkvTestUInt(0x4254, algo)) }
	for _, test := range []struct {
		name string
		body []byte
		want string
	}{
		{"zlib-defaults", mkvWrap(0x5034, nil), ""},
		{"header-stripping", compression(3), ""},
		{"codec-private", mkvTestJoin(mkvTestUInt(0x5032, 2), compression(0)), ""},
		{"frames-and-private", mkvTestJoin(mkvTestUInt(0x5032, 3), compression(0)), ""},
		{"encrypted", mkvTestJoin(mkvTestUInt(0x5033, 1), mkvWrap(0x5035, nil)), "ContentEncodingType=1"},
		{"encryption-with-default-type", mkvTestJoin(compression(3), mkvWrap(0x5035, nil)), "ContentEncryption=true"},
		{"unknown-type", mkvTestJoin(mkvTestUInt(0x5033, 9), compression(3)), "ContentEncodingType=9"},
		{"empty-scope", mkvTestJoin(mkvTestUInt(0x5032, 0), compression(3)), "ContentEncodingScope=0"},
		{"next-encoding-scope", mkvTestJoin(mkvTestUInt(0x5032, 4), compression(3)), "ContentEncodingScope=4"},
		{"unsupported-algo", compression(8), "ContentCompAlgo=8"},
		{"bzlib", compression(1), "ContentCompAlgo=1"},
		{"lzo", compression(2), "ContentCompAlgo=2"},
		{"missing-compression", nil, "缺少 ContentCompression"},
		{"malformed-compression", mkvWrap(0x5034, []byte{0}), "无效的 EBML"},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoding := mkvWrap(0x6240, test.body)
			track := mkvWrap(0xae, mkvTestJoin(mkvTestUInt(0xd7, 1), mkvTestUInt(0x83, 1), mkvWrap(0x6d80, encoding)))
			tracks, err := mkvReadTracks(track)
			if test.want == "" {
				if err != nil || len(tracks) != 1 || tracks[0].number != 1 {
					t.Fatalf("supported compression rejected: %+v, %v", tracks, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
	if err := mkvValidateContentEncodings(nil); err == nil {
		t.Fatal("empty ContentEncodings accepted")
	}
	encoding := mkvWrap(0x6240, compression(3))
	if err := mkvValidateContentEncodings(mkvTestJoin(encoding, encoding)); err == nil || !strings.Contains(err.Error(), "多层") {
		t.Fatalf("combined encodings accepted: %v", err)
	}
	encodings := mkvWrap(0x6d80, encoding)
	track := mkvWrap(0xae, mkvTestJoin(mkvTestUInt(0xd7, 1), mkvTestUInt(0x83, 1), encodings, encodings))
	if _, err := mkvReadTracks(track); err == nil || !strings.Contains(err.Error(), "ContentEncodings 重复") {
		t.Fatalf("duplicate ContentEncodings accepted: %v", err)
	}
}

// Rewrite only locally generated fixtures. Header stripping removes the E-AC-3
// sync word; zlib compresses real packets/private data. Indexes and CRCs are
// removed because element sizes change, while timestamps and other data stay.
func mkvTestCompressTracks(t *testing.T, data []byte, mode string, scope uint64) []byte {
	t.Helper()
	header, err := mkvElementAt(data, 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	segment, err := mkvElementAt(data[header.end:], header.end, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	encoded := map[uint64]bool{}
	privateCount, packetCount := 0, 0
	compress := func(value []byte) []byte {
		if mode == "header-stripping" {
			if !bytes.HasPrefix(value, []byte{0x0b, 0x77}) {
				t.Fatal("E-AC-3 fixture packet lacks the expected sync word")
			}
			return value[2:]
		}
		var buf bytes.Buffer
		writer := zlib.NewWriter(&buf)
		if _, err := writer.Write(value); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	block := func(value []byte) []byte {
		number, n, _, err := mkvVInt(value, true)
		if err != nil || len(value) <= n+3 {
			t.Fatal("invalid fixture Block")
		}
		if !encoded[number] || scope&1 == 0 {
			return value
		}
		if value[n+2]&6 != 0 {
			t.Fatal("fixture unexpectedly uses lacing")
		}
		packetCount++
		return mkvTestJoin(value[:n+3], compress(value[n+3:]))
	}
	var body []byte
	err = mkvChildren(data[segment.body:segment.end], func(e mkvElement, value []byte) error {
		switch e.id {
		case mkvSeekHead, 0x1c53bb6b, 0xbf:
			return nil
		case mkvTracks:
			var tracks []byte
			if err := mkvChildren(value, func(entry mkvElement, fields []byte) error {
				if entry.id == 0xbf {
					return nil
				}
				if entry.id != 0xae {
					tracks = append(tracks, value[entry.start:entry.end]...)
					return nil
				}
				trackInfo, err := mkvReadTracks(mkvWrap(0xae, fields))
				if err != nil {
					return err
				}
				track := trackInfo[0]
				target := track.kind == 2 || (mode == "zlib" && track.kind == 1)
				encoded[track.number] = target
				var rewritten []byte
				if err := mkvChildren(fields, func(field mkvElement, payload []byte) error {
					if field.id == 0xbf {
						return nil
					}
					if field.id == 0x63a2 && target && scope&2 != 0 && len(payload) > 0 {
						privateCount++
						rewritten = append(rewritten, mkvWrap(field.id, compress(payload))...)
					} else {
						rewritten = append(rewritten, fields[field.start:field.end]...)
					}
					return nil
				}); err != nil {
					return err
				}
				if target {
					var compression []byte
					if mode == "header-stripping" {
						compression = mkvTestJoin(mkvTestUInt(0x4254, 3), mkvWrap(0x4255, []byte{0x0b, 0x77}))
					}
					encoding := mkvTestJoin(mkvTestUInt(0x5032, scope), mkvWrap(0x5034, compression))
					rewritten = append(rewritten, mkvWrap(0x6d80, mkvWrap(0x6240, encoding))...)
				}
				tracks = append(tracks, mkvWrap(0xae, rewritten)...)
				return nil
			}); err != nil {
				return err
			}
			body = append(body, mkvWrap(e.id, tracks)...)
		case mkvCluster:
			var cluster []byte
			if err := mkvChildren(value, func(child mkvElement, payload []byte) error {
				switch child.id {
				case 0xbf, 0xa7, 0xab:
					return nil
				case 0xa3:
					payload = block(payload)
				case 0xa0:
					var group []byte
					if err := mkvChildren(payload, func(field mkvElement, packet []byte) error {
						if field.id == 0xbf {
							return nil
						}
						if field.id == 0xa1 {
							packet = block(packet)
						}
						group = append(group, mkvWrap(field.id, packet)...)
						return nil
					}); err != nil {
						return err
					}
					payload = group
				}
				cluster = append(cluster, mkvWrap(child.id, payload)...)
				return nil
			}); err != nil {
				return err
			}
			body = append(body, mkvWrap(e.id, cluster)...)
		default:
			body = append(body, data[segment.body+e.start:segment.body+e.end]...)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if (scope&1 != 0 && packetCount == 0) || (scope&2 != 0 && privateCount == 0) {
		t.Fatalf("compression fixture did not exercise scope=%d: packets=%d private=%d", scope, packetCount, privateCount)
	}
	return mkvTestJoin(data[:header.end], mkvWrap(mkvSegment, body))
}
