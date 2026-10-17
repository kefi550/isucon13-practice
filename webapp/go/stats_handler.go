package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

type LivestreamStatistics struct {
	Rank           int64 `json:"rank"`
	ViewersCount   int64 `json:"viewers_count"`
	TotalReactions int64 `json:"total_reactions"`
	TotalReports   int64 `json:"total_reports"`
	MaxTip         int64 `json:"max_tip"`
}

type UserStatistics struct {
	Rank              int64  `json:"rank"`
	ViewersCount      int64  `json:"viewers_count"`
	TotalReactions    int64  `json:"total_reactions"`
	TotalLivecomments int64  `json:"total_livecomments"`
	TotalTip          int64  `json:"total_tip"`
	FavoriteEmoji     string `json:"favorite_emoji"`
}

// countReactionsByUser はユーザごとのリアクション数をまとめて集計する。
func countReactionsByUser(ctx context.Context, q sqlx.QueryerContext) (map[int64]int64, error) {
	var rows []struct {
		UserID int64 `db:"user_id"`
		Count  int64 `db:"cnt"`
	}
	if err := sqlx.SelectContext(ctx, q, &rows, "SELECT l.user_id AS user_id, COUNT(*) AS cnt FROM livestreams l INNER JOIN reactions r ON r.livestream_id = l.id GROUP BY l.user_id"); err != nil {
		return nil, err
	}
	result := make(map[int64]int64, len(rows))
	for i := range rows {
		result[rows[i].UserID] = rows[i].Count
	}
	return result, nil
}

// sumTipsByUser はユーザごとのチップ合計をまとめて集計する。
func sumTipsByUser(ctx context.Context, q sqlx.QueryerContext) (map[int64]int64, error) {
	var rows []struct {
		UserID int64 `db:"user_id"`
		Tip    int64 `db:"tip"`
	}
	if err := sqlx.SelectContext(ctx, q, &rows, "SELECT l.user_id AS user_id, IFNULL(SUM(lc.tip), 0) AS tip FROM livestreams l INNER JOIN livecomments lc ON lc.livestream_id = l.id GROUP BY l.user_id"); err != nil {
		return nil, err
	}
	result := make(map[int64]int64, len(rows))
	for i := range rows {
		result[rows[i].UserID] = rows[i].Tip
	}
	return result, nil
}

// countReactionsByLivestream はライブ配信ごとのリアクション数をまとめて集計する。
func countReactionsByLivestream(ctx context.Context, q sqlx.QueryerContext) (map[int64]int64, error) {
	var rows []struct {
		LivestreamID int64 `db:"livestream_id"`
		Count        int64 `db:"cnt"`
	}
	if err := sqlx.SelectContext(ctx, q, &rows, "SELECT livestream_id, COUNT(*) AS cnt FROM reactions GROUP BY livestream_id"); err != nil {
		return nil, err
	}
	result := make(map[int64]int64, len(rows))
	for i := range rows {
		result[rows[i].LivestreamID] = rows[i].Count
	}
	return result, nil
}

// sumTipsByLivestream はライブ配信ごとのチップ合計をまとめて集計する。
func sumTipsByLivestream(ctx context.Context, q sqlx.QueryerContext) (map[int64]int64, error) {
	var rows []struct {
		LivestreamID int64 `db:"livestream_id"`
		Tip          int64 `db:"tip"`
	}
	if err := sqlx.SelectContext(ctx, q, &rows, "SELECT livestream_id, IFNULL(SUM(tip), 0) AS tip FROM livecomments GROUP BY livestream_id"); err != nil {
		return nil, err
	}
	result := make(map[int64]int64, len(rows))
	for i := range rows {
		result[rows[i].LivestreamID] = rows[i].Tip
	}
	return result, nil
}

func getUserStatisticsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	username := c.Param("username")
	// ユーザごとに、紐づく配信について、累計リアクション数、累計ライブコメント数、累計売上金額を算出
	// また、現在の合計視聴者数もだす

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var user UserModel
	if err := tx.GetContext(ctx, &user, "SELECT * FROM users WHERE name = ?", username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusBadRequest, "not found user that has the given username")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
		}
	}

	// ランク算出
	// NOTE: 元実装はユーザごとにクエリを発行していたため、集計クエリをまとめる
	var users []UserModel
	if err := tx.SelectContext(ctx, &users, "SELECT id, name FROM users"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get users: "+err.Error())
	}

	reactionCounts, err := countReactionsByUser(ctx, tx)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count reactions: "+err.Error())
	}
	tipTotals, err := sumTipsByUser(ctx, tx)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count tips: "+err.Error())
	}

	var myScore int64
	rank := int64(1)
	for i := range users {
		score := reactionCounts[users[i].ID] + tipTotals[users[i].ID]
		if users[i].Name == username {
			myScore = score
		}
	}
	// スコア降順(同スコアはユーザ名降順)で自身より前に並ぶユーザ数を数える
	for i := range users {
		score := reactionCounts[users[i].ID] + tipTotals[users[i].ID]
		if score > myScore || (score == myScore && users[i].Name > username) {
			rank++
		}
	}

	// リアクション数
	var totalReactions int64
	query := `SELECT COUNT(*) FROM livestreams l
    INNER JOIN reactions r ON r.livestream_id = l.id
    WHERE l.user_id = ?
	`
	if err := tx.GetContext(ctx, &totalReactions, query, user.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count total reactions: "+err.Error())
	}

	// ライブコメント数、チップ合計
	var totalLivecomments int64
	var totalTip int64
	query = `SELECT COUNT(*), IFNULL(SUM(lc.tip), 0) FROM livestreams l
	INNER JOIN livecomments lc ON lc.livestream_id = l.id
	WHERE l.user_id = ?
	`
	if err := tx.QueryRowxContext(ctx, query, user.ID).Scan(&totalLivecomments, &totalTip); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomments: "+err.Error())
	}

	// 合計視聴者数
	var viewersCount int64
	query = `SELECT COUNT(*) FROM livestreams l
	INNER JOIN livestream_viewers_history h ON h.livestream_id = l.id
	WHERE l.user_id = ?
	`
	if err := tx.GetContext(ctx, &viewersCount, query, user.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream_view_history: "+err.Error())
	}

	// お気に入り絵文字
	var favoriteEmoji string
	query = `
	SELECT r.emoji_name
	FROM livestreams l
	INNER JOIN reactions r ON r.livestream_id = l.id
	WHERE l.user_id = ?
	GROUP BY r.emoji_name
	ORDER BY COUNT(*) DESC, r.emoji_name DESC
	LIMIT 1
	`
	if err := tx.GetContext(ctx, &favoriteEmoji, query, user.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to find favorite emoji: "+err.Error())
	}

	stats := UserStatistics{
		Rank:              rank,
		ViewersCount:      viewersCount,
		TotalReactions:    totalReactions,
		TotalLivecomments: totalLivecomments,
		TotalTip:          totalTip,
		FavoriteEmoji:     favoriteEmoji,
	}
	return c.JSON(http.StatusOK, stats)
}

func getLivestreamStatisticsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	id, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	livestreamID := int64(id)

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var livestream LivestreamModel
	if err := tx.GetContext(ctx, &livestream, "SELECT * FROM livestreams WHERE id = ?", livestreamID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusBadRequest, "cannot get stats of not found livestream")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
		}
	}

	// ランク算出
	// NOTE: 元実装はライブ配信ごとにクエリを発行していたため、集計クエリをまとめる
	var livestreamIDs []int64
	if err := tx.SelectContext(ctx, &livestreamIDs, "SELECT id FROM livestreams"); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}

	reactionCounts, err := countReactionsByLivestream(ctx, tx)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count reactions: "+err.Error())
	}
	tipTotals, err := sumTipsByLivestream(ctx, tx)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count tips: "+err.Error())
	}

	var myScore int64
	rank := int64(1)
	for _, lsID := range livestreamIDs {
		if lsID == livestreamID {
			myScore = reactionCounts[lsID] + tipTotals[lsID]
			break
		}
	}
	// スコア降順(同スコアはID降順)で自身より前に並ぶライブ配信数を数える
	for _, lsID := range livestreamIDs {
		score := reactionCounts[lsID] + tipTotals[lsID]
		if score > myScore || (score == myScore && lsID > livestreamID) {
			rank++
		}
	}

	// 視聴者数算出
	var viewersCount int64
	if err := tx.GetContext(ctx, &viewersCount, `SELECT COUNT(*) FROM livestream_viewers_history WHERE livestream_id = ?`, livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count livestream viewers: "+err.Error())
	}

	// 最大チップ額
	var maxTip int64
	if err := tx.GetContext(ctx, &maxTip, `SELECT IFNULL(MAX(tip), 0) FROM livecomments WHERE livestream_id = ?`, livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to find maximum tip livecomment: "+err.Error())
	}

	// リアクション数
	totalReactions := reactionCounts[livestreamID]

	// スパム報告数
	var totalReports int64
	if err := tx.GetContext(ctx, &totalReports, `SELECT COUNT(*) FROM livecomment_reports WHERE livestream_id = ?`, livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count total spam reports: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, LivestreamStatistics{
		Rank:           rank,
		ViewersCount:   viewersCount,
		MaxTip:         maxTip,
		TotalReactions: totalReactions,
		TotalReports:   totalReports,
	})
}
