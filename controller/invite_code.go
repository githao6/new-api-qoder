package controller

import (
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

const maxInviteCodeBatchSize = 100

// GetAllInviteCodes lists invite codes for the admin console.
func GetAllInviteCodes(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	inviteCodes, total, err := model.GetAllInviteCodes(pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(inviteCodes)
	common.ApiSuccess(c, pageInfo)
}

// SearchInviteCodes filters invite codes by keyword (id, name or key) and status.
func SearchInviteCodes(c *gin.Context) {
	keyword := c.Query("keyword")
	status := c.Query("status")
	pageInfo := common.GetPageQuery(c)
	inviteCodes, total, err := model.SearchInviteCodes(keyword, status, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(inviteCodes)
	common.ApiSuccess(c, pageInfo)
}

func GetInviteCode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	inviteCode, err := model.GetInviteCodeById(id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, inviteCode)
}

type addInviteCodeRequest struct {
	Name        string `json:"name"`
	Count       int    `json:"count"`
	ExpiredTime int64  `json:"expired_time"`
}

// AddInviteCodes generates invite codes. Each code registers exactly one
// account, so count controls how many single-use codes are created.
func AddInviteCodes(c *gin.Context) {
	var request addInviteCodeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	if utf8.RuneCountInString(request.Name) == 0 || utf8.RuneCountInString(request.Name) > 20 {
		common.ApiErrorT(c, "Invite code name length must be between 1-20")
		return
	}
	if request.Count <= 0 {
		common.ApiErrorT(c, "Invite code count must be greater than 0")
		return
	}
	if request.Count > maxInviteCodeBatchSize {
		common.ApiErrorStatus(c, http.StatusOK, common.NewMessage("Maximum {{max}} invite codes can be generated at once", map[string]any{"max": maxInviteCodeBatchSize}), gin.H{"max": maxInviteCodeBatchSize})
		return
	}
	if err := validateExpiredTime(request.ExpiredTime); err != nil {
		common.ApiError(c, err)
		return
	}
	var keys []string
	for i := 0; i < request.Count; i++ {
		cleanInviteCode := model.InviteCode{
			UserId:      c.GetInt("id"),
			Name:        request.Name,
			Key:         common.GetUUID(),
			Status:      common.InviteCodeStatusEnabled,
			CreatedTime: common.GetTimestamp(),
			ExpiredTime: request.ExpiredTime,
		}
		if err := cleanInviteCode.Insert(); err != nil {
			common.SysError(common.LogText("failed to insert invite code: %s", err.Error()))
			common.ApiErrorStatus(c, http.StatusOK, common.NewMessage("Failed to create invite code, please try again later"), gin.H{"data": keys})
			return
		}
		keys = append(keys, cleanInviteCode.Key)
	}
	recordManageAudit(c, "invite_code.create", map[string]any{
		"name":  request.Name,
		"count": len(keys),
	})
	common.ApiSuccess(c, keys)
}

func DeleteInviteCode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	inviteCode, err := model.GetInviteCodeById(id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if inviteCode.Status == common.InviteCodeStatusUsed {
		common.ApiErrorT(c, "Cannot delete an invite code that has already been used")
		return
	}
	if err := inviteCode.Delete(); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "invite_code.delete", map[string]any{"id": id})
	common.ApiSuccess(c, nil)
}

type updateInviteCodeRequest struct {
	Id          int    `json:"id"`
	Name        string `json:"name"`
	Status      int    `json:"status"`
	ExpiredTime int64  `json:"expired_time"`
}

// UpdateInviteCode edits name, status (enable/disable) and expiry. With
// ?status_only set, only the status changes and the rest of the payload is
// ignored, mirroring the redemption console. Used codes are immutable so the
// registration history stays intact.
func UpdateInviteCode(c *gin.Context) {
	statusOnly := c.Query("status_only")
	var request updateInviteCodeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	inviteCode, err := model.GetInviteCodeById(request.Id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if inviteCode.Status == common.InviteCodeStatusUsed {
		common.ApiErrorT(c, "Cannot modify an invite code that has already been used")
		return
	}
	if statusOnly != "" {
		if request.Status != common.InviteCodeStatusEnabled && request.Status != common.InviteCodeStatusDisabled {
			common.ApiErrorT(c, "Invalid invite code status")
			return
		}
		inviteCode.Status = request.Status
	} else {
		// A full edit keeps the stored status; the console toggles it through
		// the status_only path.
		if utf8.RuneCountInString(request.Name) == 0 || utf8.RuneCountInString(request.Name) > 20 {
			common.ApiErrorT(c, "Invite code name length must be between 1-20")
			return
		}
		if err := validateExpiredTime(request.ExpiredTime); err != nil {
			common.ApiError(c, err)
			return
		}
		inviteCode.Name = request.Name
		inviteCode.ExpiredTime = request.ExpiredTime
	}
	if err := inviteCode.Update(); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "invite_code.update", map[string]any{"id": request.Id, "status_only": statusOnly != ""})
	common.ApiSuccess(c, inviteCode)
}

// DeleteInvalidInviteCode removes disabled and expired codes.
func DeleteInvalidInviteCode(c *gin.Context) {
	rows, err := model.DeleteInvalidInviteCodes()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "invite_code.delete_invalid", map[string]any{"count": rows})
	common.ApiSuccess(c, rows)
}

func DeleteInviteCodeBatch(c *gin.Context) {
	var request struct {
		Ids []int `json:"ids" binding:"required,min=1,max=1000,dive,gt=0"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	count, err := model.BatchDeleteInviteCodes(request.Ids)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "invite_code.delete_batch", map[string]any{
		"count":                     count,
		"total":                     len(request.Ids),
		"requested_invite_code_ids": request.Ids,
	})
	common.ApiSuccess(c, count)
}
