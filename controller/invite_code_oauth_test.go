package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const inviteOAuthSlug = "invite-oauth-test"

// inviteCodeOAuthProvider is a built-in-style provider: it writes the provider
// user ID onto the user row instead of creating a binding record.
type inviteCodeOAuthProvider struct {
	authFlowTestOAuthProvider
	providerUserID string
	taken          bool
	lookupUserID   int
}

func (provider *inviteCodeOAuthProvider) GetName() string { return "Invite OAuth Test" }
func (provider *inviteCodeOAuthProvider) GetProviderPrefix() string {
	return inviteOAuthSlug + "_"
}
func (provider *inviteCodeOAuthProvider) ProviderUserIDColumn() string { return "github_id" }
func (provider *inviteCodeOAuthProvider) IsUserIDTaken(string) bool    { return provider.taken }
func (provider *inviteCodeOAuthProvider) SetProviderUserID(user *model.User, id string) {
	user.GitHubId = id
}
func (provider *inviteCodeOAuthProvider) FillUserByProviderID(user *model.User, _ string) error {
	return model.DB.First(user, provider.lookupUserID).Error
}
func (provider *inviteCodeOAuthProvider) GetUserInfo(context.Context, *oauth.OAuthToken) (*oauth.OAuthUser, error) {
	provider.userInfoCalls++
	return &oauth.OAuthUser{ProviderUserID: provider.providerUserID}, nil
}

func setupInviteOAuthTest(t *testing.T, provider *inviteCodeOAuthProvider) {
	t.Helper()
	setupSecurityEnrollmentTest(t)
	_ = provider
	require.NoError(t, model.DB.AutoMigrate(&model.InviteCode{}))
	oauth.Register(inviteOAuthSlug, provider)
	t.Cleanup(func() { oauth.Unregister(inviteOAuthSlug) })
	previousRegister, previousGate := common.RegisterEnabled, common.InviteRegistrationEnabled
	t.Cleanup(func() {
		common.RegisterEnabled = previousRegister
		common.InviteRegistrationEnabled = previousGate
	})
	common.RegisterEnabled = true
}

func registerInviteOAuthProvider(t *testing.T, provider *inviteCodeOAuthProvider) {
	t.Helper()
	oauth.Register(inviteOAuthSlug, provider)
	t.Cleanup(func() { oauth.Unregister(inviteOAuthSlug) })
}

func seedOAuthInviteCode(t *testing.T, key string) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.InviteCode{
		UserId: 1, Key: key, Status: common.InviteCodeStatusEnabled,
		Name: "oauth", CreatedTime: common.GetTimestamp(),
	}).Error)
}

// completeOAuthCallback drives the provider callback for a login flow whose
// payload carries the given invite code.
func completeOAuthCallback(t *testing.T, invite string) *httptest.ResponseRecorder {
	t.Helper()
	payload := `{}`
	if invite != "" {
		payload = `{"invite_code":"` + invite + `"}`
	}
	token, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeOAuth, Provider: inviteOAuthSlug, Intent: model.AuthFlowIntentLogin,
		Payload: payload, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	router := gin.New()
	router.GET("/api/oauth/:provider", HandleOAuth)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/oauth/"+inviteOAuthSlug+"?state="+token+"&code=provider-code", nil))
	return response
}

func countProviderAccounts(t *testing.T) int64 {
	t.Helper()
	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).
		Where("username LIKE ?", inviteOAuthSlug+"%").Count(&count).Error)
	return count
}

