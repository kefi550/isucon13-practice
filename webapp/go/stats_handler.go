package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

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

// 配信者ごとのリアクション数
type userReactionCount struct {
	UserID int64 `db:"user_id"`
	Count  int64 `db:"cnt"`
}

// 配信者ごとのライブコメント数とチップ合計
type userLivecommentSummary struct {
	UserID   int64 `db:"user_id"`
	Count    int64 `db:"cnt"`
	TotalTip int64 `db:"total_tip"`
}

// 配信ごとのリアクション数
type livestreamReactionCount struct {
	LivestreamID int64 `db:"livestream_id"`
	Count        int64 `db:"cnt"`
}

// 配信ごとのチップ合計と最大チップ額
type livestreamTipSummary struct {
	LivestreamID int64 `db:"livestream_id"`
	TotalTip     int64 `db:"total_tip"`
	MaxTip       int64 `db:"max_tip"`
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

	// 全配信者のリアクション数・チップ合計をそれぞれ1クエリで集計する
	var reactionCounts []userReactionCount
	if err := tx.SelectContext(ctx, &reactionCounts, `
		SELECT l.user_id, COUNT(*) AS cnt FROM livestreams l
		INNER JOIN reactions r ON r.livestream_id = l.id
		GROUP BY l.user_id`); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count reactions: "+err.Error())
	}
	var livecommentSummaries []userLivecommentSummary
	if err := tx.SelectContext(ctx, &livecommentSummaries, `
		SELECT l.user_id, COUNT(*) AS cnt, IFNULL(SUM(l2.tip), 0) AS total_tip FROM livestreams l
		INNER JOIN livecomments l2 ON l2.livestream_id = l.id
		GROUP BY l.user_id`); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count tips: "+err.Error())
	}

	reactionsByUser := make(map[int64]int64, len(reactionCounts))
	for _, r := range reactionCounts {
		reactionsByUser[r.UserID] = r.Count
	}
	tipsByUser := make(map[int64]int64, len(livecommentSummaries))
	livecommentsByUser := make(map[int64]int64, len(livecommentSummaries))
	for _, s := range livecommentSummaries {
		tipsByUser[s.UserID] = s.TotalTip
		livecommentsByUser[s.UserID] = s.Count
	}

	// ランク算出
	// スコア(リアクション数+チップ合計)の降順、同点ならユーザ名の降順で並べたときの順位
	var users []*UserModel
	if err := tx.SelectContext(ctx, &users, "SELECT id, name FROM users"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get users: "+err.Error())
	}
	myScore := reactionsByUser[user.ID] + tipsByUser[user.ID]
	var rank int64 = 1
	for _, u := range users {
		score := reactionsByUser[u.ID] + tipsByUser[u.ID]
		if score > myScore || (score == myScore && u.Name > user.Name) {
			rank++
		}
	}

	// 合計視聴者数
	var viewersCount int64
	if err := tx.GetContext(ctx, &viewersCount, `
		SELECT COUNT(*) FROM livestreams l
		INNER JOIN livestream_viewers_history h ON h.livestream_id = l.id
		WHERE l.user_id = ?`, user.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream_view_history: "+err.Error())
	}

	// お気に入り絵文字
	var favoriteEmoji string
	query := `
	SELECT r.emoji_name
	FROM livestreams l
	INNER JOIN reactions r ON r.livestream_id = l.id
	WHERE l.user_id = ?
	GROUP BY emoji_name
	ORDER BY COUNT(*) DESC, emoji_name DESC
	LIMIT 1
	`
	if err := tx.GetContext(ctx, &favoriteEmoji, query, user.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to find favorite emoji: "+err.Error())
	}

	stats := UserStatistics{
		Rank:              rank,
		ViewersCount:      viewersCount,
		TotalReactions:    reactionsByUser[user.ID],
		TotalLivecomments: livecommentsByUser[user.ID],
		TotalTip:          tipsByUser[user.ID],
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

	// 全配信のリアクション数・チップ合計(と最大チップ額)をそれぞれ1クエリで集計する
	var reactionCounts []livestreamReactionCount
	if err := tx.SelectContext(ctx, &reactionCounts, "SELECT livestream_id, COUNT(*) AS cnt FROM reactions GROUP BY livestream_id"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count reactions: "+err.Error())
	}
	var tipSummaries []livestreamTipSummary
	if err := tx.SelectContext(ctx, &tipSummaries, "SELECT livestream_id, IFNULL(SUM(tip), 0) AS total_tip, IFNULL(MAX(tip), 0) AS max_tip FROM livecomments GROUP BY livestream_id"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count tips: "+err.Error())
	}

	reactionsByLivestream := make(map[int64]int64, len(reactionCounts))
	for _, r := range reactionCounts {
		reactionsByLivestream[r.LivestreamID] = r.Count
	}
	tipsByLivestream := make(map[int64]int64, len(tipSummaries))
	var maxTip int64
	for _, s := range tipSummaries {
		tipsByLivestream[s.LivestreamID] = s.TotalTip
		if s.LivestreamID == livestreamID {
			maxTip = s.MaxTip
		}
	}

	// ランク算出
	// スコア(リアクション数+チップ合計)の降順、同点ならIDの降順で並べたときの順位
	var livestreamIDs []int64
	if err := tx.SelectContext(ctx, &livestreamIDs, "SELECT id FROM livestreams"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}
	myScore := reactionsByLivestream[livestreamID] + tipsByLivestream[livestreamID]
	var rank int64 = 1
	for _, lid := range livestreamIDs {
		score := reactionsByLivestream[lid] + tipsByLivestream[lid]
		if score > myScore || (score == myScore && lid > livestreamID) {
			rank++
		}
	}

	// 視聴者数算出
	var viewersCount int64
	if err := tx.GetContext(ctx, &viewersCount, "SELECT COUNT(*) FROM livestream_viewers_history WHERE livestream_id = ?", livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count livestream viewers: "+err.Error())
	}

	// スパム報告数
	var totalReports int64
	if err := tx.GetContext(ctx, &totalReports, "SELECT COUNT(*) FROM livecomment_reports WHERE livestream_id = ?", livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count total spam reports: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, LivestreamStatistics{
		Rank:           rank,
		ViewersCount:   viewersCount,
		MaxTip:         maxTip,
		TotalReactions: reactionsByLivestream[livestreamID],
		TotalReports:   totalReports,
	})
}
