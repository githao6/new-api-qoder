package controller

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixture population is operator (group default), managed-user (default)
// and the four batch-target accounts (default). Quota tests seed their own
// groups so a selection size never depends on the inherited rows.

func quotaBatchBody(t *testing.T, groups []string, all bool, mode string, value int64) string {
	t.Helper()
	payload, err := common.Marshal(BatchQuotaRequest{Groups: groups, All: all, Mode: mode, Value: value})
	require.NoError(t, err)
	return string(payload)
}

func quotaBatchOperation(groups []string, all bool, mode string, value int64) service.VerificationOperation {
	payload, err := common.Marshal(service.AdminUserBatchQuotaContext{Groups: groups, All: all, Mode: mode, Value: value})
	if err != nil {
		panic(err)
	}
	return service.VerificationOperation{Scope: service.VerificationScopeAdminUserBatchQuota, Context: payload}
}

func storedQuota(t *testing.T, id int) int {
	t.Helper()
	user, err := model.GetUserById(id, false)
	require.NoError(t, err)
	return user.Quota
}

// seedQuotaGroup creates count common users in one group with a known balance
// and returns them in id order.
func seedQuotaGroup(t *testing.T, group string, count int, quota int) []*model.User {
	t.Helper()
	password, err := common.Password2Hash("quota-password-123")
	require.NoError(t, err)
	users := make([]*model.User, 0, count)
	for i := 0; i < count; i++ {
		user := &model.User{
			Username:    fmt.Sprintf("quota-%s-%d", group, i),
			Password:    password,
			Role:        common.RoleCommonUser,
			Status:      common.UserStatusEnabled,
			Group:       group,
			Quota:       quota,
			AuthVersion: 1,
			AffCode:     fmt.Sprintf("quota-aff-%s-%d", group, i),
		}
		require.NoError(t, model.DB.Create(user).Error)
		require.NoError(t, model.PublishUserAuthCache(user.Id))
		users = append(users, user)
	}
	return users
}

func decodeQuotaResult(t *testing.T, result securityEnrollmentResponse) BatchQuotaResult {
	t.Helper()
	var payload BatchQuotaResult
	require.NoError(t, common.Unmarshal(result.Data, &payload))
	return payload
}

func TestBatchQuotaRequiresProofBeforeMutation(t *testing.T) {
	_, identity, _ := setupBatchUserTest(t)
	quota := seedQuotaGroup(t, "quota-a", 2, 500)

	response := adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, []string{"quota-a"}, false, "add", 1000), "", identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result := decodeSecurityEnrollmentResponse(t, response)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Equal(t, "SECURITY_PROOF_REQUIRED", result.Code)
	for _, user := range quota {
		assert.Equal(t, 500, storedQuota(t, user.Id))
	}
}

// Every validation failure happens before the proof is consumed, so an
// operator who corrects a rejected request can reuse the verification they
// already completed.
func TestBatchQuotaValidatesBeforeConsumingProof(t *testing.T) {
	_, identity, fixture := setupBatchUserTest(t)
	quota := seedQuotaGroup(t, "quota-a", 3, 500)
	proof := issueSecurityEnrollmentProof(t, identity, quotaBatchOperation([]string{"quota-a"}, false, "add", 1000), service.VerificationMethodPassword)

	blocked := []struct{ name, body string }{
		{"empty selection", quotaBatchBody(t, nil, false, "add", 1000)},
		{"unknown group", quotaBatchBody(t, []string{"ghost-group"}, false, "add", 1000)},
		{"invalid mode", quotaBatchBody(t, []string{"quota-a"}, false, "set", 1000)},
		{"zero delta", quotaBatchBody(t, []string{"quota-a"}, false, "add", 0)},
		{"negative delta", quotaBatchBody(t, []string{"quota-a"}, false, "subtract", -5)},
		{"all with groups", quotaBatchBody(t, []string{"quota-a"}, true, "add", 1000)},
		{"malformed body", `{"groups":"quota-a","mode":"add","value":1}`},
	}
	for _, test := range blocked {
		t.Run(test.name, func(t *testing.T) {
			response := adminUserRequest(http.MethodPost, "/api/user/quota/batch", test.body, proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
			result := decodeSecurityEnrollmentResponse(t, response)
			assert.False(t, result.Success, test.name)
			for _, user := range quota {
				assert.Equal(t, 500, storedQuota(t, user.Id), test.name)
			}
		})
	}
	for _, user := range fixture {
		assert.Equal(t, 0, storedQuota(t, user.Id))
	}

	// The proof survived every rejection, so the valid request still succeeds.
	response := adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, []string{"quota-a"}, false, "add", 1000), proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result := decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	payload := decodeQuotaResult(t, result)
	assert.Equal(t, 3, payload.Affected)
	assert.Equal(t, 3, payload.Processed)
	assert.Zero(t, payload.Skipped)
	assert.Equal(t, int64(3), payload.SelectionSize)
	for _, user := range quota {
		assert.Equal(t, 1500, storedQuota(t, user.Id))
	}

	response = adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, []string{"quota-a"}, false, "add", 1000), proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result = decodeSecurityEnrollmentResponse(t, response)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Equal(t, "SECURITY_PROOF_CONSUMED", result.Code)
}

