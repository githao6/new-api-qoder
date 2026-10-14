package controller

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

// Batch quota adjustments keep the step-up contract of the batch status
// actions, but the proof binds a group selection plus the operation and amount
// instead of an ID list: the target set is whatever those groups contain at
// execution time and can be far larger than a proof context could carry.
// Selection and parameters are validated before the proof is consumed, and
// every balance change is its own committed transaction, so the run reports
// per-account outcomes instead of rolling back the whole population.

type BatchQuotaRequest struct {
	Groups []string `json:"groups"`
	All    bool     `json:"all"`
	Mode   string   `json:"mode"`
	Value  int64    `json:"value"`
}

type BatchQuotaSkips struct {
	Self    int `json:"self"`
	Role    int `json:"role"`
	Deleted int `json:"deleted"`
	Missing int `json:"missing"`
	Limit   int `json:"quota_limit"`
}

type BatchQuotaResult struct {
	Groups        []string        `json:"groups"`
	All           bool            `json:"all"`
	Mode          string          `json:"mode"`
	Value         int64           `json:"value"`
	Processed     int             `json:"processed"`
	Affected      int             `json:"affected"`
	Unchanged     int             `json:"unchanged"`
	Skipped       int             `json:"skipped"`
	SkippedBy     BatchQuotaSkips `json:"skipped_by_reason"`
	SelectionSize int64           `json:"selection_size"`
	Partial       bool            `json:"partial"`
}

// quotaGroupChoices lists the groups an operator may target: the configured
// usable groups, the ratio groups, and every group present in live accounts,
// so a renamed or legacy group is still addressable.
func quotaGroupChoices() map[string]bool {
	choices := make(map[string]bool, 16)
	for group := range setting.GetUserUsableGroupsCopy() {
		choices[group] = true
	}
	for group := range ratio_setting.GetGroupRatioCopy() {
		choices[group] = true
	}
	for _, group := range model.DistinctUserQuotaGroups() {
		if group != "" {
			choices[group] = true
		}
	}
	return choices
}

func countBatchQuotaSkip(skips *BatchQuotaSkips, reason model.UserQuotaSkipReason) {
	switch reason {
	case model.UserQuotaSkipSelf:
		skips.Self++
	case model.UserQuotaSkipRole:
		skips.Role++
	case model.UserQuotaSkipDeleted:
		skips.Deleted++
	case model.UserQuotaSkipMissing:
		skips.Missing++
	case model.UserQuotaSkipLimit:
		skips.Limit++
	}
}

// validateBatchQuotaRequest normalizes the request into its proof context and
// rejects anything the proof should never be issued for. Failures happen
// before proof consumption, so a rejected attempt leaves the proof reusable.
func validateBatchQuotaRequest(c *gin.Context) (*service.AdminUserBatchQuotaContext, bool) {
	var req BatchQuotaRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return nil, false
	}
	context := service.AdminUserBatchQuotaContext{
		Groups: req.Groups,
		All:    req.All,
		Mode:   req.Mode,
		Value:  req.Value,
	}
	switch {
	case !service.IsAdminUserQuotaMode(context.Mode):
		common.ApiErrorT(c, "Invalid parameters")
		return nil, false
	case context.Mode != "override" && context.Value <= 0:
		common.ApiErrorT(c, "Quota change amount cannot be zero")
		return nil, false
	case context.Value > int64(common.MaxWalletQuota) || context.Value < -int64(common.MaxWalletQuota):
		common.ApiErrorT(c, "Quota value exceeds valid range, maximum is {{max}}", map[string]any{"max": common.MaxWalletQuota})
		return nil, false
	case context.All && len(context.Groups) > 0:
		common.ApiErrorT(c, "Select either all users or specific groups")
		return nil, false
	case !context.All && len(context.Groups) == 0:
		common.ApiErrorT(c, "No users selected")
		return nil, false
	case !context.All && len(context.Groups) > service.MaxAdminUserBatchQuotaGroups():
		common.ApiErrorT(c, "Too many items in batch request, maximum is {{max}}", map[string]any{"max": service.MaxAdminUserBatchQuotaGroups()})
		return nil, false
	}
	if !service.NormalizeAdminUserBatchQuotaContext(&context) {
		common.ApiErrorT(c, "Invalid parameters")
		return nil, false
	}
	// Unknown group names are rejected before the proof is consumed: a typo
	// must not silently narrow a money-changing request.
	if !context.All {
		known := quotaGroupChoices()
		for _, group := range context.Groups {
			if !known[group] {
				common.ApiErrorT(c, "User group {{group}} does not exist", map[string]any{"group": group})
				return nil, false
			}
		}
	}
	return &context, true
}

