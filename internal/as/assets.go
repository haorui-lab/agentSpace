package as

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type storedAsset struct {
	Asset
	StorageKey string
}

func (s *Server) SaveAsset(ctx context.Context, p Principal, spaceID, filename string, body []byte, base string) (Asset, error) {
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return Asset{}, err
	}
	if int64(len(body)) > s.cfg.MaxUploadBytes {
		return Asset{}, tooLarge("upload exceeds the size limit")
	}
	safeName, ext, err := sanitizeFilename(filename)
	if err != nil {
		return Asset{}, err
	}
	mime, ok := sniffAsset(ext, body)
	if !ok {
		return Asset{}, badRequest("file content does not match an allowed type")
	}
	key := randomHex(32)
	path, err := s.safeAssetPath(key)
	if err != nil {
		return Asset{}, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return Asset{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return Asset{}, err
	}
	asset := Asset{
		ID: newID("asset_"), SpaceID: spaceID, Filename: safeName, MimeType: mime,
		Size: int64(len(body)), UploadedBy: p.ActorID(), CreatedAt: now(),
	}
	asset.DownloadURL = strings.TrimRight(base, "/") + "/api/v1/spaces/" + spaceID + "/assets/" + asset.ID
	ev, err := s.st.createAsset(ctx, assetRecord{
		ID: asset.ID, SpaceID: spaceID, Filename: asset.Filename, MimeType: asset.MimeType,
		Size: asset.Size, StorageKey: key, UploadedBy: asset.UploadedBy, CreatedAt: asset.CreatedAt,
	})
	if err != nil {
		os.Remove(path)
		return Asset{}, err
	}
	s.hub.publish(ev)
	return asset, nil
}

func sanitizeFilename(name string) (string, string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, name)
	name = strings.ReplaceAll(name, "..", "_")
	name = strings.Trim(name, ". ")
	if name == "" || name == "_" {
		return "", "", badRequest("invalid filename")
	}
	if len([]rune(name)) > 180 {
		name = string([]rune(name)[:180])
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".txt", ".log", ".md", ".json", ".pdf":
	default:
		return "", "", badRequest("file type is not allowed")
	}
	return name, ext, nil
}

func sniffAsset(ext string, body []byte) (string, bool) {
	if len(body) == 0 || len(body) > 0 && bytes.Contains(body, []byte{0}) && !isBinaryExt(ext) {
		if !isBinaryExt(ext) {
			return "", false
		}
	}
	switch ext {
	case ".png":
		return "image/png", bytes.HasPrefix(body, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	case ".jpg", ".jpeg":
		return "image/jpeg", len(body) > 3 && body[0] == 0xFF && body[1] == 0xD8 && body[2] == 0xFF
	case ".gif":
		return "image/gif", bytes.HasPrefix(body, []byte("GIF87a")) || bytes.HasPrefix(body, []byte("GIF89a"))
	case ".webp":
		return "image/webp", len(body) > 12 && string(body[0:4]) == "RIFF" && string(body[8:12]) == "WEBP"
	case ".txt", ".log", ".md":
		return "text/plain; charset=utf-8", utf8.Valid(body) && !bytes.Contains(body, []byte{0})
	case ".json":
		return "application/json", json.Valid(bytes.TrimSpace(body)) && utf8.Valid(body)
	case ".pdf":
		return "application/pdf", bytes.HasPrefix(body, []byte("%PDF-"))
	default:
		return "", false
	}
}

func isBinaryExt(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".pdf":
		return true
	default:
		return false
	}
}

func (s *Server) safeAssetPath(key string) (string, error) {
	if len(key) != 64 || !isHex(key) {
		return "", badRequest("invalid asset key")
	}
	dir := filepath.Join(s.cfg.AssetDir, key[:2], key[2:4])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	full := filepath.Join(dir, key)
	abs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(s.cfg.AssetDir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", badRequest("invalid asset path")
	}
	return abs, nil
}

func (s *Server) OpenAsset(ctx context.Context, p Principal, spaceID, assetID string) (storedAsset, []byte, error) {
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return storedAsset{}, nil, err
	}
	rec, err := s.st.getAsset(ctx, spaceID, assetID)
	if err != nil {
		return storedAsset{}, nil, err
	}
	row := storedAsset{Asset: Asset{
		ID: rec.ID, SpaceID: rec.SpaceID, Filename: rec.Filename, MimeType: rec.MimeType,
		Size: rec.Size, UploadedBy: rec.UploadedBy, CreatedAt: rec.CreatedAt,
	}, StorageKey: rec.StorageKey}
	path, err := s.safeAssetPath(row.StorageKey)
	if err != nil {
		return storedAsset{}, nil, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return storedAsset{}, nil, notFound("asset not found")
	}
	return row, body, nil
}
