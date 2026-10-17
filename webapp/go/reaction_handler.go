package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

type ReactionModel struct {
	ID           int64  `db:"id"`
	EmojiName    string `db:"emoji_name"`
	UserID       int64  `db:"user_id"`
	LivestreamID int64  `db:"livestream_id"`
	CreatedAt    int64  `db:"created_at"`
}

type Reaction struct {
	ID         int64      `json:"id"`
	EmojiName  string     `json:"emoji_name"`
	User       User       `json:"user"`
	Livestream Livestream `json:"livestream"`
	CreatedAt  int64      `json:"created_at"`
}

type PostReactionRequest struct {
	EmojiName string `json:"emoji_name"`
}

func getReactionsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	query := "SELECT * FROM reactions WHERE livestream_id = ? ORDER BY created_at DESC"
	if c.QueryParam("limit") != "" {
		limit, err := strconv.Atoi(c.QueryParam("limit"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
		}
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	var reactionModels []*ReactionModel
	if err := dbConn.SelectContext(ctx, &reactionModels, query, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "failed to get reactions")
	}

	reactions, err := fillReactionResponses(ctx, dbConn, reactionModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill reaction: "+err.Error())
	}

	return c.JSON(http.StatusOK, reactions)
}

func postReactionHandler(c echo.Context) error {
	ctx := c.Request().Context()
	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *PostReactionRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	reactionModel := ReactionModel{
		UserID:       int64(userID),
		LivestreamID: int64(livestreamID),
		EmojiName:    req.EmojiName,
		CreatedAt:    time.Now().Unix(),
	}

	result, err := tx.NamedExecContext(ctx, "INSERT INTO reactions (user_id, livestream_id, emoji_name, created_at) VALUES (:user_id, :livestream_id, :emoji_name, :created_at)", reactionModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert reaction: "+err.Error())
	}

	reactionID, err := result.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted reaction id: "+err.Error())
	}
	reactionModel.ID = reactionID

	reaction, err := fillReactionResponse(ctx, tx, reactionModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill reaction: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusCreated, reaction)
}

func fillReactionResponse(ctx context.Context, q sqlx.QueryerContext, reactionModel ReactionModel) (Reaction, error) {
	reactions, err := fillReactionResponses(ctx, q, []*ReactionModel{&reactionModel})
	if err != nil {
		return Reaction{}, err
	}
	return reactions[0], nil
}

// fillReactionResponses は複数のリアクションレスポンスを IN 句でまとめて組み立てる (N+1解消)
func fillReactionResponses(ctx context.Context, q sqlx.QueryerContext, reactionModels []*ReactionModel) ([]Reaction, error) {
	if len(reactionModels) == 0 {
		return []Reaction{}, nil
	}

	userIDs := make([]int64, 0, len(reactionModels))
	livestreamIDs := make([]int64, 0, len(reactionModels))
	for _, reactionModel := range reactionModels {
		userIDs = append(userIDs, reactionModel.UserID)
		livestreamIDs = append(livestreamIDs, reactionModel.LivestreamID)
	}

	users, err := fetchUsersByIDs(ctx, q, userIDs)
	if err != nil {
		return nil, err
	}
	userResponses := make(map[int64]User, len(users))
	for userID, userModel := range users {
		user, err := fillUserResponse(ctx, q, *userModel)
		if err != nil {
			return nil, err
		}
		userResponses[userID] = user
	}

	// 配信は重複を除いてまとめて取得し、元の順序に合わせて割り当てる
	uniqueLivestreamIDs := distinctInt64s(livestreamIDs)
	livestreamModels, err := fetchLivestreamsByIDs(ctx, q, uniqueLivestreamIDs)
	if err != nil {
		return nil, err
	}
	livestreamModelSlice := make([]*LivestreamModel, 0, len(livestreamModels))
	for _, livestreamID := range uniqueLivestreamIDs {
		livestreamModel, ok := livestreamModels[livestreamID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		livestreamModelSlice = append(livestreamModelSlice, livestreamModel)
	}
	livestreamResponses, err := fillLivestreamResponses(ctx, q, livestreamModelSlice)
	if err != nil {
		return nil, err
	}
	livestreamsByID := make(map[int64]Livestream, len(livestreamModelSlice))
	for i, livestreamModel := range livestreamModelSlice {
		livestreamsByID[livestreamModel.ID] = livestreamResponses[i]
	}

	reactions := make([]Reaction, len(reactionModels))
	for i, reactionModel := range reactionModels {
		user, ok := userResponses[reactionModel.UserID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		livestream, ok := livestreamsByID[reactionModel.LivestreamID]
		if !ok {
			return nil, sql.ErrNoRows
		}

		reactions[i] = Reaction{
			ID:         reactionModel.ID,
			EmojiName:  reactionModel.EmojiName,
			User:       user,
			Livestream: livestream,
			CreatedAt:  reactionModel.CreatedAt,
		}
	}
	return reactions, nil
}