// GetBatchQuotaGroups lists every addressable group with the number of live
// accounts it currently covers, so the console can show what a selection is
// worth before asking for a proof. A configured group with no accounts is still
// offered, because an empty run is refused rather than silently succeeding.
// `total` counts every account an all-users run would reach.
func GetBatchQuotaGroups(c *gin.Context) {
	known := quotaGroupChoices()
	counts, err := model.CountUsersByQuotaGroups()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	total, err := model.CountUsersForQuotaBatch(nil)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	groups := make([]model.QuotaGroupCount, 0, len(known))
	seen := make(map[string]bool, len(known))
	for _, item := range counts {
		groups = append(groups, item)
		seen[item.Group] = true
	}
	for group := range known {
		if !seen[group] {
			groups = append(groups, model.QuotaGroupCount{Group: group, Count: 0})
		}
	}
	slices.SortFunc(groups, func(a, b model.QuotaGroupCount) int {
		return strings.Compare(a.Group, b.Group)
	})
	common.ApiSuccess(c, gin.H{"groups": groups, "total": total})
}

// BatchAdjustUserQuota raises, lowers or overrides the balance of every user in// the selected groups on a single `admin.user.quota.batch` proof.
func BatchAdjustUserQuota(c *gin.Context) {
	context, ok := validateBatchQuotaRequest(c)
	if !ok {
		return
	}
	// A normalized all-selection carries no groups, which the model layer
	// reads as "every group", including accounts whose group column is empty.
	groups := context.Groups
	if context.All {
		groups = nil
	}
	selectionSize, err := model.CountUsersForQuotaBatch(groups)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if selectionSize == 0 {
		common.ApiErrorT(c, "No users selected")
		return
	}

	authorization := requireAdminUserProof(c, service.VerificationScopeAdminUserBatchQuota, *context)
	if authorization == nil {
		return
	}

	result := BatchQuotaResult{
		Groups:        context.Groups,
		All:           context.All,
		Mode:          context.Mode,
		Value:         context.Value,
		SelectionSize: selectionSize,
	}
	// The balance math is identical for every account, so the model layer runs
	// the page as one batch and syncs each quota cache delta after commit.
	afterID := 0
	for {
		page, err := model.ListUsersForQuotaBatchPage(groups, afterID, model.UserQuotaBatchPageSize)
		if err != nil {
			recordBatchQuotaAudit(c, result, *authorization, true)
			common.SysError(common.LogText("failed to list batch quota targets: %s", err.Error()))
			result.Partial = true
			break
		}
		if len(page) == 0 {
			break
		}
		afterID = page[len(page)-1].Id
		outcomes, err := model.AdjustUserQuotaBatch(c.GetInt("id"), c.GetInt("role"), context.Mode, int(context.Value), page)
		for _, outcome := range outcomes {
			result.Processed++
			switch {
			case outcome.Skip != model.UserQuotaSkipNone:
				result.Skipped++
				countBatchQuotaSkip(&result.SkippedBy, outcome.Skip)
			case outcome.Changed:
				result.Affected++
			default:
				result.Unchanged++
			}
		}
		if err != nil {
			recordBatchQuotaAudit(c, result, *authorization, true)
			common.SysError(common.LogText("batch quota adjustment aborted after %d accounts: %s", result.Processed, err.Error()))
			result.Partial = true
			break
		}
		if len(page) < model.UserQuotaBatchPageSize {
			break
		}
	}

	if !result.Partial {
		recordBatchQuotaAudit(c, result, *authorization, false)
	}
	if result.Partial {
		common.ApiErrorT(c, "Failed to adjust user quota")
		return
	}
	common.ApiSuccess(c, result)
}

func recordBatchQuotaAudit(c *gin.Context, result BatchQuotaResult, authorization model.AuthFlowAuthorization, failed bool) {
	params := map[string]any{
		"mode":                result.Mode,
		"quota":               result.Value,
		"all":                 result.All,
		"groups":              result.Groups,
		"selection_size":      result.SelectionSize,
		"processed":           result.Processed,
		"affected":            result.Affected,
		"skipped":             result.Skipped,
		"verification_method": authorization.Method,
	}
	if failed {
		params["partial"] = true
	}
	recordManageAudit(c, "user.quota_batch", params)
}
