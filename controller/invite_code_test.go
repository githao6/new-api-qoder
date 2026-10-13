package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupInviteRegistrationTest gives each case an isolated SQLite database plus
// the option flags the registration gate reads.
func setupInviteRegistrationTest(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	previousGate := common.InviteRegistrationEnabled
	previousRegister := common.RegisterEnabled
	previousPasswordRegister := common.PasswordRegisterEnabled
	previousEmailVerification := common.EmailVerificationEnabled

	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/invite.db?_pragma=busy_timeout(10000)"), &gorm.Config{})
	require.NoError(t, err)
	db.Logger = logger.Default.LogMode(logger.Silent)
	connection, err := db.DB()
	require.NoError(t, err)
	connection.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.InviteCode{}, &model.Option{}, &model.AuditLog{}))

	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.RegisterEnabled = true
	common.PasswordRegisterEnabled = true
	common.EmailVerificationEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled = previousRedis
		common.InviteRegistrationEnabled = previousGate
		common.RegisterEnabled = previousRegister
		common.PasswordRegisterEnabled = previousPasswordRegister
		common.EmailVerificationEnabled = previousEmailVerification
		_ = connection.Close()
	})
}

func seedInviteCode(t *testing.T, key string, status int, expiredTime int64) *model.InviteCode {
	t.Helper()
	inviteCode := model.InviteCode{
		UserId:      1,
		Key:         key,
		Status:      status,
		Name:        "seed",
		CreatedTime: common.GetTimestamp(),
		ExpiredTime: expiredTime,
	}
	require.NoError(t, model.DB.Create(&inviteCode).Error)
	return &inviteCode
}

func registerPayload(t *testing.T, username, invite string) string {
	t.Helper()
	fields := []string{
		`"username":"` + username + `"`,
		`"password":"invite-password"`,
	}
	if invite != "" {
		fields = append(fields, `"invite":"`+invite+`"`)
	}
	return "{" + strings.Join(fields, ",") + "}"
}

func postRegister(t *testing.T, username, invite string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/register", strings.NewReader(registerPayload(t, username, invite)))
	c.Request.Header.Set("Content-Type", "application/json")
	Register(c)
	return response
}

