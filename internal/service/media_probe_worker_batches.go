package service

import (
	"errors"
	"strconv"
)

type ProbeWorkerBatchSet struct {
	Version        int                `json:"version"`
	ManifestDigest string             `json:"manifest_digest"`
	SourcesDigest  string             `json:"sources_digest"`
	Roots          []ProbeWorkerRoot  `json:"roots"`
	Batches        []ProbeWorkerBatch `json:"batches"`
}

// Finite offline partitioning. It starts no worker and grants no permission to
// execute another round. All media IDs and root bindings are explicit.
func SplitProbeWorkerBatches(m *ProbeExchangeManifest, template ProbeWorkerBatch, sources *ProbeWorkerSources, size int) (*ProbeWorkerBatchSet, error) {
	if err := m.CheckDigest(); err != nil {
		return nil, err
	}
	if size < 1 || size > 100 {
		return nil, errors.New("整库分批大小必须为 1 到 100")
	}
	digest, err := probeWorkerSourcesDigest(template, sources)
	if err != nil {
		return nil, err
	}
	set := &ProbeWorkerBatchSet{Version: ProbeExchangeVersion, ManifestDigest: m.Digest, SourcesDigest: digest, Roots: template.Roots, Batches: []ProbeWorkerBatch{}}
	items := []ProbeExchangeItem{}
	for _, item := range m.Items {
		if !probeExchangeBasicComplete(item.Metadata) || item.Metadata.BitRate <= 0 {
			items = append(items, item)
		}
	}
	for start := 0; start < len(items); start += size {
		batch := template
		batch.ID = template.ID + "-" + strconv.Itoa(len(set.Batches)+1)
		batch.MediaIDs, batch.Roots = []string{}, []ProbeWorkerRoot{}
		used := map[int]bool{}
		for _, item := range items[start:min(start+size, len(items))] {
			index, _, err := probeWorkerBinding(item, template.Roots)
			if err != nil {
				return nil, err
			}
			batch.MediaIDs = append(batch.MediaIDs, item.MediaID)
			if !used[index] {
				batch.Roots = append(batch.Roots, template.Roots[index])
				used[index] = true
			}
		}
		if err := validateProbeWorkerBatch(m, &batch); err != nil {
			return nil, err
		}
		set.Batches = append(set.Batches, batch)
	}
	return set, nil
}

func (set *ProbeWorkerBatchSet) Select(m *ProbeExchangeManifest, index int) (ProbeWorkerBatch, error) {
	if set == nil || set.Version != ProbeExchangeVersion || set.ManifestDigest != m.Digest || index < 0 || index >= len(set.Batches) {
		return ProbeWorkerBatch{}, errors.New("批次集合与清单不一致或索引超出范围（从 0 开始）")
	}
	return set.Batches[index], nil
}

func (set *ProbeWorkerBatchSet) CheckSources(sources *ProbeWorkerSources) error {
	digest, err := probeWorkerSourcesDigest(ProbeWorkerBatch{Roots: set.Roots}, sources)
	if err != nil {
		return err
	}
	if digest != set.SourcesDigest {
		return errors.New("来源配置已变化，请重新生成整库批次集合")
	}
	return nil
}