func TestBatchQuotaSubtractAndOverride(t *testing.T) {
	_, identity, _ := setupBatchUserTest(t)
	quota := seedQuotaGroup(t, "quota-a", 3, 3000)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", quota[0].Id).Update("quota", 100).Error)

	// A subtract that would drive one account below zero matches the
	// single-account contract: the balance goes negative rather than clamping,
	// and the run keeps going.
	proof := issueSecurityEnrollmentProof(t, identity, quotaBatchOperation([]string{"quota-a"}, false, "subtract", 1000), service.VerificationMethodPassword)
	response := adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, []string{"quota-a"}, false, "subtract", 1000), proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result := decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	payload := decodeQuotaResult(t, result)
	assert.Equal(t, 3, payload.Affected)
	assert.Equal(t, -900, storedQuota(t, quota[0].Id))
	assert.Equal(t, 2000, storedQuota(t, quota[1].Id))

	proof = issueSecurityEnrollmentProof(t, identity, quotaBatchOperation([]string{"quota-a"}, false, "override", 777), service.VerificationMethodPassword)
	response = adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, []string{"quota-a"}, false, "override", 777), proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result = decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	payload = decodeQuotaResult(t, result)
	assert.Equal(t, 3, payload.Affected)
	for _, user := range quota {
		assert.Equal(t, 777, storedQuota(t, user.Id))
	}

	// An override that changes nothing reports unchanged, not affected.
	proof = issueSecurityEnrollmentProof(t, identity, quotaBatchOperation([]string{"quota-a"}, false, "override", 777), service.VerificationMethodPassword)
	response = adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, []string{"quota-a"}, false, "override", 777), proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result = decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	payload = decodeQuotaResult(t, result)
	assert.Zero(t, payload.Affected)
	assert.Equal(t, 3, payload.Unchanged)
}

// A group selection must not bleed into other groups, including accounts whose
// group column is empty and soft-deleted rows.
func TestBatchQuotaConfinesToSelectedGroups(t *testing.T) {
	_, identity, _ := setupBatchUserTest(t)
	target := seedQuotaGroup(t, "quota-a", 2, 100)
	other := seedQuotaGroup(t, "quota-b", 2, 200)
	password := target[0].Password
	blank := &model.User{Username: "quota-blank", Password: password, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "quota-blank-group", Quota: 300, AuthVersion: 1, AffCode: "quota-blank-aff"}
	require.NoError(t, model.DB.Create(blank).Error)
	// The column default fills an empty Group on insert, so the blank group is
	// written explicitly to prove a group selection never matches it.
	require.NoError(t, model.DB.Model(blank).Update("group", "").Error)
	require.NoError(t, model.PublishUserAuthCache(blank.Id))
	deleted := &model.User{Username: "quota-deleted", Password: password, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "quota-a", Quota: 400, AuthVersion: 1, AffCode: "quota-deleted-aff"}
	require.NoError(t, model.DB.Create(deleted).Error)
	require.NoError(t, model.DB.Exec("UPDATE users SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?", deleted.Id).Error)

	proof := issueSecurityEnrollmentProof(t, identity, quotaBatchOperation([]string{"quota-a"}, false, "add", 50), service.VerificationMethodPassword)
	response := adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, []string{"quota-a"}, false, "add", 50), proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result := decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	payload := decodeQuotaResult(t, result)
	assert.Equal(t, 2, payload.Affected)
	assert.Equal(t, int64(2), payload.SelectionSize, "soft-deleted accounts stay out of the selection")
	for _, user := range target {
		assert.Equal(t, 150, storedQuota(t, user.Id))
	}
	for _, user := range other {
		assert.Equal(t, 200, storedQuota(t, user.Id))
	}
	assert.Equal(t, 300, storedQuota(t, blank.Id))

	// Only an all-selection reaches the remaining groups.
	proof = issueSecurityEnrollmentProof(t, identity, quotaBatchOperation(nil, true, "override", 1), service.VerificationMethodPassword)
	response = adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, nil, true, "override", 1), proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result = decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	for _, user := range other {
		assert.Equal(t, 1, storedQuota(t, user.Id))
	}
	assert.Equal(t, 1, storedQuota(t, blank.Id))
}

