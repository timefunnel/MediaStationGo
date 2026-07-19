import assert from 'node:assert/strict'
import test from 'node:test'

import { groupSeries, seriesTitleFromPath } from './groupSeries.ts'

const seriesDirectory =
  'cloud://openlist/115/动漫/[Maho.sub&VCB-Studio] Aki Sora Yume no Naka [Hi10p_1080p]'

test('SPs 特典目录与正片目录聚合为同一部剧', () => {
  const episodePath = `${seriesDirectory}/[Maho.sub&VCB-Studio] Aki Sora Yume no Naka [01][Hi10p_1080p][x264_flac].mkv`
  const specialPath = `${seriesDirectory}/SPs/[Maho.sub&VCB-Studio] Aki Sora Yume no Naka [NCED][Hi10p_1080p][x264_flac].mkv`

  assert.equal(seriesTitleFromPath(specialPath), seriesTitleFromPath(episodePath))

  const cards = groupSeries([
    {
      id: 'episode-1',
      library_id: 'anime-library',
      title: 'Aki Sora OVA',
      path: episodePath,
      season_num: 1,
      episode_num: 1,
      tmdb_id: 1281913,
    },
    {
      id: 'special-nced',
      library_id: 'anime-library',
      title: 'Aki Sora OVA',
      path: specialPath,
      season_num: 1,
      episode_num: 2,
      tmdb_id: 1281913,
    },
  ])

  assert.equal(cards.length, 1)
  assert.equal(cards[0]?.count, 2)
})
