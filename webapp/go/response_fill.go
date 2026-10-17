package main

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

// N+1 クエリを避けるため、関連レコードを IN でまとめて取得してレスポンスを組み立てる。

func distinctIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return ids
	}
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

// loadUsers は users + themes + icons をまとめて取得し、レスポンス用の User を返す。
func loadUsers(ctx context.Context, q sqlx.QueryerContext, userIDs []int64) (map[int64]User, error) {
	result := make(map[int64]User, len(userIDs))
	ids := distinctIDs(userIDs)
	if len(ids) == 0 {
		return result, nil
	}

	var userModels []UserModel
	query, args, err := sqlx.In("SELECT * FROM users WHERE id IN (?)", ids)
	if err != nil {
		return nil, err
	}
	if err := sqlx.SelectContext(ctx, q, &userModels, query, args...); err != nil {
		return nil, err
	}

	var themeModels []ThemeModel
	query, args, err = sqlx.In("SELECT * FROM themes WHERE user_id IN (?) ORDER BY id", ids)
	if err != nil {
		return nil, err
	}
	if err := sqlx.SelectContext(ctx, q, &themeModels, query, args...); err != nil {
		return nil, err
	}
	themes := make(map[int64]ThemeModel, len(themeModels))
	for i := range themeModels {
		// 同一ユーザーに複数行ある場合は若い id を採用(元実装の暗黙の先頭行取得に合わせる)
		if _, ok := themes[themeModels[i].UserID]; !ok {
			themes[themeModels[i].UserID] = themeModels[i]
		}
	}

	icons, err := loadIcons(ctx, q, ids)
	if err != nil {
		return nil, err
	}

	for i := range userModels {
		userModel := &userModels[i]
		themeModel, ok := themes[userModel.ID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		result[userModel.ID] = User{
			ID:          userModel.ID,
			Name:        userModel.Name,
			DisplayName: userModel.DisplayName,
			Description: userModel.Description,
			Theme: Theme{
				ID:       themeModel.ID,
				DarkMode: themeModel.DarkMode,
			},
			IconHash: icons[userModel.ID].hash,
		}
	}

	return result, nil
}

// loadLivestreams は livestreams + owner + tags をまとめて取得する。
func loadLivestreams(ctx context.Context, q sqlx.QueryerContext, livestreamIDs []int64) (map[int64]Livestream, error) {
	result := make(map[int64]Livestream, len(livestreamIDs))
	ids := distinctIDs(livestreamIDs)
	if len(ids) == 0 {
		return result, nil
	}

	var livestreamModels []LivestreamModel
	query, args, err := sqlx.In("SELECT * FROM livestreams WHERE id IN (?)", ids)
	if err != nil {
		return nil, err
	}
	if err := sqlx.SelectContext(ctx, q, &livestreamModels, query, args...); err != nil {
		return nil, err
	}
	if len(livestreamModels) == 0 {
		return result, nil
	}

	ownerIDs := make([]int64, 0, len(livestreamModels))
	for i := range livestreamModels {
		ownerIDs = append(ownerIDs, livestreamModels[i].UserID)
	}
	owners, err := loadUsers(ctx, q, ownerIDs)
	if err != nil {
		return nil, err
	}

	var livestreamTagModels []LivestreamTagModel
	query, args, err = sqlx.In("SELECT * FROM livestream_tags WHERE livestream_id IN (?) ORDER BY id", ids)
	if err != nil {
		return nil, err
	}
	if err := sqlx.SelectContext(ctx, q, &livestreamTagModels, query, args...); err != nil {
		return nil, err
	}

	tagsByID := make(map[int64]Tag, len(livestreamTagModels))
	for i := range livestreamTagModels {
		tagID := livestreamTagModels[i].TagID
		if _, ok := tagsByID[tagID]; ok {
			continue
		}
		// tags はオンメモリキャッシュから引く
		tagModel, err := getTagByID(tagID)
		if err != nil {
			return nil, err
		}
		tagsByID[tagID] = Tag{ID: tagModel.ID, Name: tagModel.Name}
	}

	tagsByLivestream := make(map[int64][]LivestreamTagModel, len(livestreamModels))
	for i := range livestreamTagModels {
		lt := livestreamTagModels[i]
		tagsByLivestream[lt.LivestreamID] = append(tagsByLivestream[lt.LivestreamID], lt)
	}

	for i := range livestreamModels {
		livestreamModel := &livestreamModels[i]
		owner, ok := owners[livestreamModel.UserID]
		if !ok {
			return nil, sql.ErrNoRows
		}

		livestreamTagModels := tagsByLivestream[livestreamModel.ID]
		tags := make([]Tag, 0, len(livestreamTagModels))
		for _, livestreamTagModel := range livestreamTagModels {
			tag, ok := tagsByID[livestreamTagModel.TagID]
			if !ok {
				return nil, sql.ErrNoRows
			}
			tags = append(tags, tag)
		}

		result[livestreamModel.ID] = Livestream{
			ID:           livestreamModel.ID,
			Owner:        owner,
			Title:        livestreamModel.Title,
			Description:  livestreamModel.Description,
			PlaylistUrl:  livestreamModel.PlaylistUrl,
			ThumbnailUrl: livestreamModel.ThumbnailUrl,
			Tags:         tags,
			StartAt:      livestreamModel.StartAt,
			EndAt:        livestreamModel.EndAt,
		}
	}

	return result, nil
}

// loadLivecomments は livecomments + user + livestream をまとめて取得する。
func loadLivecomments(ctx context.Context, q sqlx.QueryerContext, livecommentIDs []int64) (map[int64]Livecomment, error) {
	result := make(map[int64]Livecomment, len(livecommentIDs))
	ids := distinctIDs(livecommentIDs)
	if len(ids) == 0 {
		return result, nil
	}

	var livecommentModels []LivecommentModel
	query, args, err := sqlx.In("SELECT * FROM livecomments WHERE id IN (?)", ids)
	if err != nil {
		return nil, err
	}
	if err := sqlx.SelectContext(ctx, q, &livecommentModels, query, args...); err != nil {
		return nil, err
	}
	if len(livecommentModels) == 0 {
		return result, nil
	}

	userIDs := make([]int64, 0, len(livecommentModels))
	livestreamIDs := make([]int64, 0, len(livecommentModels))
	for i := range livecommentModels {
		userIDs = append(userIDs, livecommentModels[i].UserID)
		livestreamIDs = append(livestreamIDs, livecommentModels[i].LivestreamID)
	}

	users, err := loadUsers(ctx, q, userIDs)
	if err != nil {
		return nil, err
	}
	livestreams, err := loadLivestreams(ctx, q, livestreamIDs)
	if err != nil {
		return nil, err
	}

	for i := range livecommentModels {
		livecommentModel := &livecommentModels[i]
		user, ok := users[livecommentModel.UserID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		livestream, ok := livestreams[livecommentModel.LivestreamID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		result[livecommentModel.ID] = Livecomment{
			ID:         livecommentModel.ID,
			User:       user,
			Livestream: livestream,
			Comment:    livecommentModel.Comment,
			Tip:        livecommentModel.Tip,
			CreatedAt:  livecommentModel.CreatedAt,
		}
	}

	return result, nil
}

// loadReactions は reactions + user + livestream をまとめて取得する。
func loadReactions(ctx context.Context, q sqlx.QueryerContext, reactionIDs []int64) (map[int64]Reaction, error) {
	result := make(map[int64]Reaction, len(reactionIDs))
	ids := distinctIDs(reactionIDs)
	if len(ids) == 0 {
		return result, nil
	}

	var reactionModels []ReactionModel
	query, args, err := sqlx.In("SELECT * FROM reactions WHERE id IN (?)", ids)
	if err != nil {
		return nil, err
	}
	if err := sqlx.SelectContext(ctx, q, &reactionModels, query, args...); err != nil {
		return nil, err
	}
	if len(reactionModels) == 0 {
		return result, nil
	}

	userIDs := make([]int64, 0, len(reactionModels))
	livestreamIDs := make([]int64, 0, len(reactionModels))
	for i := range reactionModels {
		userIDs = append(userIDs, reactionModels[i].UserID)
		livestreamIDs = append(livestreamIDs, reactionModels[i].LivestreamID)
	}

	users, err := loadUsers(ctx, q, userIDs)
	if err != nil {
		return nil, err
	}
	livestreams, err := loadLivestreams(ctx, q, livestreamIDs)
	if err != nil {
		return nil, err
	}

	for i := range reactionModels {
		reactionModel := &reactionModels[i]
		user, ok := users[reactionModel.UserID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		livestream, ok := livestreams[reactionModel.LivestreamID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		result[reactionModel.ID] = Reaction{
			ID:         reactionModel.ID,
			EmojiName:  reactionModel.EmojiName,
			User:       user,
			Livestream: livestream,
			CreatedAt:  reactionModel.CreatedAt,
		}
	}

	return result, nil
}

// loadLivecommentReports は livecomment_reports + reporter + livecomment をまとめて取得する。
func loadLivecommentReports(ctx context.Context, q sqlx.QueryerContext, reportIDs []int64) (map[int64]LivecommentReport, error) {
	result := make(map[int64]LivecommentReport, len(reportIDs))
	ids := distinctIDs(reportIDs)
	if len(ids) == 0 {
		return result, nil
	}

	var reportModels []LivecommentReportModel
	query, args, err := sqlx.In("SELECT * FROM livecomment_reports WHERE id IN (?)", ids)
	if err != nil {
		return nil, err
	}
	if err := sqlx.SelectContext(ctx, q, &reportModels, query, args...); err != nil {
		return nil, err
	}
	if len(reportModels) == 0 {
		return result, nil
	}

	userIDs := make([]int64, 0, len(reportModels))
	livecommentIDs := make([]int64, 0, len(reportModels))
	for i := range reportModels {
		userIDs = append(userIDs, reportModels[i].UserID)
		livecommentIDs = append(livecommentIDs, reportModels[i].LivecommentID)
	}

	users, err := loadUsers(ctx, q, userIDs)
	if err != nil {
		return nil, err
	}
	livecomments, err := loadLivecomments(ctx, q, livecommentIDs)
	if err != nil {
		return nil, err
	}

	for i := range reportModels {
		reportModel := &reportModels[i]
		reporter, ok := users[reportModel.UserID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		livecomment, ok := livecomments[reportModel.LivecommentID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		result[reportModel.ID] = LivecommentReport{
			ID:          reportModel.ID,
			Reporter:    reporter,
			Livecomment: livecomment,
			CreatedAt:   reportModel.CreatedAt,
		}
	}

	return result, nil
}

func fillUserResponses(ctx context.Context, q sqlx.QueryerContext, userModels []UserModel) ([]User, error) {
	ids := make([]int64, 0, len(userModels))
	for i := range userModels {
		ids = append(ids, userModels[i].ID)
	}
	users, err := loadUsers(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	result := make([]User, len(userModels))
	for i := range userModels {
		user, ok := users[userModels[i].ID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		result[i] = user
	}
	return result, nil
}

func fillUserResponse(ctx context.Context, q sqlx.QueryerContext, userModel UserModel) (User, error) {
	users, err := fillUserResponses(ctx, q, []UserModel{userModel})
	if err != nil {
		return User{}, err
	}
	return users[0], nil
}

func fillLivestreamResponses(ctx context.Context, q sqlx.QueryerContext, livestreamModels []LivestreamModel) ([]Livestream, error) {
	ids := make([]int64, 0, len(livestreamModels))
	for i := range livestreamModels {
		ids = append(ids, livestreamModels[i].ID)
	}
	livestreams, err := loadLivestreams(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	result := make([]Livestream, len(livestreamModels))
	for i := range livestreamModels {
		livestream, ok := livestreams[livestreamModels[i].ID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		result[i] = livestream
	}
	return result, nil
}

func fillLivestreamResponse(ctx context.Context, q sqlx.QueryerContext, livestreamModel LivestreamModel) (Livestream, error) {
	livestreams, err := fillLivestreamResponses(ctx, q, []LivestreamModel{livestreamModel})
	if err != nil {
		return Livestream{}, err
	}
	return livestreams[0], nil
}

func fillLivecommentResponses(ctx context.Context, q sqlx.QueryerContext, livecommentModels []LivecommentModel) ([]Livecomment, error) {
	ids := make([]int64, 0, len(livecommentModels))
	for i := range livecommentModels {
		ids = append(ids, livecommentModels[i].ID)
	}
	livecomments, err := loadLivecomments(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	result := make([]Livecomment, len(livecommentModels))
	for i := range livecommentModels {
		livecomment, ok := livecomments[livecommentModels[i].ID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		result[i] = livecomment
	}
	return result, nil
}

func fillLivecommentResponse(ctx context.Context, q sqlx.QueryerContext, livecommentModel LivecommentModel) (Livecomment, error) {
	livecomments, err := fillLivecommentResponses(ctx, q, []LivecommentModel{livecommentModel})
	if err != nil {
		return Livecomment{}, err
	}
	return livecomments[0], nil
}

func fillReactionResponses(ctx context.Context, q sqlx.QueryerContext, reactionModels []ReactionModel) ([]Reaction, error) {
	ids := make([]int64, 0, len(reactionModels))
	for i := range reactionModels {
		ids = append(ids, reactionModels[i].ID)
	}
	reactions, err := loadReactions(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	result := make([]Reaction, len(reactionModels))
	for i := range reactionModels {
		reaction, ok := reactions[reactionModels[i].ID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		result[i] = reaction
	}
	return result, nil
}

func fillReactionResponse(ctx context.Context, q sqlx.QueryerContext, reactionModel ReactionModel) (Reaction, error) {
	reactions, err := fillReactionResponses(ctx, q, []ReactionModel{reactionModel})
	if err != nil {
		return Reaction{}, err
	}
	return reactions[0], nil
}

func fillLivecommentReportResponses(ctx context.Context, q sqlx.QueryerContext, reportModels []LivecommentReportModel) ([]LivecommentReport, error) {
	ids := make([]int64, 0, len(reportModels))
	for i := range reportModels {
		ids = append(ids, reportModels[i].ID)
	}
	reports, err := loadLivecommentReports(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	result := make([]LivecommentReport, len(reportModels))
	for i := range reportModels {
		report, ok := reports[reportModels[i].ID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		result[i] = report
	}
	return result, nil
}

func fillLivecommentReportResponse(ctx context.Context, q sqlx.QueryerContext, reportModel LivecommentReportModel) (LivecommentReport, error) {
	reports, err := fillLivecommentReportResponses(ctx, q, []LivecommentReportModel{reportModel})
	if err != nil {
		return LivecommentReport{}, err
	}
	return reports[0], nil
}
