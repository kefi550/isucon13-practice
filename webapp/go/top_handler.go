package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

type Tag struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type TagModel struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}

type TagsResponse struct {
	Tags []*Tag `json:"tags"`
}

// tagsはリクエスト処理中に書き込まれないため、起動時と初期化時にメモリへ読み込んで保持する
type tagCache struct {
	list []*TagModel // SELECT * FROM tags の順序のまま保持する
	byID map[int64]*TagModel
}

var tagCacheStore atomic.Pointer[tagCache]

func loadTagCache(ctx context.Context, db *sqlx.DB) error {
	var tagModels []*TagModel
	if err := db.SelectContext(ctx, &tagModels, "SELECT * FROM tags"); err != nil {
		return err
	}

	byID := make(map[int64]*TagModel, len(tagModels))
	for _, tagModel := range tagModels {
		byID[tagModel.ID] = tagModel
	}
	tagCacheStore.Store(&tagCache{list: tagModels, byID: byID})
	return nil
}

// 存在しないIDの場合はDBから引いたときと同じくsql.ErrNoRowsを返す
func getTagByID(id int64) (*TagModel, error) {
	tagModel, ok := tagCacheStore.Load().byID[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return tagModel, nil
}

func getTagHandler(c echo.Context) error {
	tagModels := tagCacheStore.Load().list

	tags := make([]*Tag, len(tagModels))
	for i := range tagModels {
		tags[i] = &Tag{
			ID:   tagModels[i].ID,
			Name: tagModels[i].Name,
		}
	}
	return c.JSON(http.StatusOK, &TagsResponse{
		Tags: tags,
	})
}

// 配信者のテーマ取得API
// GET /api/user/:username/theme
func getStreamerThemeHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		c.Logger().Printf("verifyUserSession: %+v\n", err)
		return err
	}

	username := c.Param("username")

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{}
	err = tx.GetContext(ctx, &userModel, "SELECT id FROM users WHERE name = ?", username)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	themeModel := ThemeModel{}
	if err := tx.GetContext(ctx, &themeModel, "SELECT * FROM themes WHERE user_id = ?", userModel.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user theme: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	theme := Theme{
		ID:       themeModel.ID,
		DarkMode: themeModel.DarkMode,
	}

	return c.JSON(http.StatusOK, theme)
}
