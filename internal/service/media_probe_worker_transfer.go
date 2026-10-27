package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Intent is durable before either write. An uncertain write is reconciled by
// reads in a NEW approved run, never submitted again automatically.
type probeWorkerTransfer struct {
	SourceID     string          `json:"source_id"`
	RootID       string          `json:"root_id"`
	RelativePath string          `json:"relative_path"`
	ParentID     string          `json:"parent_id"`
	Name         string          `json:"staging_name"`
	DirectoryID  string          `json:"directory_id"`
	Phase        string          `json:"phase"`
	Source       probeShareEntry `json:"source"`
	FileID       string          `json:"b_file_id,omitempty"`
}

func (c *probe115Client) mkdir(ctx context.Context, parent, name string) (string, error) {
	var data struct {
		ID string `json:"file_id"`
	}
	if err := c.request(ctx, "mkdir", c.base+"/open/folder/add", url.Values{"pid": {parent}, "file_name": {name}}, &data); err != nil {
		return "", err
	}
	if !probeDecimalID(data.ID) || data.ID == "0" || data.ID == parent {
		return "", errors.New("B 建目录响应缺少有效的新目录 ID")
	}
	return data.ID, nil
}

func probeWorkerTransferKey(root ProbeWorkerRoot, relative string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%q", []string{root.SourceID, root.RootID, root.RelativePrefix, relative})))
	return hex.EncodeToString(digest[:])
}

func probeWorkerStagingName(state *probeWorkerState, key string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%q", []string{state.ManifestDigest, state.BatchID, key})))
	return "msg-probe-" + hex.EncodeToString(digest[:12])
}

func listProbeWorkerDestination(ctx context.Context, c *probe115Client, directory string) ([]probe115Entry, error) {
	all, seen := []probe115Entry{}, map[string]bool{}
	total, offset := -1, 0
	for {
		rows, n, err := c.list(ctx, directory, offset)
		if err != nil {
			return nil, err
		}
		if total >= 0 && total != n || offset+len(rows) > n {
			return nil, errors.New("B 转存目录分页期间发生变化")
		}
		total = n
		for _, row := range rows {
			if !probeDecimalID(row.ID) || row.ID == "0" || row.ID == directory || row.Parent != directory || seen[row.ID] || row.Name == "" || row.Name == "." || row.Name == ".." || strings.ContainsAny(row.Name, "/\\\x00\r\n") || (row.Category != "0" && row.Category != "1") {
				return nil, errors.New("B 转存目录包含重复、无效或越界条目")
			}
			seen[row.ID] = true
			all = append(all, row)
		}
		offset += len(rows)
		if offset == n {
			return all, nil
		}
	}
}

func resolveProbeWorkerShare(ctx context.Context, client *probeShareClient, share probeWorkerShare, relative, sourceID string, pages map[string][]probeShareEntry) (probeShareEntry, error) {
	directory := share.DirectoryID
	parts := strings.Split(relative, "/")
	for index, name := range parts {
		key := sourceID + ":" + directory
		rows, cached := pages[key]
		if !cached {
			var err error
			rows, err = client.list(ctx, share, directory)
			if err != nil {
				return probeShareEntry{}, err
			}
			pages[key] = rows
		}
		matches := []probeShareEntry{}
		for _, row := range rows {
			if row.Name == name {
				matches = append(matches, row)
			}
		}
		if len(matches) != 1 {
			return probeShareEntry{}, errors.New("来源分享缺少指定路径或存在同名冲突")
		}
		entry := matches[0]
		if index == len(parts)-1 {
			if entry.Directory || entry.Size <= 0 || !probeSHA1RE.MatchString(entry.SHA1) {
				return probeShareEntry{}, errors.New("来源分享文件缺少 SHA1、大小或不是普通文件")
			}
			return entry, nil
		}
		if !entry.Directory || entry.ID == directory {
			return probeShareEntry{}, errors.New("来源分享路径层级无效")
		}
		directory = entry.ID
	}
	return probeShareEntry{}, errors.New("来源路径为空")
}

func verifyProbeWorkerTransferred(slot *probeWorkerTransfer, rows []probe115Entry) (probeWorkerFile, error) {
	if len(rows) != 1 {
		return probeWorkerFile{}, errors.New("转存目录尚未出现唯一文件；已暂停，不重复提交转存")
	}
	r := rows[0]
	if r.Category != "1" || r.Name != slot.Source.Name || int64(r.Size) != slot.Source.Size || !strings.EqualFold(r.SHA1, slot.Source.SHA1) || r.PickCode == "" || strings.ContainsAny(r.PickCode, "\r\n") || slot.FileID != "" && slot.FileID != r.ID {
		return probeWorkerFile{}, errors.New("转存后的 B 文件与分享来源 SHA1、大小或身份不一致")
	}
	return probeWorkerFile{ID: r.ID, PickCode: r.PickCode, SHA1: strings.ToUpper(r.SHA1), Size: int64(r.Size)}, nil
}

