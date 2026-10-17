package main

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/jmoiron/sqlx"
)

// distinctInt64s は int64 スライスから重複を除いたスライスを返す
func distinctInt64s(ids []int64) []int64 {
	if len(ids) == 0 {
		return ids
	}
	seen := make(map[int64]struct{}, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}

// fetchUsersByIDs は users を IN 句でまとめて取得する (N+1解消)
func fetchUsersByIDs(ctx context.Context, q sqlx.QueryerContext, userIDs []int64) (map[int64]*UserModel, error) {
	uniqueIDs := distinctInt64s(userIDs)
	users := make(map[int64]*UserModel, len(uniqueIDs))
	if len(uniqueIDs) == 0 {
		return users, nil
	}

	query, args, err := sqlx.In("SELECT * FROM users WHERE id IN (?)", uniqueIDs)
	if err != nil {
		return nil, err
	}
	var userModels []*UserModel
	if err := sqlx.SelectContext(ctx, q, &userModels, query, args...); err != nil {
		return nil, err
	}
	for _, userModel := range userModels {
		users[userModel.ID] = userModel
	}
	return users, nil
}

// fetchLivestreamsByIDs は livestreams を IN 句でまとめて取得する (N+1解消)
func fetchLivestreamsByIDs(ctx context.Context, q sqlx.QueryerContext, livestreamIDs []int64) (map[int64]*LivestreamModel, error) {
	uniqueIDs := distinctInt64s(livestreamIDs)
	livestreams := make(map[int64]*LivestreamModel, len(uniqueIDs))
	if len(uniqueIDs) == 0 {
		return livestreams, nil
	}

	query, args, err := sqlx.In("SELECT * FROM livestreams WHERE id IN (?)", uniqueIDs)
	if err != nil {
		return nil, err
	}
	var livestreamModels []*LivestreamModel
	if err := sqlx.SelectContext(ctx, q, &livestreamModels, query, args...); err != nil {
		return nil, err
	}
	for _, livestreamModel := range livestreamModels {
		livestreams[livestreamModel.ID] = livestreamModel
	}
	return livestreams, nil
}

// fetchLivestreamTagsByLivestreamIDs は livestream_tags を IN 句でまとめて取得する (N+1解消)
func fetchLivestreamTagsByLivestreamIDs(ctx context.Context, q sqlx.QueryerContext, livestreamIDs []int64) (map[int64][]*LivestreamTagModel, error) {
	uniqueIDs := distinctInt64s(livestreamIDs)
	tags := make(map[int64][]*LivestreamTagModel, len(uniqueIDs))
	if len(uniqueIDs) == 0 {
		return tags, nil
	}

	query, args, err := sqlx.In("SELECT * FROM livestream_tags WHERE livestream_id IN (?) ORDER BY id", uniqueIDs)
	if err != nil {
		return nil, err
	}
	var livestreamTagModels []*LivestreamTagModel
	if err := sqlx.SelectContext(ctx, q, &livestreamTagModels, query, args...); err != nil {
		return nil, err
	}
	for _, livestreamTagModel := range livestreamTagModels {
		tags[livestreamTagModel.LivestreamID] = append(tags[livestreamTagModel.LivestreamID], livestreamTagModel)
	}
	return tags, nil
}

// fetchLivecommentsByIDs は livecomments を IN 句でまとめて取得する (N+1解消)
func fetchLivecommentsByIDs(ctx context.Context, q sqlx.QueryerContext, livecommentIDs []int64) (map[int64]*LivecommentModel, error) {
	uniqueIDs := distinctInt64s(livecommentIDs)
	livecomments := make(map[int64]*LivecommentModel, len(uniqueIDs))
	if len(uniqueIDs) == 0 {
		return livecomments, nil
	}

	query, args, err := sqlx.In("SELECT * FROM livecomments WHERE id IN (?)", uniqueIDs)
	if err != nil {
		return nil, err
	}
	var livecommentModels []*LivecommentModel
	if err := sqlx.SelectContext(ctx, q, &livecommentModels, query, args...); err != nil {
		return nil, err
	}
	for _, livecommentModel := range livecommentModels {
		livecomments[livecommentModel.ID] = livecommentModel
	}
	return livecomments, nil
}

// ---- ユーザのテーマ・アイコンハッシュのメモリキャッシュ ----
//
// themes は初期化とユーザ登録時にしか INSERT されず、icons は POST /api/icon で
// 上書きされるだけなので、user_id ごとにキャッシュして良い。
// アイコン更新時は invalidateUserInfoCache でエントリを捨てて次の参照で読み直すため、
// アイコン変更は 2 秒以内に icon_hash へ反映される。

type userInfo struct {
	theme    ThemeModel
	iconHash string
}

var (
	userInfoCache     sync.Map // int64 -> userInfo
	userNameToIDCache sync.Map // string -> int64
	iconImageCache    sync.Map // int64 -> []byte (アイコン画像)
)

func loadUserInfo(ctx context.Context, q sqlx.QueryerContext, userID int64) (userInfo, error) {
	themeModel := ThemeModel{}
	if err := sqlx.GetContext(ctx, q, &themeModel, "SELECT * FROM themes WHERE user_id = ?", userID); err != nil {
		return userInfo{}, err
	}

	// 画像本体(BLOB)は読まず、POST /api/icon 時に保存した icon_hash 列を参照する
	var iconHash string
	if err := sqlx.GetContext(ctx, q, &iconHash, "SELECT icon_hash FROM icons WHERE user_id = ?", userID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return userInfo{}, err
		}
		iconHash, err = getFallbackIconHash()
		if err != nil {
			return userInfo{}, err
		}
	}

	return userInfo{
		theme:    themeModel,
		iconHash: iconHash,
	}, nil
}

