package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Batch quota support. The single-account path (AdjustUserQuota) already owns
// the balance math; the batch reuses exactly that math per account, in its own
// committed transaction, so a run over a whole group never holds one long
// transaction and never pays out a partial rollback silently.

// UserQuotaBatchPageSize is the id-cursor page size a batch quota run reads at
// a time, keeping memory bounded for a population-wide request.
const UserQuotaBatchPageSize = 200

// UserQuotaSkipReason explains why a batch candidate was left untouched.
type UserQuotaSkipReason string

const (
	UserQuotaSkipNone    UserQuotaSkipReason = ""
	UserQuotaSkipSelf    UserQuotaSkipReason = "self"
	UserQuotaSkipRole    UserQuotaSkipReason = "role"
	UserQuotaSkipDeleted UserQuotaSkipReason = "deleted"
	UserQuotaSkipMissing UserQuotaSkipReason = "missing"
	UserQuotaSkipLimit   UserQuotaSkipReason = "quota_limit"
)

// UserQuotaBatchCandidate is the projected row a batch run reads: the identity
// needed for authorization and grouping plus the balance snapshot.
type UserQuotaBatchCandidate struct {
	Id       int    `gorm:"column:id" json:"id"`
	Username string `gorm:"column:username" json:"username"`
	Group    string `gorm:"column:group" json:"group"`
	Role     int    `gorm:"column:role" json:"role"`
	Quota    int    `gorm:"column:quota" json:"quota"`
}

// UserQuotaBatchOutcome is the per-account result of one batch adjustment.
type UserQuotaBatchOutcome struct {
	UserID   int                 `json:"user_id"`
	Username string              `json:"username"`
	Group    string              `json:"group"`
	Before   int                 `json:"before"`
	After    int                 `json:"after"`
	Changed  bool                `json:"changed"`
	Skip     UserQuotaSkipReason `json:"skip_reason,omitempty"`
}

// ErrUserQuotaBatchAborted marks a run that stopped on a transaction error.
// Accounts committed before it keep their new balance; the caller reports the
// partial result instead of implying a rollback.
var ErrUserQuotaBatchAborted = errors.New("user quota batch aborted")

// ListUsersForQuotaBatchPage returns one id-ordered page of accounts covered by
// a group selection, where an empty selection means every group. The User
// soft-delete scope already excludes removed accounts, so a batch never pays
// out a deleted account. The group column is a reserved word, so it goes
// through clause.Column and the dialect's own quoting.
func ListUsersForQuotaBatchPage(groups []string, afterId int, limit int) ([]UserQuotaBatchCandidate, error) {
	if limit <= 0 {
		return nil, errors.New("limit must be positive")
	}
	if afterId < 0 {
		afterId = 0
	}
	query := DB.Model(&User{}).
		Select("id", "username", "group", "role", "quota").
		Where("id > ?", afterId)
	if len(groups) > 0 {
		query = query.Where(groupINClause(groups))
	}
	var page []UserQuotaBatchCandidate
	if err := query.Order("id asc").Limit(limit).Find(&page).Error; err != nil {
		return nil, err
	}
	return page, nil
}