func prepareProbeWorkerTransfers(ctx context.Context, c *probe115Client, shareClient *probeShareClient, manifest *ProbeExchangeManifest, plan *ProbeWorkerPlan, sources *ProbeWorkerSources, state *probeWorkerState, save func() error) error {
	if !probeWorkerHasTransfers(plan.Batch) {
		return nil
	}
	if err := shareClient.verifyAccount(ctx, plan.Batch.AccountID); err != nil {
		return err
	}
	if state.Transfers == nil {
		state.Transfers = map[string]*probeWorkerTransfer{}
	}
	items := map[string]ProbeExchangeItem{}
	for _, item := range manifest.Items {
		items[item.MediaID] = item
	}
	pages, parents := map[string][]probeShareEntry{}, map[string][]probe115Entry{}
	verified := map[string]probeWorkerFile{}
	for _, id := range plan.Batch.MediaIDs {
		item := items[id]
		index, relative, err := probeWorkerBinding(item, plan.Batch.Roots)
		if err != nil {
			return err
		}
		root := plan.Batch.Roots[index]
		if root.SourceID == "" {
			continue
		}
		key := probeWorkerTransferKey(root, relative)
		if file, exists := verified[key]; exists {
			state.Files[id] = file
			continue
		}
		share, err := parseProbeWorkerShare(sources.Shares[root.SourceID])
		if err != nil {
			return err
		}
		slot := state.Transfers[key]
		if slot == nil {
			source, err := resolveProbeWorkerShare(ctx, shareClient, share, relative, root.SourceID, pages)
			if err != nil {
				return err
			}
			if source.Size != item.SizeBytes {
				return errors.New("分享来源文件大小与 MSG 清单不一致，未转存")
			}
			slot = &probeWorkerTransfer{SourceID: root.SourceID, RootID: root.RootID, RelativePath: relative, ParentID: root.DirectoryID, Name: probeWorkerStagingName(state, key), Phase: "new", Source: source}
			state.Transfers[key] = slot
			if err := save(); err != nil {
				return err
			}
		}
		if slot.SourceID != root.SourceID || slot.RootID != root.RootID || slot.RelativePath != relative || slot.ParentID != root.DirectoryID || slot.Name != probeWorkerStagingName(state, key) || slot.Source.Size != item.SizeBytes {
			return errors.New("转存检查点不属于当前清单及目录绑定")
		}
		parentRows, cached := parents[root.DirectoryID]
		if !cached {
			parentRows, err = listProbeWorkerDestination(ctx, c, root.DirectoryID)
			if err != nil {
				return err
			}
			parents[root.DirectoryID] = parentRows
		}
		matches := []probe115Entry{}
		for _, row := range parentRows {
			if row.Name == slot.Name {
				matches = append(matches, row)
			}
		}
		switch slot.Phase {
		case "new":
			if len(matches) != 0 {
				return errors.New("B 暂存名称已被占用，没有写入意图记录，拒绝接管")
			}
			slot.Phase = "creating"
			if err := save(); err != nil {
				return err
			}
			directory, err := c.mkdir(ctx, root.DirectoryID, slot.Name)
			if err != nil {
				return err
			}
			slot.DirectoryID, slot.Phase = directory, "ready"
			parents[root.DirectoryID] = append(parentRows, probe115Entry{ID: directory, Parent: root.DirectoryID, Category: "0", Name: slot.Name})
			if err := save(); err != nil {
				return err
			}
		case "creating":
			if len(matches) != 1 || matches[0].Category != "0" {
				return errors.New("建目录结果仍不明确，已暂停；不会重复创建")
			}
			slot.DirectoryID, slot.Phase = matches[0].ID, "ready"
			if err := save(); err != nil {
				return err
			}
		default:
			if len(matches) != 1 || matches[0].Category != "0" || matches[0].ID != slot.DirectoryID {
				return errors.New("B 暂存目录身份已变化，停止转存")
			}
		}
		rows, err := listProbeWorkerDestination(ctx, c, slot.DirectoryID)
		if err != nil {
			return err
		}
		if slot.Phase == "ready" {
			if len(rows) != 0 {
				return errors.New("B 暂存目录并非空目录，未提交转存")
			}
			slot.Phase = "receiving"
			if err := save(); err != nil {
				return err
			}
			if err := shareClient.receive(ctx, share, slot.Source.ID, slot.DirectoryID); err != nil {
				return err
			}
			rows, err = listProbeWorkerDestination(ctx, c, slot.DirectoryID)
			if err != nil {
				return err
			}
		}
		file, err := verifyProbeWorkerTransferred(slot, rows)
		if err != nil {
			return fmt.Errorf("媒体 %s: %w", id, err)
		}
		slot.FileID, slot.Phase = file.ID, "verified"
		state.Files[id], verified[key] = file, file
		if err := save(); err != nil {
			return err
		}
	}
	return nil
}

func validateProbeWorkerTransfers(state *probeWorkerState) error {
	for key, slot := range state.Transfers {
		if slot == nil || !probeSHA256RE.MatchString(key) || slot.SourceID == "" || slot.RootID == "" || validateProbeRelativePath(slot.RelativePath) != nil || !probeDecimalID(slot.ParentID) || slot.ParentID == "0" || slot.Name != probeWorkerStagingName(state, key) || !probeDecimalID(slot.Source.ID) || slot.Source.ID == "0" || !probeSHA1RE.MatchString(slot.Source.SHA1) || slot.Source.Directory || slot.Source.Size <= 0 || slot.Source.Name == "" || strings.ContainsAny(slot.Source.Name, "/\\\x00\r\n") {
			return errors.New("转存检查点包含无效来源或暂存身份")
		}
		switch slot.Phase {
		case "new", "creating":
			if slot.DirectoryID != "" || slot.FileID != "" {
				return errors.New("未完成建目录的检查点包含无效文件身份")
			}
		case "ready", "receiving", "verified":
			if !probeDecimalID(slot.DirectoryID) || slot.DirectoryID == "0" || slot.DirectoryID == slot.ParentID || slot.Phase == "verified" && (!probeDecimalID(slot.FileID) || slot.FileID == "0") {
				return errors.New("转存检查点目标身份无效")
			}
		default:
			return errors.New("转存检查点状态无效")
		}
	}
	return nil
}
