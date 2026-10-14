package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newInviteCode(t *testing.T, key string, status int, expiredTime int64) *InviteCode {
	t.Helper()
	inviteCode := &InviteCode{
		UserId:      1,
		Key:         key,
		Status:      status,
		Name:        "test",
		CreatedTime: common.GetTimestamp(),
		ExpiredTime: expiredTime,
	}
	require.NoError(t, inviteCode.Insert())
	return inviteCode
}

func loadInviteCode(t *testing.T, key string) *InviteCode {
	t.Helper()
	stored := &InviteCode{}
	require.NoError(t, DB.Where("key = ?", key).First(stored).Error)
	return stored
}

func TestConsumeInviteCodeIsSingleUse(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&InviteCode{}))
	defer DB.Exec("DELETE FROM invite_codes")

	key := "invite-single-use"
	newInviteCode(t, key, common.InviteCodeStatusEnabled, 0)

	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return ConsumeInviteCode(tx, key, 7, "consumer")
	}))

	stored := loadInviteCode(t, key)
	assert.Equal(t, common.InviteCodeStatusUsed, stored.Status)
	assert.Equal(t, 7, stored.UsedUserId)
	assert.Equal(t, "consumer", stored.UsedUsername)
	assert.NotZero(t, stored.UsedTime)

	// One code registers exactly one account: the second attempt must fail and
	// must not overwrite the first consumer's audit fields.
	err := DB.Transaction(func(tx *gorm.DB) error {
		return ConsumeInviteCode(tx, key, 8, "second")
	})
	assert.ErrorIs(t, err, ErrInviteCodeInvalid)

	stored = loadInviteCode(t, key)
	assert.Equal(t, 7, stored.UsedUserId)
	assert.Equal(t, "consumer", stored.UsedUsername)
}

func TestConsumeInviteCodeRejectsMissingDisabledAndExpired(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&InviteCode{}))
	defer DB.Exec("DELETE FROM invite_codes")

	assert.ErrorIs(t, ConsumeInviteCode(DB, "", 1, "nobody"), ErrInviteCodeRequired)
	assert.ErrorIs(t, ConsumeInviteCode(DB, "does-not-exist", 1, "nobody"), ErrInviteCodeInvalid)

	disabledKey := "invite-disabled"
	newInviteCode(t, disabledKey, common.InviteCodeStatusDisabled, 0)
	assert.ErrorIs(t, ConsumeInviteCode(DB, disabledKey, 1, "nobody"), ErrInviteCodeInvalid)

	expiredKey := "invite-expired"
	newInviteCode(t, expiredKey, common.InviteCodeStatusEnabled, common.GetTimestamp()-1)
	assert.ErrorIs(t, ConsumeInviteCode(DB, expiredKey, 1, "nobody"), ErrInviteCodeExpired)

	// Rejections must leave the rows untouched so admins can still recover them.
	assert.Equal(t, common.InviteCodeStatusDisabled, loadInviteCode(t, disabledKey).Status)
	assert.Equal(t, common.InviteCodeStatusEnabled, loadInviteCode(t, expiredKey).Status)
}

func TestConsumeInviteCodeRollsBackWithTransaction(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&InviteCode{}))
	defer DB.Exec("DELETE FROM invite_codes")

	key := "invite-rollback"
	newInviteCode(t, key, common.InviteCodeStatusEnabled, 0)

	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := ConsumeInviteCode(tx, key, 9, "loser"); err != nil {
			return err
		}
		return assert.AnError
	})
	require.Error(t, err)

	stored := loadInviteCode(t, key)
	assert.Equal(t, common.InviteCodeStatusEnabled, stored.Status)
	assert.Zero(t, stored.UsedUserId)
	assert.Zero(t, stored.UsedTime)
}