func decodeSuccess(t *testing.T, response *httptest.ResponseRecorder) (bool, string) {
	t.Helper()
	var result struct {
		Success    bool   `json:"success"`
		MessageKey string `json:"message_key"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	return result.Success, result.MessageKey
}

func storedInviteCode(t *testing.T, key string) *model.InviteCode {
	t.Helper()
	inviteCode := &model.InviteCode{}
	require.NoError(t, model.DB.Where("key = ?", key).First(inviteCode).Error)
	return inviteCode
}

func TestRegisterRequiresInviteCodeWhenGateEnabled(t *testing.T) {
	setupInviteRegistrationTest(t)
	common.InviteRegistrationEnabled = true
	key := "gate-missing-code"
	seedInviteCode(t, key, common.InviteCodeStatusEnabled, 0)

	success, messageKey := decodeSuccess(t, postRegister(t, "nocodereg", ""))
	assert.False(t, success)
	assert.Equal(t, "Invite code is required to register", messageKey)

	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&count).Error)
	assert.Zero(t, count)
	assert.Equal(t, common.InviteCodeStatusEnabled, storedInviteCode(t, key).Status)
}

func TestRegisterConsumesInviteCodeOnce(t *testing.T) {
	setupInviteRegistrationTest(t)
	common.InviteRegistrationEnabled = true
	key := "gate-single-use"
	seedInviteCode(t, key, common.InviteCodeStatusEnabled, 0)

	success, _ := decodeSuccess(t, postRegister(t, "firstinvitee", key))
	require.True(t, success)

	consumed := storedInviteCode(t, key)
	assert.Equal(t, common.InviteCodeStatusUsed, consumed.Status)
	assert.Equal(t, "firstinvitee", consumed.UsedUsername)
	require.NotZero(t, consumed.UsedUserId)

	// One code, one account: a second registration with the same code is rejected
	// and creates no user.
	success, messageKey := decodeSuccess(t, postRegister(t, "secondinvitee", key))
	assert.False(t, success)
	assert.Equal(t, "Invite code is invalid or already used", messageKey)

	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", "secondinvitee").Count(&count).Error)
	assert.Zero(t, count)
}

func TestRegisterRejectsExpiredAndDisabledInviteCode(t *testing.T) {
	setupInviteRegistrationTest(t)
	common.InviteRegistrationEnabled = true

	expiredKey := "gate-expired"
	seedInviteCode(t, expiredKey, common.InviteCodeStatusEnabled, common.GetTimestamp()-1)
	success, messageKey := decodeSuccess(t, postRegister(t, "expiredreg", expiredKey))
	assert.False(t, success)
	assert.Equal(t, "Invite code has expired", messageKey)
	assert.Equal(t, common.InviteCodeStatusEnabled, storedInviteCode(t, expiredKey).Status, "expired codes stay recoverable")

	disabledKey := "gate-disabled"
	seedInviteCode(t, disabledKey, common.InviteCodeStatusDisabled, 0)
	success, messageKey = decodeSuccess(t, postRegister(t, "disabledreg", disabledKey))
	assert.False(t, success)
	assert.Equal(t, "Invite code is invalid or already used", messageKey)
}

func TestRegisterIgnoresInviteCodeWhenGateDisabled(t *testing.T) {
	setupInviteRegistrationTest(t)
	common.InviteRegistrationEnabled = false
	key := "gate-off-code"
	seedInviteCode(t, key, common.InviteCodeStatusEnabled, 0)

	// A stale code from a client that cached it must not burn the code while the
	// gate is off, and a registration without any code must succeed.
	success, _ := decodeSuccess(t, postRegister(t, "freejoin", key))
	require.True(t, success)
	stored := storedInviteCode(t, key)
	assert.Equal(t, common.InviteCodeStatusEnabled, stored.Status)
	assert.Zero(t, stored.UsedUserId)

	success, _ = decodeSuccess(t, postRegister(t, "freejoin2", ""))
	assert.True(t, success)
}

// newInviteAdminContext builds a request through a router so FullPath, audit
// context and query parameters behave like real dashboard calls.
func newInviteAdminContext(t *testing.T, method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	router := gin.New()
	var captured *gin.Context
	router.Handle(method, "/api/invite/:action", func(c *gin.Context) {
		c.Set("id", 1)
		c.Set("role", common.RoleAdminUser)
		captured = c
		c.Abort()
	})
	response := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, req)
	require.NotNil(t, captured)
	captured.Set("id", 1)
	captured.Set("role", common.RoleAdminUser)
	return captured, response
}

func TestAddInviteCodesValidation(t *testing.T) {
	setupInviteRegistrationTest(t)

	cases := []struct {
		name       string
		body       string
		messageKey string
	}{
		{"empty name", `{"name":"","count":1,"expired_time":0}`, "Invite code name length must be between 1-20"},
		{"long name", `{"name":"aaaaaaaaaaaaaaaaaaaaaaa","count":1,"expired_time":0}`, "Invite code name length must be between 1-20"},
		{"zero count", `{"name":"batch","count":0,"expired_time":0}`, "Invite code count must be greater than 0"},
		{"over limit", `{"name":"batch","count":101,"expired_time":0}`, "Maximum 100 invite codes can be generated at once"},
		{"past expiry", fmt.Sprintf(`{"name":"batch","count":1,"expired_time":%d}`, common.GetTimestamp()-1), "Expiration time cannot be earlier than current time"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			c, rec := newInviteAdminContext(t, http.MethodPost, "/api/invite/create", testCase.body)
			AddInviteCodes(c)
			success, messageKey := decodeSuccess(t, rec)
			assert.False(t, success)
			assert.Equal(t, testCase.messageKey, messageKey)
			var count int64
			require.NoError(t, model.DB.Model(&model.InviteCode{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}

	c, rec := newInviteAdminContext(t, http.MethodPost, "/api/invite/create", `{"name":"batch","count":3,"expired_time":0}`)
	AddInviteCodes(c)
	var created struct {
		Success bool     `json:"success"`
		Data    []string `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &created))
	require.True(t, created.Success)
	require.Len(t, created.Data, 3)

	var count int64
	require.NoError(t, model.DB.Model(&model.InviteCode{}).Count(&count).Error)
	assert.EqualValues(t, 3, count)
}

