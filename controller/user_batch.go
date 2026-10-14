package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Batch user management mirrors the single-account contract: every mutation is
// step-up verified, and the proof binds the exact sorted, de-duplicated target
// set so a proof issued for one batch cannot be replayed against another.
// Whole-batch validation runs before the proof is consumed; an invalid member
// fails the entire request instead of silently skipping it.

type BatchManageUsersRequest struct {
	Ids    []int  `json:"ids"`
	Action string `json:"action"`
}

type BatchUserResult struct {
	AffectedUserIds []int `json:"affected_user_ids"`
	Affected        int   `json:"affected"`
	Total           int   `json:"total"`
}

// loadBatchManageTargets resolves and authorizes every requested user before
// any mutation or proof consumption takes place.
func loadBatchManageTargets(c *gin.Context, ids []int, action string) ([]*model.User, bool) {
	if !service.IsAdminUserBatchManageAction(action) {
		common.ApiErrorT(c, "Invalid parameters")
		return nil, false
	}
	userIDs, ok := service.NormalizeAdminUserBatchIDs(ids)
	if !ok {
		if len(ids) > service.MaxAdminUserBatchSize() {
			common.ApiErrorT(c, "Maximum 100 users can be managed in one batch")
			return nil, false
		}
		common.ApiErrorT(c, "No users selected")
		return nil, false
	}
	myRole := c.GetInt("role")
	operatorID := c.GetInt("id")
	targets := make([]*model.User, 0, len(userIDs))
	for _, id := range userIDs {
		if id == operatorID {
			common.ApiErrorT(c, "You cannot include your own account in a batch user action")
			return nil, false
		}
		user := model.User{Id: id}
		// Fill attributes, including soft-deleted rows, the way ManageUser does.
		if err := model.DB.Unscoped().Where(&user).First(&user).Error; err != nil {
			common.ApiErrorT(c, "User does not exist")
			return nil, false
		}
		if !canManageTargetRole(myRole, user.Role) {
			common.ApiErrorT(c, "No permission to update users of same or higher permission level")
			return nil, false
		}
		if user.Role == common.RoleRootUser {
			if action == "disable" {
				common.ApiErrorT(c, "Cannot disable super administrator user")
				return nil, false
			}
			common.ApiErrorT(c, "Cannot enable super administrator user")
			return nil, false
		}
		targets = append(targets, &user)
	}
	return targets, true
}

// BatchManageUsers disables or enables an authorized batch of users with a
// single `admin.user.batch_manage` proof.
func BatchManageUsers(c *gin.Context) {
	var req BatchManageUsersRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	targets, ok := loadBatchManageTargets(c, req.Ids, req.Action)
	if !ok {
		return
	}
	authorization := requireAdminUserProof(c, service.VerificationScopeAdminUserBatchManage, service.AdminUserBatchManageContext{UserIDs: req.Ids, Action: req.Action})
	if authorization == nil {
		return
	}

	newStatus := common.UserStatusDisabled
	if req.Action == "enable" {
		newStatus = common.UserStatusEnabled
	}
	var affected []*model.User
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		for _, user := range targets {
			if user.Status == newStatus {
				continue
			}
			patch := model.User{Id: user.Id, Status: newStatus}
			if err := patch.UpdateWithTx(tx, false); err != nil {
				return err
			}
			affected = append(affected, &patch)
		}
		return nil
	}); err != nil {
		common.ApiError(c, err)
		return
	}
	affectedIDs := make([]int, 0, len(affected))
	for _, user := range affected {
		// UpdateWithTx bumped the auth version inside the transaction. Finish
		// what the single-account User.Update does after commit: publish the
		// fresh state, revoke browser sessions, and drop relay token caches.
		if err := model.PublishUserAuthCache(user.Id); err != nil {
			common.ApiError(c, err)
			return
		}
		if _, err := model.RevokeAllUserSessions(user.Id, "user_security_changed"); err != nil {
			common.ApiError(c, err)
			return
		}
		if err := model.InvalidateUserTokensCache(user.Id); err != nil {
			common.SysLog(common.LogText("failed to invalidate tokens cache for user %d: %s", user.Id, err.Error()))
		}
		affectedIDs = append(affectedIDs, user.Id)
	}
	recordManageAudit(c, "user.manage_batch", map[string]any{
		"action":              req.Action,
		"count":               len(affectedIDs),
		"total":               len(targets),
		"requested_user_ids":  req.Ids,
		"affected_user_ids":   affectedIDs,
		"verification_method": authorization.Method,
	})
	common.ApiSuccess(c, BatchUserResult{AffectedUserIds: affectedIDs, Affected: len(affectedIDs), Total: len(targets)})
}

// BatchDeleteUsers permanently deletes an authorized batch of users with a
// single `admin.user.batch_delete` proof. The semantics match the
// single-account hard delete including the myRole <= targetRole rejection.
func BatchDeleteUsers(c *gin.Context) {
	var req struct {
		Ids []int `json:"ids"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	userIDs, ok := service.NormalizeAdminUserBatchIDs(req.Ids)
	if !ok {
		if len(req.Ids) > service.MaxAdminUserBatchSize() {
			common.ApiErrorT(c, "Maximum 100 users can be deleted in one batch")
			return
		}
		common.ApiErrorT(c, "No users selected")
		return
	}
	myRole := c.GetInt("role")
	operatorID := c.GetInt("id")
	targets := make([]int, 0, len(userIDs))
	for _, id := range userIDs {
		if id == operatorID {
			common.ApiErrorT(c, "You cannot include your own account in a batch user action")
			return
		}
		originUser, err := model.GetUserById(id, false)
		if err != nil {
			common.ApiErrorT(c, "User does not exist")
			return
		}
		// Deletion uses the stricter single-account rule, not canManageTargetRole.
		// It already protects the root account, since no operator has a role above it.
		if myRole <= originUser.Role {
			common.ApiErrorT(c, "No permission to update users of same or higher permission level")
			return
		}
		targets = append(targets, id)
	}
	authorization := requireAdminUserProof(c, service.VerificationScopeAdminUserBatchDelete, service.AdminUserBatchDeleteContext{UserIDs: req.Ids})
	if authorization == nil {
		return
	}

	deletedIDs := make([]int, 0, len(targets))
	var revokedAccessTokens int64
	for _, id := range targets {
		count, err := model.HardDeleteUserById(id)
		if err != nil {
			// Deletions are per-user transactions, so earlier targets are
			// already gone. Report what was actually removed before failing.
			recordManageAudit(c, "user.delete_batch", map[string]any{
				"count":                 len(deletedIDs),
				"total":                 len(targets),
				"requested_user_ids":    req.Ids,
				"deleted_user_ids":      deletedIDs,
				"verification_method":   authorization.Method,
				"revoked_access_tokens": revokedAccessTokens,
				"partial":               true,
			})
			common.SysError(common.LogText("failed to batch delete user %d: %s", id, err.Error()))
			common.ApiErrorT(c, "Failed to delete users")
			return
		}
		revokedAccessTokens += count
		deletedIDs = append(deletedIDs, id)
	}
	recordManageAudit(c, "user.delete_batch", map[string]any{
		"count":                 len(deletedIDs),
		"total":                 len(targets),
		"requested_user_ids":    req.Ids,
		"deleted_user_ids":      deletedIDs,
		"verification_method":   authorization.Method,
		"revoked_access_tokens": revokedAccessTokens,
	})
	common.ApiSuccess(c, BatchUserResult{AffectedUserIds: deletedIDs, Affected: len(deletedIDs), Total: len(targets)})
}