// A batch never moves the operator's own balance, and a non-root operator only
// reaches strictly lower roles. Both are reported as skips instead of failing.
func TestBatchQuotaSkipsSelfAndHigherRole(t *testing.T) {
	operator, identity, _ := setupBatchUserTest(t)
	target := seedQuotaGroup(t, "quota-a", 2, 100)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", operator.Id).Update("quota", 999999).Error)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target[0].Id).Update("role", common.RoleAdminUser).Error)
	operatorQuota := storedQuota(t, operator.Id)

	// A root operator may raise a peer administrator but never themselves.
	proof := issueSecurityEnrollmentProof(t, identity, quotaBatchOperation([]string{"quota-a"}, false, "add", 5), service.VerificationMethodPassword)
	response := adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, []string{"quota-a"}, false, "add", 5), proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result := decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	payload := decodeQuotaResult(t, result)
	assert.Equal(t, 2, payload.Affected)
	assert.Zero(t, payload.Skipped)
	assert.Equal(t, operatorQuota, storedQuota(t, operator.Id))

	// The same run as a plain administrator skips the administrator account.
	proof = issueSecurityEnrollmentProof(t, identity, quotaBatchOperation([]string{"quota-a"}, false, "add", 5), service.VerificationMethodPassword)
	response = adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, []string{"quota-a"}, false, "add", 5), proof, identity, common.RoleAdminUser, nil, BatchAdjustUserQuota)
	result = decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	payload = decodeQuotaResult(t, result)
	assert.Equal(t, 1, payload.Affected)
	assert.Equal(t, 1, payload.Skipped)
	assert.Equal(t, 1, payload.SkippedBy.Role)

	// An all-selection includes the operator's row, which is always skipped.
	require.NoError(t, model.DB.Model(&model.User{}).Where("1 = 1").Update("role", common.RoleCommonUser).Error)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", operator.Id).Update("role", common.RoleRootUser).Error)
	proof = issueSecurityEnrollmentProof(t, identity, quotaBatchOperation(nil, true, "add", 1), service.VerificationMethodPassword)
	response = adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, nil, true, "add", 1), proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result = decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	payload = decodeQuotaResult(t, result)
	assert.Equal(t, 1, payload.SkippedBy.Self)
	assert.Equal(t, operatorQuota, storedQuota(t, operator.Id))
}

func TestBatchQuotaProofIsBoundToSelectionModeAndAmount(t *testing.T) {
	_, identity, _ := setupBatchUserTest(t)
	quota := seedQuotaGroup(t, "quota-a", 2, 100)
	seedQuotaGroup(t, "quota-b", 2, 100)
	proof := issueSecurityEnrollmentProof(t, identity, quotaBatchOperation([]string{"quota-a"}, false, "add", 1000), service.VerificationMethodPassword)

	for _, test := range []struct{ name, body string }{
		{"other group", quotaBatchBody(t, []string{"quota-b"}, false, "add", 1000)},
		{"wider selection", quotaBatchBody(t, []string{"quota-a", "quota-b"}, false, "add", 1000)},
		{"all users", quotaBatchBody(t, nil, true, "add", 1000)},
		{"other amount", quotaBatchBody(t, []string{"quota-a"}, false, "add", 999)},
		{"other mode", quotaBatchBody(t, []string{"quota-a"}, false, "subtract", 1000)},
	} {
		response := adminUserRequest(http.MethodPost, "/api/user/quota/batch", test.body, proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
		result := decodeSecurityEnrollmentResponse(t, response)
		assert.Equal(t, http.StatusForbidden, response.Code, test.name)
		assert.Equal(t, "SECURITY_PROOF_CONTEXT_MISMATCH", result.Code, test.name)
		for _, user := range quota {
			assert.Equal(t, 100, storedQuota(t, user.Id), test.name)
		}
	}

	// A proof issued for another scope cannot drive the quota endpoint.
	deleteProof := issueSecurityEnrollmentProof(t, identity, batchOperation(service.VerificationScopeAdminUserBatchDelete, []int{quota[0].Id}, ""), service.VerificationMethodPassword)
	response := adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, []string{"quota-a"}, false, "add", 1000), deleteProof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result := decodeSecurityEnrollmentResponse(t, response)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Equal(t, "SECURITY_PROOF_SCOPE_MISMATCH", result.Code)

	// Reordered and duplicated group names bind to the same operation.
	unordered := issueSecurityEnrollmentProof(t, identity, quotaBatchOperation([]string{"quota-a", "quota-b", "quota-a"}, false, "add", 1000), service.VerificationMethodPassword)
	response = adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, []string{"quota-b", "quota-a"}, false, "add", 1000), unordered, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result = decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	payload := decodeQuotaResult(t, result)
	assert.Equal(t, 4, payload.Affected)
}