// CountUsersForQuotaBatch returns how many active accounts a group selection
// currently covers; the audit records it as the size of the confirmed selection.
func CountUsersForQuotaBatch(groups []string) (int64, error) {
	query := DB.Model(&User{})
	if len(groups) > 0 {
		query = query.Where(groupINClause(groups))
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// QuotaGroupCount is how many active accounts one group currently covers.
type QuotaGroupCount struct {
	Group string `json:"group"`
	Count int64  `json:"count"`
}

// CountUsersByQuotaGroups summarizes the population per group so a batch run
// can show its blast radius before the operator confirms it. It reuses the same
// group filter as the run itself, so a listed count is exactly what selecting
// that group covers.
func CountUsersByQuotaGroups() ([]QuotaGroupCount, error) {
	names := DistinctUserQuotaGroups()
	counts := make([]QuotaGroupCount, 0, len(names))
	for _, group := range names {
		if group == "" {
			// An empty group is only reachable through an all-users run, since
			// a requested group name must be non-empty.
			continue
		}
		total, err := CountUsersForQuotaBatch([]string{group})
		if err != nil {
			return nil, err
		}
		counts = append(counts, QuotaGroupCount{Group: group, Count: total})
	}
	return counts, nil
}

// DistinctUserQuotaGroups lists the groups present in active accounts, so the
// console can offer groups that exist in data even when they are no longer in
// the configured usable-group list. gorm renders the quoted identifier itself,
// which the raw commonGroupCol constant cannot provide in isolated tests where
// the production bootstrap that initializes it never ran.
func DistinctUserQuotaGroups() []string {
	var groups []string
	err := DB.Model(&User{}).
		Distinct().
		Order(clause.OrderByColumn{Column: clause.Column{Name: "group"}}).
		Pluck("group", &groups).Error
	if err != nil {
		common.SysError(common.LogText("failed to list user quota groups: %s", err.Error()))
		return nil
	}
	return groups
}

// groupINClause builds the group filter with the dialect's identifier quoting,
// which the raw commonGroupCol constant cannot provide in isolated tests where
// the production bootstrap that initializes it never ran.
func groupINClause(groups []string) clause.IN {
	values := make([]any, 0, len(groups))
	for _, group := range groups {
		values = append(values, group)
	}
	return clause.IN{Column: clause.Column{Name: "group"}, Values: values}
}

// AdjustUserQuotaBatch applies one quota operation to every candidate, one
// committed transaction per account, and returns the outcomes in input order.
// A transaction error aborts the run and wraps it in ErrUserQuotaBatchAborted,
// with the outcomes produced so far still returned.
func AdjustUserQuotaBatch(operatorID, operatorRole int, mode string, value int, candidates []UserQuotaBatchCandidate) ([]UserQuotaBatchOutcome, error) {
	outcomes := make([]UserQuotaBatchOutcome, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Id <= 0 {
			continue
		}
		outcome, err := adjustOneUserQuota(operatorID, operatorRole, mode, value, candidate.Id)
		if err != nil {
			return outcomes, errors.Join(ErrUserQuotaBatchAborted, err)
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

func adjustOneUserQuota(operatorID, operatorRole int, mode string, value int, userID int) (UserQuotaBatchOutcome, error) {
	outcome := UserQuotaBatchOutcome{UserID: userID}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				outcome.Skip = UserQuotaSkipMissing
				return nil
			}
			return err
		}
		outcome.Username = user.Username
		outcome.Group = user.Group
		outcome.Before = user.Quota
		outcome.After = user.Quota
		if user.DeletedAt.Valid {
			outcome.Skip = UserQuotaSkipDeleted
			return nil
		}
		// A batch never touches the operator's own account, and a non-root
		// operator reaches only strictly lower roles, exactly like the
		// single-account adjustment.
		if user.Id == operatorID {
			outcome.Skip = UserQuotaSkipSelf
			return nil
		}
		if operatorRole != common.RoleRootUser && operatorRole <= user.Role {
			outcome.Skip = UserQuotaSkipRole
			return nil
		}
		if user.Quota > common.MaxWalletQuota || user.Quota < -common.MaxWalletQuota {
			outcome.Skip = UserQuotaSkipLimit
			return nil
		}
		quota := decimal.NewFromInt(int64(value))
		switch mode {
		case "add":
			quota = decimal.NewFromInt(int64(user.Quota)).Add(quota)
		case "subtract":
			quota = decimal.NewFromInt(int64(user.Quota)).Sub(quota)
		}
		after, err := common.WalletQuotaFromDecimalStrict(quota)
		if err != nil {
			outcome.Skip = UserQuotaSkipLimit
			return nil
		}
		outcome.After = after
		if after == user.Quota {
			return nil
		}
		result := tx.Model(&User{}).Where("id = ?", user.Id).Update("quota", after)
		if result.Error != nil {
			return result.Error
		}
		// MySQL counts only changed rows, so an unchanged override is verified
		// against the locked snapshot above rather than RowsAffected.
		if result.RowsAffected != 1 {
			outcome.After = user.Quota
			outcome.Skip = UserQuotaSkipMissing
			return nil
		}
		outcome.Changed = true
		return nil
	})
	if err != nil {
		return outcome, err
	}
	if outcome.Changed {
		// Mirror only the committed difference so outstanding relay
		// reservations survive, matching AdjustUserQuota.
		delta := int64(outcome.After) - int64(outcome.Before)
		if err := cacheIncrUserQuota(outcome.UserID, delta); err != nil {
			common.SysError(common.LogText("failed to sync batch quota adjustment for user %d: %s", outcome.UserID, err))
		}
	}
	return outcome, nil
}