func TestUpdateInviteCodeKeepsStatusOnFullEdit(t *testing.T) {
	setupInviteRegistrationTest(t)
	disabledKey := "edit-keeps-status"
	seedInviteCode(t, disabledKey, common.InviteCodeStatusDisabled, 0)
	disabled := storedInviteCode(t, disabledKey)

	future := common.GetTimestamp() + 3600
	c, rec := newInviteAdminContext(t, http.MethodPut, "/api/invite/update",
		fmt.Sprintf(`{"id":%d,"name":"renamed","expired_time":%d,"count":1}`, disabled.Id, future))
	UpdateInviteCode(c)
	var updated struct {
		Success bool             `json:"success"`
		Data    model.InviteCode `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &updated))
	require.True(t, updated.Success, rec.Body.String())
	assert.Equal(t, "renamed", updated.Data.Name)
	// The console sends no status on a full edit, so the stored status survives.
	assert.Equal(t, common.InviteCodeStatusDisabled, updated.Data.Status)
	assert.Equal(t, future, updated.Data.ExpiredTime)
}

func TestUpdateInviteCodeStatusOnly(t *testing.T) {
	setupInviteRegistrationTest(t)
	key := "status-only"
	seedInviteCode(t, key, common.InviteCodeStatusEnabled, 0)
	target := storedInviteCode(t, key)

	c, rec := newInviteAdminContext(t, http.MethodPut, "/api/invite/update?status_only=true",
		fmt.Sprintf(`{"id":%d,"status":2}`, target.Id))
	UpdateInviteCode(c)
	success, _ := decodeSuccess(t, rec)
	require.True(t, success)
	assert.Equal(t, common.InviteCodeStatusDisabled, storedInviteCode(t, key).Status)

	c, rec = newInviteAdminContext(t, http.MethodPut, "/api/invite/update?status_only=true",
		fmt.Sprintf(`{"id":%d,"status":7}`, target.Id))
	UpdateInviteCode(c)
	success, messageKey := decodeSuccess(t, rec)
	assert.False(t, success)
	assert.Equal(t, "Invalid invite code status", messageKey)
}

func TestUsedInviteCodeIsImmutable(t *testing.T) {
	setupInviteRegistrationTest(t)
	key := "used-immutable"
	seedInviteCode(t, key, common.InviteCodeStatusUsed, 0)
	used := storedInviteCode(t, key)

	c, rec := newInviteAdminContext(t, http.MethodPut, "/api/invite/update?status_only=true",
		fmt.Sprintf(`{"id":%d,"status":1}`, used.Id))
	UpdateInviteCode(c)
	success, messageKey := decodeSuccess(t, rec)
	assert.False(t, success)
	assert.Equal(t, "Cannot modify an invite code that has already been used", messageKey)

	c, rec = newInviteAdminContext(t, http.MethodDelete, fmt.Sprintf("/api/invite/%d", used.Id), "")
	c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(used.Id)}}
	DeleteInviteCode(c)
	success, messageKey = decodeSuccess(t, rec)
	assert.False(t, success)
	assert.Equal(t, "Cannot delete an invite code that has already been used", messageKey)
	assert.NotZero(t, storedInviteCode(t, key).Id, "used code stays for audit history")
}

func TestDeleteInvalidInviteCodeRemovesDisabledAndExpiredOnly(t *testing.T) {
	setupInviteRegistrationTest(t)
	usedKey := "keep-used"
	seedInviteCode(t, usedKey, common.InviteCodeStatusUsed, 0)
	disabledKey := "drop-disabled"
	seedInviteCode(t, disabledKey, common.InviteCodeStatusDisabled, 0)
	expiredKey := "drop-expired"
	seedInviteCode(t, expiredKey, common.InviteCodeStatusEnabled, common.GetTimestamp()-1)
	availableKey := "keep-available"
	seedInviteCode(t, availableKey, common.InviteCodeStatusEnabled, 0)

	c, rec := newInviteAdminContext(t, http.MethodDelete, "/api/invite/invalid", "")
	DeleteInvalidInviteCode(c)
	var result struct {
		Success bool  `json:"success"`
		Data    int64 `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &result))
	require.True(t, result.Success)
	assert.EqualValues(t, 2, result.Data)

	assert.NotZero(t, storedInviteCode(t, usedKey).Id)
	assert.NotZero(t, storedInviteCode(t, availableKey).Id)
	var missing model.InviteCode
	assert.ErrorIs(t, model.DB.Where("key = ?", disabledKey).First(&missing).Error, gorm.ErrRecordNotFound)
	assert.ErrorIs(t, model.DB.Where("key = ?", expiredKey).First(&missing).Error, gorm.ErrRecordNotFound)
}