func TestBatchQuotaPaginatesAcrossPages(t *testing.T) {
	_, identity, _ := setupBatchUserTest(t)
	const total = model.UserQuotaBatchPageSize + 7
	quota := seedQuotaGroup(t, "quota-many", total, 10)
	require.Len(t, quota, total)

	proof := issueSecurityEnrollmentProof(t, identity, quotaBatchOperation([]string{"quota-many"}, false, "override", 99), service.VerificationMethodPassword)
	response := adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, []string{"quota-many"}, false, "override", 99), proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result := decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	payload := decodeQuotaResult(t, result)
	assert.Equal(t, total, payload.Processed)
	assert.Equal(t, total, payload.Affected)
	assert.Equal(t, int64(total), payload.SelectionSize)
	for _, user := range []*model.User{quota[0], quota[model.UserQuotaBatchPageSize], quota[total-1]} {
		assert.Equal(t, 99, storedQuota(t, user.Id))
	}
}

func TestBatchQuotaRejectsOversizedSelectionAndEmptyTargets(t *testing.T) {
	_, identity, _ := setupBatchUserTest(t)
	quota := seedQuotaGroup(t, "quota-a", 1, 100)

	// A selection over the group cap is rejected before the proof is consumed,
	// so the proof stays available for a request the operator can make.
	groups := make([]string, 0, service.MaxAdminUserBatchQuotaGroups()+1)
	for i := 0; i <= service.MaxAdminUserBatchQuotaGroups(); i++ {
		groups = append(groups, fmt.Sprintf("quota-group-%d", i))
	}
	proof := issueSecurityEnrollmentProof(t, identity, quotaBatchOperation([]string{"quota-a"}, false, "add", 10), service.VerificationMethodPassword)
	response := adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, groups, false, "add", 10), proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result := decodeSecurityEnrollmentResponse(t, response)
	assert.False(t, result.Success)
	assert.Equal(t, 100, storedQuota(t, quota[0].Id))

	// A configured group with no members is rejected instead of burning a
	// proof on a no-op. "vip" ships in the usable-group settings but no
	// fixture account uses it.
	vipUsers, err := model.CountUsersForQuotaBatch([]string{"vip"})
	require.NoError(t, err)
	require.Zero(t, vipUsers)
	proof = issueSecurityEnrollmentProof(t, identity, quotaBatchOperation([]string{"vip"}, false, "add", 10), service.VerificationMethodPassword)
	response = adminUserRequest(http.MethodPost, "/api/user/quota/batch", quotaBatchBody(t, []string{"vip"}, false, "add", 10), proof, identity, common.RoleRootUser, nil, BatchAdjustUserQuota)
	result = decodeSecurityEnrollmentResponse(t, response)
	assert.False(t, result.Success)
}

// A scoped access token may drive the quota batch, but only with user:write,
// and the audit records no secret material.
func TestBatchQuotaThroughScopedAccessToken(t *testing.T) {
	operator, _, _ := setupBatchUserTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.ExternalIdentityClaim{}, &model.Token{}))
	quota := seedQuotaGroup(t, "quota-a", 2, 250)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", operator.Id).Update("group", "quota-operator").Error)

	readToken, _ := createScopedAccessToken(t, operator.Id, 0, "user:read")
	writeToken, _ := createScopedAccessToken(t, operator.Id, 0, "user:write")
	router := newAccessTokenTestRouter()
	body := quotaBatchBody(t, []string{"quota-a"}, false, "add", 250)
	verify := `{"scope":"admin.user.quota.batch","method":"password","password":"enrollment-password","context":{"groups":["quota-a"],"all":false,"mode":"add","value":250}}`

	response := accessTokenRequest(router, http.MethodPost, "/api/verify", readToken, "", verify)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Equal(t, "SECURITY_ACTION_FORBIDDEN", decodeSecurityEnrollmentResponse(t, response).Code)
	for _, user := range quota {
		assert.Equal(t, 250, storedQuota(t, user.Id))
	}

	response = accessTokenRequest(router, http.MethodPost, "/api/verify", writeToken, "", verify)
	bodyResult := decodeSecurityEnrollmentResponse(t, response)
	require.True(t, bodyResult.Success, bodyResult.Message)
	var proof service.SecurityProof
	require.NoError(t, common.Unmarshal(bodyResult.Data, &proof))

	response = accessTokenRequest(router, http.MethodPost, "/api/user/quota/batch", writeToken, proof.ProofToken, body)
	result := decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	payload := decodeQuotaResult(t, result)
	assert.Equal(t, 2, payload.Affected)
	for _, user := range quota {
		assert.Equal(t, 500, storedQuota(t, user.Id))
	}

	var audit model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "user.quota_batch").Last(&audit).Error)
	assert.Equal(t, "access_token", audit.AuthMethod)
	require.NotNil(t, audit.Other.Op)
	auditJSON, err := common.Marshal(audit)
	require.NoError(t, err)
	assert.NotContains(t, string(auditJSON), proof.ProofToken)
	assert.NotContains(t, string(auditJSON), "enrollment-password")
}
