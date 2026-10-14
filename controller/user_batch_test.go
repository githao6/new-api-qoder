package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupBatchUserTest adds the tables the hard-delete cascade touches to the
// security-enrollment fixture and seeds a pool of manageable users.
func setupBatchUserTest(t *testing.T) (*model.User, service.AuthIdentity, []*model.User) {
	t.Helper()
	operator, identity, _ := setupAdminUserTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.ExternalIdentityClaim{}, &model.TwoFABackupCode{}))
	password, err := common.Password2Hash("batch-password-123")
	require.NoError(t, err)
	users := make([]*model.User, 0, 4)
	for i := 0; i < 4; i++ {
		user := &model.User{
			Username: fmt.Sprintf("batch-target-%d", i), Password: password,
			Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
			Group: "default", AuthVersion: 1, AffCode: fmt.Sprintf("batch-aff-%d", i),
		}
		require.NoError(t, model.DB.Create(user).Error)
		require.NoError(t, model.PublishUserAuthCache(user.Id))
		users = append(users, user)
	}
	return operator, identity, users
}

func jsonIDs(t *testing.T, ids []int) string {
	t.Helper()
	payload, err := json.Marshal(ids)
	require.NoError(t, err)
	return string(payload)
}

func batchUserIDs(users []*model.User) []int {
	ids := make([]int, 0, len(users))
	for _, user := range users {
		ids = append(ids, user.Id)
	}
	return ids
}

func batchOperation(scope string, ids []int, action string) service.VerificationOperation {
	var context any
	if action == "" {
		context = service.AdminUserBatchDeleteContext{UserIDs: ids}
	} else {
		context = service.AdminUserBatchManageContext{UserIDs: ids, Action: action}
	}
	payload, err := common.Marshal(context)
	if err != nil {
		panic(err)
	}
	return service.VerificationOperation{Scope: scope, Context: payload}
}

func TestBatchManageRequiresProofBeforeMutation(t *testing.T) {
	_, identity, users := setupBatchUserTest(t)
	ids := batchUserIDs(users)
	response := adminUserRequest(http.MethodPost, "/api/user/manage/batch", fmt.Sprintf(`{"ids":%s,"action":"disable"}`, jsonIDs(t, ids)), "", identity, common.RoleRootUser, nil, BatchManageUsers)
	result := decodeSecurityEnrollmentResponse(t, response)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Equal(t, "SECURITY_PROOF_REQUIRED", result.Code)
	for _, user := range users {
		stored, err := model.GetUserById(user.Id, false)
		require.NoError(t, err)
		assert.Equal(t, common.UserStatusEnabled, stored.Status)
	}
}

func TestBatchManageValidatesBeforeConsumingProof(t *testing.T) {
	operator, identity, users := setupBatchUserTest(t)
	ids := batchUserIDs(users)
	blocked := []struct {
		name, body string
	}{
		{"empty selection", `{"ids":[],"action":"disable"}`},
		{"invalid action", fmt.Sprintf(`{"ids":%s,"action":"promote"}`, jsonIDs(t, ids))},
		{"missing target", fmt.Sprintf(`{"ids":[%d,999999],"action":"disable"}`, ids[0])},
		{"operator self", fmt.Sprintf(`{"ids":[%d],"action":"disable"}`, operator.Id)},
	}
	for _, test := range blocked {
		t.Run(test.name, func(t *testing.T) {
			proof := issueSecurityEnrollmentProof(t, identity, batchOperation(service.VerificationScopeAdminUserBatchManage, ids, "disable"), service.VerificationMethodPassword)
			response := adminUserRequest(http.MethodPost, "/api/user/manage/batch", test.body, proof, identity, common.RoleRootUser, nil, BatchManageUsers)
			result := decodeSecurityEnrollmentResponse(t, response)
			assert.False(t, result.Success, response.Body.String())
			// Validation rejects before RequireSecurityProof runs, so the one-time
			// proof stays unconsumed and reusable for the valid request below.
			stored, err := model.GetUserById(ids[0], false)
			require.NoError(t, err)
			assert.Equal(t, common.UserStatusEnabled, stored.Status)
		})
	}
	// The proof survived every rejected request; a valid batch still consumes it.
	proof := issueSecurityEnrollmentProof(t, identity, batchOperation(service.VerificationScopeAdminUserBatchManage, ids, "disable"), service.VerificationMethodPassword)
	response := adminUserRequest(http.MethodPost, "/api/user/manage/batch", fmt.Sprintf(`{"ids":%s,"action":"disable"}`, jsonIDs(t, ids)), proof, identity, common.RoleRootUser, nil, BatchManageUsers)
	result := decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())

	response = adminUserRequest(http.MethodPost, "/api/user/manage/batch", fmt.Sprintf(`{"ids":%s,"action":"disable"}`, jsonIDs(t, ids)), proof, identity, common.RoleRootUser, nil, BatchManageUsers)
	result = decodeSecurityEnrollmentResponse(t, response)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Equal(t, "SECURITY_PROOF_CONSUMED", result.Code)
}