func TestDeleteInviteCodeBatchSparesUsedCodes(t *testing.T) {
	setupInviteRegistrationTest(t)
	usedKey := "batch-used"
	seedInviteCode(t, usedKey, common.InviteCodeStatusUsed, 0)
	used := storedInviteCode(t, usedKey)
	availableKey := "batch-available"
	seedInviteCode(t, availableKey, common.InviteCodeStatusEnabled, 0)
	available := storedInviteCode(t, availableKey)

	c, rec := newInviteAdminContext(t, http.MethodPost, "/api/invite/batch",
		fmt.Sprintf(`{"ids":[%d,%d]}`, used.Id, available.Id))
	DeleteInviteCodeBatch(c)
	var result struct {
		Success bool  `json:"success"`
		Data    int64 `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &result))
	require.True(t, result.Success)
	assert.EqualValues(t, 1, result.Data, "only the unused code is deleted")

	assert.NotZero(t, storedInviteCode(t, usedKey).Id)
	var missing model.InviteCode
	assert.ErrorIs(t, model.DB.Where("key = ?", availableKey).First(&missing).Error, gorm.ErrRecordNotFound)
}

func TestWeChatFirstSignInIsGatedByInviteCode(t *testing.T) {
	setupInviteRegistrationTest(t)
	previousEnabled, previousAddress := common.WeChatAuthEnabled, common.WeChatServerAddress
	t.Cleanup(func() {
		common.WeChatAuthEnabled = previousEnabled
		common.WeChatServerAddress = previousAddress
	})
	common.WeChatAuthEnabled = true

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// One openid per authorization code, so each attempt is a distinct
		// WeChat identity.
		_, _ = w.Write([]byte(`{"success":true,"data":"wechat-` + r.URL.Query().Get("code") + `"}`))
	}))
	t.Cleanup(upstream.Close)
	common.WeChatServerAddress = upstream.URL

	key := "wechat-single-use"
	seedInviteCode(t, key, common.InviteCodeStatusEnabled, 0)

	attempt := 0
	runWeChatCallback := func(invite string) *httptest.ResponseRecorder {
		attempt++
		target := fmt.Sprintf("/api/oauth/wechat?code=code-%d", attempt)
		if invite != "" {
			target += "&invite=" + invite
		}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, target, nil)
		WeChatAuth(c)
		return recorder
	}

	common.InviteRegistrationEnabled = true
	response := runWeChatCallback("")
	assert.Contains(t, response.Body.String(), "Invite code is required to register")
	assert.Equal(t, common.InviteCodeStatusEnabled, storedInviteCode(t, key).Status)

	response = runWeChatCallback(key)
	assert.NotContains(t, response.Body.String(), "Invite code is required to register")
	consumed := storedInviteCode(t, key)
	assert.Equal(t, common.InviteCodeStatusUsed, consumed.Status)
	assert.True(t, strings.HasPrefix(consumed.UsedUsername, "wechat_"), consumed.UsedUsername)

	// Used codes cannot admit another new WeChat account.
	response = runWeChatCallback(key)
	assert.Contains(t, response.Body.String(), "Invite code is invalid or already used")
}