func TestGenerateOAuthCodeCarriesInviteInLoginFlow(t *testing.T) {
	setupAuthFlowControllerTest(t)

	cases := []struct {
		name string
		body string
	}{
		{"invite too long", `{"provider":"auth-flow-test","intent":"login","invite":"` + strings.Repeat("a", 33) + `"}`},
		{"non-login intent", `{"provider":"auth-flow-test","intent":"bind","invite":"0123456789abcdef0123456789abcdef"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(testCase.body))
			c.Request.Header.Set("Content-Type", "application/json")
			GenerateOAuthCode(c)
			success, messageKey := decodeSuccess(t, recorder)
			assert.False(t, success)
			assert.Equal(t, "Invalid parameters", messageKey)
		})
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state",
		strings.NewReader(`{"provider":"auth-flow-test","intent":"login","invite":"0123456789abcdef0123456789abcdef"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	GenerateOAuthCode(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			FlowToken string `json:"flow_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success, recorder.Body.String())

	flow, err := model.GetAuthFlow(response.Data.FlowToken, model.AuthFlowMatch{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
	})
	require.NoError(t, err)
	var payload oauthFlowPayload
	require.NoError(t, common.UnmarshalJsonStr(flow.Payload, &payload))
	assert.Equal(t, "0123456789abcdef0123456789abcdef", payload.InviteCode)
}

func TestOAuthFirstLoginIsGatedByInviteCode(t *testing.T) {
	provider := &inviteCodeOAuthProvider{providerUserID: "invite-newcomer"}
	setupInviteOAuthTest(t, provider)
	key := "oauth-single-use"
	seedOAuthInviteCode(t, key)

	// Gate on, no code carried: first-time sign-in refused, code untouched.
	common.InviteRegistrationEnabled = true
	response := completeOAuthCallback(t, "")
	assert.Contains(t, response.Body.String(), "Invite code is required to register")
	assert.EqualValues(t, 0, countProviderAccounts(t))
	assert.Equal(t, common.InviteCodeStatusEnabled, storedInviteCode(t, key).Status)

	// Gate on with a valid code: account created, code spent and attributed.
	response = completeOAuthCallback(t, key)
	assert.NotContains(t, response.Body.String(), "Invite code is required to register")
	require.EqualValues(t, 1, countProviderAccounts(t))
	consumed := storedInviteCode(t, key)
	assert.Equal(t, common.InviteCodeStatusUsed, consumed.Status)
	assert.NotZero(t, consumed.UsedUserId)
	assert.True(t, strings.HasPrefix(consumed.UsedUsername, inviteOAuthSlug+"_"), consumed.UsedUsername)

	// The spent code cannot admit a second identity.
	provider.providerUserID = "invite-secondcomer"
	response = completeOAuthCallback(t, key)
	assert.Contains(t, response.Body.String(), "Invite code is invalid or already used")
	assert.EqualValues(t, 1, countProviderAccounts(t))

	// Gate off: sign-ins proceed without any code.
	common.InviteRegistrationEnabled = false
	provider.providerUserID = "invite-freecomer"
	response = completeOAuthCallback(t, "")
	assert.NotContains(t, response.Body.String(), "Invite code is required to register")
	assert.EqualValues(t, 2, countProviderAccounts(t), response.Body.String())
}

func TestOAuthExistingUserLoginIgnoresInviteGate(t *testing.T) {
	setupInviteOAuthTest(t, nil)
	hashed, err := common.Password2Hash("existing-password")
	require.NoError(t, err)
	existing := &model.User{
		Username: "oauth-regular", Password: hashed, Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
		AffCode: "oauth-regular-aff", GitHubId: "invite-regular",
	}
	require.NoError(t, model.DB.Create(existing).Error)
	registerInviteOAuthProvider(t, &inviteCodeOAuthProvider{
		providerUserID: "invite-regular", taken: true, lookupUserID: existing.Id,
	})

	common.InviteRegistrationEnabled = true
	key := "unused-for-existing"
	seedOAuthInviteCode(t, key)

	response := completeOAuthCallback(t, key)
	// The gate only applies to account creation, so a returning user signs in and
	// a code cached by their client is not burned.
	assert.NotContains(t, response.Body.String(), "Invite code is required to register")
	assert.NotContains(t, response.Body.String(), "Invite code is invalid or already used")
	assert.Equal(t, common.InviteCodeStatusEnabled, storedInviteCode(t, key).Status)
	assert.Contains(t, strings.Join(response.Header().Values("Set-Cookie"), "\n"), "new_api_")
}
