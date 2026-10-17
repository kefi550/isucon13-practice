package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

// アイコン画像とそのハッシュのキャッシュ。
// icons テーブルは LONGBLOB のため、レスポンス生成のたびに読み出すとコストが大きい。
// アイコン更新 (POST /api/icon) と初期化 (POST /api/initialize) で明示的に破棄する。
const iconCacheTTL = 10 * time.Minute

type iconData struct {
	hash  string
	image []byte
}

type iconCacheEntry struct {
	iconData
	gen       uint64
	expiresAt time.Time
}

var (
	iconMu    sync.RWMutex
	iconCache = map[int64]*iconCacheEntry{}
	iconGens  = map[int64]uint64{}
)

func iconHash(image []byte) string {
	sum := sha256.Sum256(image)
	return fmt.Sprintf("%x", sum)
}

// invalidateIcon は書き込みのコミット後に呼び出すこと。
func invalidateIcon(userID int64) {
	iconMu.Lock()
	iconGens[userID]++
	delete(iconCache, userID)
	iconMu.Unlock()
}

func clearIconCache() {
	iconMu.Lock()
	iconCache = map[int64]*iconCacheEntry{}
	iconGens = map[int64]uint64{}
	iconMu.Unlock()
}

var (
	fallbackIconOnce sync.Once
	fallbackIconData iconData
	fallbackIconErr  error
)

func getFallbackIcon() (iconData, error) {
	fallbackIconOnce.Do(func() {
		image, err := os.ReadFile(fallbackImage)
		if err != nil {
			fallbackIconErr = err
			return
		}
		fallbackIconData = iconData{hash: iconHash(image), image: image}
	})
	return fallbackIconData, fallbackIconErr
}

// loadIcons は指定ユーザーのアイコン(未設定ならフォールバック画像)を返す。
// キャッシュミス分だけまとめて SELECT する。
func loadIcons(ctx context.Context, q sqlx.QueryerContext, userIDs []int64) (map[int64]iconData, error) {
	result := make(map[int64]iconData, len(userIDs))
	gens := make(map[int64]uint64, len(userIDs))
	missing := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	now := time.Now()

	iconMu.RLock()
	for _, userID := range userIDs {
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		if entry, ok := iconCache[userID]; ok && now.Before(entry.expiresAt) {
			result[userID] = entry.iconData
			continue
		}
		gens[userID] = iconGens[userID]
		missing = append(missing, userID)
	}
	iconMu.RUnlock()

	if len(missing) == 0 {
		return result, nil
	}

	type iconRow struct {
		UserID int64  `db:"user_id"`
		Image  []byte `db:"image"`
	}
	var rows []iconRow
	query, args, err := sqlx.In("SELECT user_id, image FROM icons WHERE user_id IN (?) ORDER BY id", missing)
	if err != nil {
		return nil, err
	}
	if err := sqlx.SelectContext(ctx, q, &rows, query, args...); err != nil {
		return nil, err
	}
	fetched := make(map[int64][]byte, len(rows))
	for i := range rows {
		// 同一ユーザーに複数行ある場合は若い id を採用(元実装の暗黙の先頭行取得に合わせる)
		if _, ok := fetched[rows[i].UserID]; !ok {
			fetched[rows[i].UserID] = rows[i].Image
		}
	}

	fallback, err := getFallbackIcon()
	if err != nil {
		return nil, err
	}

	toStore := make(map[int64]iconData, len(missing))
	for _, userID := range missing {
		if image, ok := fetched[userID]; ok {
			result[userID] = iconData{hash: iconHash(image), image: image}
		} else {
			result[userID] = fallback
		}
		toStore[userID] = result[userID]
	}

	iconMu.Lock()
	for userID, data := range toStore {
		// 参照〜保存の間に更新が入っていたらキャッシュしない
		if iconGens[userID] != gens[userID] {
			continue
		}
		iconCache[userID] = &iconCacheEntry{iconData: data, gen: gens[userID], expiresAt: now.Add(iconCacheTTL)}
	}
	iconMu.Unlock()

	return result, nil
}

func loadUserIcon(ctx context.Context, q sqlx.QueryerContext, userID int64) (iconData, error) {
	icons, err := loadIcons(ctx, q, []int64{userID})
	if err != nil {
		return iconData{}, err
	}
	return icons[userID], nil
}