func TestDeleteInvalidInviteCodesKeepsUsedCodes(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&InviteCode{}))
	defer DB.Exec("DELETE FROM invite_codes")

	usedKey := "invite-keep"
	newInviteCode(t, usedKey, common.InviteCodeStatusUsed, 0)
	disabledKey := "invite-drop-disabled"
	newInviteCode(t, disabledKey, common.InviteCodeStatusDisabled, 0)
	expiredKey := "invite-drop-expired"
	newInviteCode(t, expiredKey, common.InviteCodeStatusEnabled, common.GetTimestamp()-1)
	availableKey := "invite-drop-available"
	newInviteCode(t, availableKey, common.InviteCodeStatusEnabled, 0)

	count, err := DeleteInvalidInviteCodes()
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)

	assert.Equal(t, common.InviteCodeStatusUsed, loadInviteCode(t, usedKey).Status)
	assert.Equal(t, common.InviteCodeStatusEnabled, loadInviteCode(t, availableKey).Status)
	assert.ErrorIs(t, DB.Where("key = ?", disabledKey).First(&InviteCode{}).Error, gorm.ErrRecordNotFound)
	assert.ErrorIs(t, DB.Where("key = ?", expiredKey).First(&InviteCode{}).Error, gorm.ErrRecordNotFound)
}

func TestInsertWithInviteCodeConsumesAtomically(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&InviteCode{}))
	defer DB.Exec("DELETE FROM invite_codes")

	key := "invite-sign-up"
	newInviteCode(t, key, common.InviteCodeStatusEnabled, 0)

	user := &User{Username: "invitee", Password: "secret-password-123", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
	require.NoError(t, user.InsertWithInviteCode(0, key))

	stored := loadInviteCode(t, key)
	assert.Equal(t, common.InviteCodeStatusUsed, stored.Status)
	assert.Equal(t, user.Id, stored.UsedUserId)
	assert.Equal(t, "invitee", stored.UsedUsername)

	// A second sign-up with the same code must fail and must not create a user.
	duplicate := &User{Username: "invitee2", Password: "secret-password-123", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
	assert.ErrorIs(t, duplicate.InsertWithInviteCode(0, key), ErrInviteCodeInvalid)
	var count int64
	require.NoError(t, DB.Model(&User{}).Where("username = ?", "invitee2").Count(&count).Error)
	assert.Zero(t, count)
}

func TestInsertWithInviteCodeReleasesCodeOnUserInsertFailure(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&InviteCode{}))
	defer DB.Exec("DELETE FROM invite_codes")

	key := "invite-rollback-sign-up"
	newInviteCode(t, key, common.InviteCodeStatusEnabled, 0)

	require.NoError(t, (&User{Username: "taken", Password: "secret-password-123", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}).Insert(0))

	// Same username: the user insert fails, so the transaction rolls back and the
	// invite code stays available for the next attempt.
	conflict := &User{Username: "taken", Password: "secret-password-123", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
	require.Error(t, conflict.InsertWithInviteCode(0, key))
	assert.Equal(t, common.InviteCodeStatusEnabled, loadInviteCode(t, key).Status)
}

func TestInsertWithInviteCodeKeepsPlainInsertWhenNoCode(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&InviteCode{}))
	defer DB.Exec("DELETE FROM invite_codes")

	user := &User{Username: "nocodeneeded", Password: "secret-password-123", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
	require.NoError(t, user.InsertWithInviteCode(0, ""))
	assert.NotZero(t, user.Id)

	var count int64
	require.NoError(t, DB.Model(&InviteCode{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestBatchDeleteInviteCodesSkipsUsedCodes(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&InviteCode{}))
	defer DB.Exec("DELETE FROM invite_codes")

	usedKey := "batch-keep-used"
	used := newInviteCode(t, usedKey, common.InviteCodeStatusUsed, 0)
	availableKey := "batch-drop-available"
	available := newInviteCode(t, availableKey, common.InviteCodeStatusEnabled, 0)

	count, err := BatchDeleteInviteCodes([]int{used.Id, available.Id, 999999})
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)

	assert.Equal(t, common.InviteCodeStatusUsed, loadInviteCode(t, usedKey).Status)
	assert.ErrorIs(t, DB.Where("key = ?", availableKey).First(&InviteCode{}).Error, gorm.ErrRecordNotFound)
}