func TestBatchManageDisableAndEnable(t *testing.T) {
	_, identity, users := setupBatchUserTest(t)
	ids := batchUserIDs(users)

	proof := issueSecurityEnrollmentProof(t, identity, batchOperation(service.VerificationScopeAdminUserBatchManage, ids, "disable"), service.VerificationMethodPassword)
	response := adminUserRequest(http.MethodPost, "/api/user/manage/batch", fmt.Sprintf(`{"ids":%s,"action":"disable"}`, jsonIDs(t, ids)), proof, identity, common.RoleRootUser, nil, BatchManageUsers)
	result := decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	var payload BatchUserResult
	require.NoError(t, common.Unmarshal(result.Data, &payload))
	assert.Len(t, payload.AffectedUserIds, len(ids))
	assert.Equal(t, len(ids), payload.Total)
	for _, id := range ids {
		stored, err := model.GetUserById(id, false)
		require.NoError(t, err)
		assert.Equal(t, common.UserStatusDisabled, stored.Status)
	}
	// Disabling bumps the auth version, so old sessions stop validating.
	for _, id := range ids {
		var sessions int64
		require.NoError(t, model.DB.Model(&model.UserSession{}).Where("user_id = ? AND status = ?", id, model.UserSessionStatusActive).Count(&sessions).Error)
		assert.Zero(t, sessions)
	}

	// Re-enable; a second disabled user is skipped from affected ids.
	proof = issueSecurityEnrollmentProof(t, identity, batchOperation(service.VerificationScopeAdminUserBatchManage, ids, "enable"), service.VerificationMethodPassword)
	response = adminUserRequest(http.MethodPost, "/api/user/manage/batch", fmt.Sprintf(`{"ids":%s,"action":"enable"}`, jsonIDs(t, ids)), proof, identity, common.RoleRootUser, nil, BatchManageUsers)
	result = decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	require.NoError(t, common.Unmarshal(result.Data, &payload))
	assert.Len(t, payload.AffectedUserIds, len(ids))
	stored, err := model.GetUserById(ids[0], false)
	require.NoError(t, err)
	assert.Equal(t, common.UserStatusEnabled, stored.Status)

	var audit model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "user.manage_batch").Last(&audit).Error)
	require.NotNil(t, audit.Other.Op)
	actionValue, err := common.Marshal(audit.Other.Op.Params["action"])
	require.NoError(t, err)
	assert.JSONEq(t, `"enable"`, string(actionValue))
}

func TestBatchProofIsBoundToExactSetAndAction(t *testing.T) {
	_, identity, users := setupBatchUserTest(t)
	ids := batchUserIDs(users[:2])
	other := batchUserIDs(users[1:3])
	proof := issueSecurityEnrollmentProof(t, identity, batchOperation(service.VerificationScopeAdminUserBatchManage, ids, "disable"), service.VerificationMethodPassword)

	for _, test := range []struct {
		name, body string
	}{
		{"different set", fmt.Sprintf(`{"ids":%s,"action":"disable"}`, jsonIDs(t, other))},
		{"different action", fmt.Sprintf(`{"ids":%s,"action":"enable"}`, jsonIDs(t, ids))},
		{"superset", fmt.Sprintf(`{"ids":%s,"action":"disable"}`, jsonIDs(t, batchUserIDs(users[:3])))},
		{"delete scope", fmt.Sprintf(`{"ids":%s}`, jsonIDs(t, ids))},
	} {
		handler := gin.HandlerFunc(BatchManageUsers)
		if test.name == "delete scope" {
			handler = gin.HandlerFunc(BatchDeleteUsers)
		}
		response := adminUserRequest(http.MethodPost, "/api/user/manage/batch", test.body, proof, identity, common.RoleRootUser, nil, handler)
		result := decodeSecurityEnrollmentResponse(t, response)
		assert.Equal(t, http.StatusForbidden, response.Code, test.name)
		// A wrong scope is a scope mismatch; a wrong set or action is a context mismatch.
		if test.name == "delete scope" {
			assert.Equal(t, "SECURITY_PROOF_SCOPE_MISMATCH", result.Code, test.name)
		} else {
			assert.Equal(t, "SECURITY_PROOF_CONTEXT_MISMATCH", result.Code, test.name)
		}
	}
	// Order and duplicates do not change the binding.
	unorderedProof := issueSecurityEnrollmentProof(t, identity, batchOperation(service.VerificationScopeAdminUserBatchManage, []int{ids[1], ids[0], ids[1]}, "disable"), service.VerificationMethodPassword)
	response := adminUserRequest(http.MethodPost, "/api/user/manage/batch", fmt.Sprintf(`{"ids":%s,"action":"disable"}`, jsonIDs(t, ids)), unorderedProof, identity, common.RoleRootUser, nil, BatchManageUsers)
	result := decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
}

