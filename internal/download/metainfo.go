package download

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

// MaxTorrentFileBytes caps a .torrent file. Real ones are kilobytes; the cap
// exists because the bytes arrive from an indexer and bencode parsing is not
// free.
const MaxTorrentFileBytes = 10 << 20

// loadMetaInfo parses a .torrent file's contents.
//
// The bytes come from an indexer, so they are hostile: size-capped before
// parsing, and the parsed info is validated before it reaches anything that
// builds a path. A torrent's declared name is the classic traversal vector in
// this protocol — "../../etc/cron.d/x" is a legal bencode string — and the
// answer here is not to sanitise it but to never use it for a path at all. See
// Engine.DataPathFor, which builds from the info hash.
func loadMetaInfo(data []byte) (*metainfo.MetaInfo, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("download: empty torrent file")
	}
	if len(data) > MaxTorrentFileBytes {
		return nil, fmt.Errorf("download: torrent file is %d bytes, over the %d cap",
			len(data), MaxTorrentFileBytes)
	}

	mi, err := metainfo.Load(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("download: unreadable torrent file: %w", err)
	}
	if _, err := mi.UnmarshalInfo(); err != nil {
		return nil, fmt.Errorf("download: torrent file has an unreadable info dictionary: %w", err)
	}
	return mi, nil
}

// HashOf is the info hash of a payload an indexer returned — .torrent bytes or
// a magnet URI — worked out before the engine is given it, so a caller can ask
// whether this release has been grabbed before (ADR-0030: the queue is the
// blocklist). Lowercase hex, as the queue stores it.
func HashOf(torrent []byte, magnet string) (string, error) {
	switch {
	case len(torrent) > 0:
		mi, err := loadMetaInfo(torrent)
		if err != nil {
			return "", err
		}
		return strings.ToLower(mi.HashInfoBytes().HexString()), nil
	case magnet != "":
		m, err := metainfo.ParseMagnetUri(magnet)
		if err != nil {
			return "", fmt.Errorf("download: unreadable magnet: %w", err)
		}
		return strings.ToLower(m.InfoHash.HexString()), nil
	}
	return "", errors.New("download: neither a torrent nor a magnet")
}

// TorrentFiles lists what a .torrent file says it holds, paths relative to the
// download and joined with "/", sizes as declared. Nothing is written or
// opened: the names are the uploader's, and the only use made of them is to
// read which episodes a season pack claims to hold before it is queued
// (ADR-0033, decision 5). A path in the list is never a path on disk.
func TorrentFiles(torrent []byte) ([]TransferFile, error) {
	mi, err := loadMetaInfo(torrent)
	if err != nil {
		return nil, err
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, fmt.Errorf("download: torrent file has an unreadable info dictionary: %w", err)
	}
	if !info.IsDir() {
		return []TransferFile{{Path: info.BestName(), Bytes: info.Length}}, nil
	}
	out := make([]TransferFile, 0, len(info.Files))
	for _, f := range info.Files {
		out = append(out, TransferFile{Path: strings.Join(f.BestPath(), "/"), Bytes: f.Length})
	}
	return out, nil
}
