package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAdminUserBatchIDs(t *testing.T) {
	t.Run("sorts and de-duplicates", func(t *testing.T) {
		ids, ok := NormalizeAdminUserBatchIDs([]int{5, 1, 5, 3})
		require.True(t, ok)
		assert.Equal(t, []int{1, 3, 5}, ids)
	})
	t.Run("rejects an empty list", func(t *testing.T) {
		_, ok := NormalizeAdminUserBatchIDs(nil)
		assert.False(t, ok)
		_, ok = NormalizeAdminUserBatchIDs([]int{})
		assert.False(t, ok)
	})
	t.Run("rejects non-positive IDs", func(t *testing.T) {
		_, ok := NormalizeAdminUserBatchIDs([]int{1, 0})
		assert.False(t, ok)
		_, ok = NormalizeAdminUserBatchIDs([]int{-2})
		assert.False(t, ok)
	})
	t.Run("rejects an oversized batch", func(t *testing.T) {
		ids := make([]int, MaxAdminUserBatchSize()+1)
		for i := range ids {
			ids[i] = i + 1
		}
		_, ok := NormalizeAdminUserBatchIDs(ids)
		assert.False(t, ok)
	})
}

func TestBindVerificationOperationBatchUserContexts(t *testing.T) {
	equal := func(t *testing.T, a, b VerificationBinding) {
		t.Helper()
		assert.Equal(t, a.Scope, b.Scope)
		assert.Equal(t, a.ContextHash, b.ContextHash)
	}

	t.Run("batch manage normalizes the ID set and action", func(t *testing.T) {
		base, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeAdminUserBatchManage, Context: []byte(`{"user_ids":[1,2,3],"action":"disable"}`)})
		require.NoError(t, err)
		reordered, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeAdminUserBatchManage, Context: []byte(`{"action":"disable","user_ids":[3,2,1,2]}`)})
		require.NoError(t, err)
		equal(t, base, reordered)

		for _, invalid := range []string{
			`{"user_ids":[1,2,3],"action":"promote"}`,
			`{"user_ids":[1,2,3],"action":"delete"}`,
			`{"user_ids":[],"action":"disable"}`,
			`{"user_ids":[0],"action":"disable"}`,
			`{"user_ids":[1,2,3]}`,
			`{"user_ids":[1,2,3],"action":"disable","extra":1}`,
			`{"user_id":1,"action":"disable"}`,
		} {
			_, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeAdminUserBatchManage, Context: []byte(invalid)})
			assert.ErrorIs(t, err, ErrVerificationContextInvalid, invalid)
		}
		differentAction, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeAdminUserBatchManage, Context: []byte(`{"user_ids":[1,2,3],"action":"enable"}`)})
		require.NoError(t, err)
		assert.NotEqual(t, base.ContextHash, differentAction.ContextHash)
	})

	t.Run("batch delete normalizes the ID set", func(t *testing.T) {
		base, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeAdminUserBatchDelete, Context: []byte(`{"user_ids":[4,9]}`)})
		require.NoError(t, err)
		reordered, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeAdminUserBatchDelete, Context: []byte(`{"user_ids":[9,4,9]}`)})
		require.NoError(t, err)
		equal(t, base, reordered)

		for _, invalid := range []string{
			`{"user_ids":[]}`,
			`{"user_ids":[-1]}`,
			`{"user_ids":[4,9],"action":"delete"}`,
			`{"user_id":4}`,
		} {
			_, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeAdminUserBatchDelete, Context: []byte(invalid)})
			assert.ErrorIs(t, err, ErrVerificationContextInvalid, invalid)
		}
		subset, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeAdminUserBatchDelete, Context: []byte(`{"user_ids":[4]}`)})
		require.NoError(t, err)
		assert.NotEqual(t, base.ContextHash, subset.ContextHash)
	})

	t.Run("oversized batches are rejected", func(t *testing.T) {
		ids := make([]string, 0, MaxAdminUserBatchSize()+1)
		for i := 0; i <= MaxAdminUserBatchSize(); i++ {
			ids = append(ids, fmt.Sprint(i+1))
		}
		context := `{"user_ids":[` + strings.Join(ids, ",") + `],"action":"disable"}`
		_, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeAdminUserBatchManage, Context: []byte(context)})
		assert.ErrorIs(t, err, ErrVerificationContextInvalid)
	})

	t.Run("batch scopes differ from single-account scopes", func(t *testing.T) {
		single, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeAdminUserDelete, Context: []byte(`{"user_id":4}`)})
		require.NoError(t, err)
		batch, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeAdminUserBatchDelete, Context: []byte(`{"user_ids":[4]}`)})
		require.NoError(t, err)
		assert.NotEqual(t, single.ContextHash, batch.ContextHash)
	})
}