func TestBatchManageRejectsRootAndHigherRoleTargets(t *testing.T) {
	_, identity, users := setupBatchUserTest(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", users[0].Id).Update("role", common.RoleAdminUser).Error)

	// A batch with a root member fails whole-batch, including already-checked peers.
	rootUser := &model.User{Username: "batch-root", Password: users[0].Password, Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "batch-root-aff"}
	require.NoError(t, model.DB.Create(rootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(rootUser.Id))
	mixed := append(batchUserIDs(users[1:]), rootUser.Id)
	proof := issueSecurityEnrollmentProof(t, identity, batchOperation(service.VerificationScopeAdminUserBatchManage, mixed, "disable"), service.VerificationMethodPassword)
	// A root operator passes the role rule for every target, so the dedicated
	// super-administrator guard is what rejects the batch.
	response := adminUserRequest(http.MethodPost, "/api/user/manage/batch", fmt.Sprintf(`{"ids":%s,"action":"disable"}`, jsonIDs(t, mixed)), proof, identity, common.RoleRootUser, nil, BatchManageUsers)
	result := decodeSecurityEnrollmentResponse(t, response)
	assert.False(t, result.Success)
	assert.Contains(t, result.Message, "Cannot disable super administrator user")
	for _, id := range batchUserIDs(users) {
		stored, err := model.GetUserById(id, false)
		require.NoError(t, err)
		assert.Equal(t, common.UserStatusEnabled, stored.Status)
	}

	// An administrator cannot disable a peer administrator.
	adminIDs := []int{users[0].Id}
	proof = issueSecurityEnrollmentProof(t, identity, batchOperation(service.VerificationScopeAdminUserBatchManage, adminIDs, "disable"), service.VerificationMethodPassword)
	response = adminUserRequest(http.MethodPost, "/api/user/manage/batch", fmt.Sprintf(`{"ids":%s,"action":"disable"}`, jsonIDs(t, adminIDs)), proof, identity, common.RoleAdminUser, nil, BatchManageUsers)
	result = decodeSecurityEnrollmentResponse(t, response)
	assert.False(t, result.Success)
	stored, err := model.GetUserById(users[0].Id, false)
	require.NoError(t, err)
	assert.Equal(t, common.UserStatusEnabled, stored.Status)
}

func TestBatchDeleteRequiresProofAndHardDeletes(t *testing.T) {
	_, identity, users := setupBatchUserTest(t)
	ids := batchUserIDs(users[:2])
	kept := users[2].Id

	require.NoError(t, model.DB.Create(&model.Token{UserId: ids[0], Name: "batch-token", Key: "batch-token-key", Status: common.TokenStatusEnabled}).Error)
	proof := issueSecurityEnrollmentProof(t, identity, batchOperation(service.VerificationScopeAdminUserBatchDelete, ids, ""), service.VerificationMethodPassword)
	response := adminUserRequest(http.MethodPost, "/api/user/delete/batch", fmt.Sprintf(`{"ids":%s}`, jsonIDs(t, ids)), "", identity, common.RoleRootUser, nil, BatchDeleteUsers)
	result := decodeSecurityEnrollmentResponse(t, response)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Equal(t, "SECURITY_PROOF_REQUIRED", result.Code)

	response = adminUserRequest(http.MethodPost, "/api/user/delete/batch", fmt.Sprintf(`{"ids":%s}`, jsonIDs(t, ids)), proof, identity, common.RoleRootUser, nil, BatchDeleteUsers)
	result = decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	var payload BatchUserResult
	require.NoError(t, common.Unmarshal(result.Data, &payload))
	assert.ElementsMatch(t, ids, payload.AffectedUserIds)

	for _, id := range ids {
		_, err := model.GetUserById(id, true)
		assert.Error(t, err, "hard-deleted user must be gone")
	}
	_, err := model.GetUserById(kept, false)
	assert.NoError(t, err)
	var tokens int64
	require.NoError(t, model.DB.Model(&model.Token{}).Where("user_id = ?", ids[0]).Count(&tokens).Error)
	assert.Zero(t, tokens)

	var audit model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "user.delete_batch").Last(&audit).Error)
	require.NotNil(t, audit.Other.Op)
	auditJSON, err := common.Marshal(audit)
	require.NoError(t, err)
	assert.NotContains(t, string(auditJSON), proof)
}

