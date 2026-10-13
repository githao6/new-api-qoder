package model

import (
	"errors"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// InviteCode is an administrator-issued credential that gates registration when
// InviteRegistrationEnabled is on. Each code registers exactly one account;
// consumption is a compare-and-swap on status so concurrent registrations of
// the same code admit exactly one, mirroring Redemption.Redeem.
type InviteCode struct {
	Id           int            `json:"id"`
	UserId       int            `json:"user_id"`
	Key          string         `json:"key" gorm:"type:char(32);uniqueIndex"`
	Status       int            `json:"status" gorm:"default:1"`
	Name         string         `json:"name" gorm:"index"`
	CreatedTime  int64          `json:"created_time" gorm:"bigint"`
	UsedTime     int64          `json:"used_time" gorm:"bigint"`
	ExpiredTime  int64          `json:"expired_time" gorm:"bigint"` // 过期时间，0 表示不过期
	UsedUserId   int            `json:"used_user_id"`
	UsedUsername string         `json:"used_username" gorm:"type:varchar(64)"`
	Count        int            `json:"count" gorm:"-:all"` // only for api request
	DeletedAt    gorm.DeletedAt `gorm:"index"`
}

func GetAllInviteCodes(startIdx int, num int) (inviteCodes []*InviteCode, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	if err = tx.Model(&InviteCode{}).Count(&total).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Order("id desc").Limit(num).Offset(startIdx).Find(&inviteCodes).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	return inviteCodes, total, nil
}

func SearchInviteCodes(keyword string, status string, startIdx int, num int) (inviteCodes []*InviteCode, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := tx.Model(&InviteCode{})

	if keyword != "" {
		if id, convErr := strconv.Atoi(keyword); convErr == nil {
			query = query.Where("id = ? OR name LIKE ? OR key = ?", id, keyword+"%", keyword)
		} else {
			query = query.Where("name LIKE ? OR key = ?", keyword+"%", keyword)
		}
	}

	if status != "" {
		now := common.GetTimestamp()
		switch status {
		case "expired":
			query = query.Where(
				"status = ? AND expired_time != 0 AND expired_time < ?",
				common.InviteCodeStatusEnabled,
				now,
			)
		case strconv.Itoa(common.InviteCodeStatusEnabled):
			query = query.Where(
				"status = ? AND (expired_time = 0 OR expired_time >= ?)",
				common.InviteCodeStatusEnabled,
				now,
			)
		case strconv.Itoa(common.InviteCodeStatusDisabled):
			query = query.Where("status = ?", common.InviteCodeStatusDisabled)
		case strconv.Itoa(common.InviteCodeStatusUsed):
			query = query.Where("status = ?", common.InviteCodeStatusUsed)
		}
	}

	if err = query.Count(&total).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&inviteCodes).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	return inviteCodes, total, nil
}

func GetInviteCodeById(id int) (*InviteCode, error) {
	if id == 0 {
		return nil, common.NewMessage("ID is empty")
	}
	inviteCode := InviteCode{Id: id}
	err := DB.First(&inviteCode, "id = ?", id).Error
	return &inviteCode, err
}

// ConsumeInviteCode claims a code for a registration. It must run inside the
// transaction that inserts the new user, so a rolled-back registration also
// releases the code. A missing, disabled, expired or already-used code yields
// ErrInviteCodeInvalid (expired yields ErrInviteCodeExpired).
func ConsumeInviteCode(tx *gorm.DB, key string, userId int, username string) error {
	if strings.TrimSpace(key) == "" {
		return ErrInviteCodeRequired
	}
	inviteCode := &InviteCode{}
	keyCol := "`key`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		keyCol = `"key"`
	}
	if err := lockForUpdate(tx).Where(keyCol+" = ?", key).First(inviteCode).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrInviteCodeInvalid
		}
		return err
	}
	if inviteCode.Status == common.InviteCodeStatusUsed {
		return ErrInviteCodeInvalid
	}
	if inviteCode.Status != common.InviteCodeStatusEnabled {
		return ErrInviteCodeInvalid
	}
	if inviteCode.ExpiredTime != 0 && inviteCode.ExpiredTime < common.GetTimestamp() {
		return ErrInviteCodeExpired
	}
	// Compare-and-swap: only the transaction that flips enabled -> used may
	// claim the code, so a concurrent registration with the same code loses.
	result := tx.Model(&InviteCode{}).
		Where("id = ? AND status = ?", inviteCode.Id, common.InviteCodeStatusEnabled).
		Updates(map[string]any{
			"status":        common.InviteCodeStatusUsed,
			"used_time":     common.GetTimestamp(),
			"used_user_id":  userId,
			"used_username": username,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrInviteCodeInvalid
	}
	return nil
}

func (inviteCode *InviteCode) Insert() error {
	return DB.Create(inviteCode).Error
}

func (inviteCode *InviteCode) Update() error {
	err := DB.Model(inviteCode).Select("name", "status", "expired_time").Updates(inviteCode).Error
	return err
}

func (inviteCode *InviteCode) Delete() error {
	return DB.Delete(inviteCode).Error
}

// BatchDeleteInviteCodes soft-deletes the selected codes. Used codes are
// excluded so a bulk action can never erase the registration audit trail.
func BatchDeleteInviteCodes(ids []int) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result := DB.Where("id IN ? AND status <> ?", ids, common.InviteCodeStatusUsed).Delete(&InviteCode{})
	return result.RowsAffected, result.Error
}

// DeleteInvalidInviteCodes removes disabled and expired codes; used codes are
// kept so registration history stays auditable.
func DeleteInvalidInviteCodes() (int64, error) {
	now := common.GetTimestamp()
	result := DB.Where(
		"status = ? OR (status = ? AND expired_time != 0 AND expired_time < ?)",
		common.InviteCodeStatusDisabled,
		common.InviteCodeStatusEnabled,
		now,
	).Delete(&InviteCode{})
	return result.RowsAffected, result.Error
}