func getUserInfo(ctx context.Context, q sqlx.QueryerContext, userID int64) (userInfo, error) {
	if cached, ok := userInfoCache.Load(userID); ok {
		return cached.(userInfo), nil
	}

	info, err := loadUserInfo(ctx, q, userID)
	if err != nil {
		return userInfo{}, err
	}
	userInfoCache.Store(userID, info)
	return info, nil
}

// getUserIDByName は username から user_id をキャッシュ付きで解決する
// (users.name は UNIQUE で、ユーザの削除は存在しないためキャッシュして良い)
func getUserIDByName(ctx context.Context, q sqlx.QueryerContext, name string) (int64, error) {
	if cached, ok := userNameToIDCache.Load(name); ok {
		return cached.(int64), nil
	}

	var userID int64
	if err := sqlx.GetContext(ctx, q, &userID, "SELECT id FROM users WHERE name = ?", name); err != nil {
		return 0, err
	}
	userNameToIDCache.Store(name, userID)
	return userID, nil
}

// cacheUserNameToID はユーザ登録時に name -> user_id を登録する
func cacheUserNameToID(name string, userID int64) {
	userNameToIDCache.Store(name, userID)
}

// getUserIconImage はアイコン画像をキャッシュ付きで取得する
func getUserIconImage(ctx context.Context, q sqlx.QueryerContext, userID int64) ([]byte, error) {
	if cached, ok := iconImageCache.Load(userID); ok {
		return cached.([]byte), nil
	}

	var image []byte
	if err := sqlx.GetContext(ctx, q, &image, "SELECT image FROM icons WHERE user_id = ?", userID); err != nil {
		return nil, err
	}
	iconImageCache.Store(userID, image)
	return image, nil
}

// invalidateUserInfoCache はアイコン変更時に呼び出してキャッシュを捨てる
// (画像と icon_hash を別々に更新すると不整合が起きうるため、両方捨てて次の参照で読み直す)
func invalidateUserInfoCache(userID int64) {
	userInfoCache.Delete(userID)
	iconImageCache.Delete(userID)
}

// resetUserCaches は初期化時に呼び出して全キャッシュを捨てる
func resetUserCaches() {
	userInfoCache.Range(func(key, _ any) bool {
		userInfoCache.Delete(key)
		return true
	})
	userNameToIDCache.Range(func(key, _ any) bool {
		userNameToIDCache.Delete(key)
		return true
	})
	iconImageCache.Range(func(key, _ any) bool {
		iconImageCache.Delete(key)
		return true
	})
}