func TestBatchDeleteValidatesWholeBatch(t *testing.T) {
	_, identity, users := setupBatchUserTest(t)
	ids := batchUserIDs(users[:2])
	for _, test := range []struct {
		name string
		body string
	}{
		{"empty", `{"ids":[]}`},
		{"missing", fmt.Sprintf(`{"ids":[%d,999999]}`, ids[0])},
	} {
		proof := issueSecurityEnrollmentProof(t, identity, batchOperation(service.VerificationScopeAdminUserBatchDelete, ids, ""), service.VerificationMethodPassword)
		response := adminUserRequest(http.MethodPost, "/api/user/delete/batch", test.body, proof, identity, common.RoleRootUser, nil, BatchDeleteUsers)
		result := decodeSecurityEnrollmentResponse(t, response)
		assert.False(t, result.Success, test.name)
		_, err := model.GetUserById(ids[0], false)
		assert.NoError(t, err, test.name)
	}

	// Root and admin-level targets reject the whole batch.
	root := &model.User{Username: "batch-root-2", Password: users[0].Password, Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "batch-root-2-aff"}
	require.NoError(t, model.DB.Create(root).Error)
	require.NoError(t, model.PublishUserAuthCache(root.Id))
	mixed := append(ids, root.Id)
	proof := issueSecurityEnrollmentProof(t, identity, batchOperation(service.VerificationScopeAdminUserBatchDelete, mixed, ""), service.VerificationMethodPassword)
	// A root operator clears the myRole <= role rule, leaving the super-admin guard.
	response := adminUserRequest(http.MethodPost, "/api/user/delete/batch", fmt.Sprintf(`{"ids":%s}`, jsonIDs(t, mixed)), proof, identity, common.RoleRootUser, nil, BatchDeleteUsers)
	result := decodeSecurityEnrollmentResponse(t, response)
	assert.False(t, result.Success)
	// A root operator is rejected by the myRole <= target rule first, exactly
	// like the single-account endpoint; the super-admin guard covers admins.
	assert.Contains(t, result.Message, "No permission to update users of same or higher permission level")
	for _, id := range ids {
		_, err := model.GetUserById(id, false)
		assert.NoError(t, err)
	}

	// An administrator cannot delete a peer administrator (myRole <= target).
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", ids[1]).Update("role", common.RoleAdminUser).Error)
	proof = issueSecurityEnrollmentProof(t, identity, batchOperation(service.VerificationScopeAdminUserBatchDelete, ids, ""), service.VerificationMethodPassword)
	response = adminUserRequest(http.MethodPost, "/api/user/delete/batch", fmt.Sprintf(`{"ids":%s}`, jsonIDs(t, ids)), proof, identity, common.RoleAdminUser, nil, BatchDeleteUsers)
	result = decodeSecurityEnrollmentResponse(t, response)
	assert.False(t, result.Success)
	_, err := model.GetUserById(ids[0], false)
	assert.NoError(t, err)
}

func TestBatchRejectsOversizedSelection(t *testing.T) {
	_, identity, _ := setupBatchUserTest(t)
	tooMany := make([]int, service.MaxAdminUserBatchSize()+1)
	for i := range tooMany {
		tooMany[i] = 10_000 + i
	}
	for _, test := range []struct {
		path, body, expect string
	}{
		{"/api/user/manage/batch", fmt.Sprintf(`{"ids":%s,"action":"disable"}`, jsonIDs(t, tooMany)), "Maximum 100 users can be managed in one batch"},
		{"/api/user/delete/batch", fmt.Sprintf(`{"ids":%s}`, jsonIDs(t, tooMany)), "Maximum 100 users can be deleted in one batch"},
	} {
		handler := gin.HandlerFunc(BatchManageUsers)
		if test.path == "/api/user/delete/batch" {
			handler = BatchDeleteUsers
		}
		response := adminUserRequest(http.MethodPost, test.path, test.body, "", identity, common.RoleRootUser, nil, handler)
		result := decodeSecurityEnrollmentResponse(t, response)
		assert.False(t, result.Success)
		assert.Contains(t, result.Message, test.expect)
	}
}
