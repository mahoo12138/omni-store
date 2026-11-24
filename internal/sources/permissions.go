package sources

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/db"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
)

var (
	ErrPolicyNotFound = errors.New("访问策略不存在")
	ErrPolicyName     = errors.New("访问策略名称不能为空")
)

// PolicySourceInput 是访问策略内的一条存储源授权规则。
type PolicySourceInput struct {
	SourceKey  string                `json:"source_key"`
	Permission string                `json:"permission"`
	PathRules  []PolicyPathRuleInput `json:"path_rules"`
}

// PolicyPathRuleInput 是存储源内的一条相对路径权限覆盖规则。
type PolicyPathRuleInput struct {
	PathPrefix string `json:"path_prefix"`
	Permission string `json:"permission"`
}

// PolicyInput 以整体替换语义创建或更新访问策略。
type PolicyInput struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Sources     []PolicySourceInput `json:"sources"`
	UserIDs     []int64             `json:"user_ids"`
}

// CreatePolicy 创建访问策略，并在同一事务内写入存储源规则和用户绑定。
func (s *Service) CreatePolicy(in PolicyInput) (*models.AccessPolicy, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, ErrPolicyName
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	var key string
	var result sql.Result
	for attempt := 0; attempt < 5; attempt++ {
		key = auth.NewRandomToken("pol-", 8)
		result, err = tx.Exec(`INSERT INTO access_policies
  (key, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			key, in.Name, strings.TrimSpace(in.Description), now, now)
		if err == nil || !db.IsUniqueConstraint(err, "access_policies.key") {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("创建访问策略失败: %w", err)
	}
	policyID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := replacePolicyBindings(tx, policyID, in, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetPolicy(key)
}

// UpdatePolicy 整体替换策略基本信息、存储源规则和用户绑定。
func (s *Service) UpdatePolicy(key string, in PolicyInput) (*models.AccessPolicy, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, ErrPolicyName
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var policyID int64
	if err := tx.QueryRow(`SELECT id FROM access_policies WHERE key = ?`, key).Scan(&policyID); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPolicyNotFound
	} else if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(`UPDATE access_policies SET name = ?, description = ?, updated_at = ? WHERE id = ?`,
		in.Name, strings.TrimSpace(in.Description), now, policyID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM access_policy_sources WHERE policy_id = ?`, policyID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM user_access_policies WHERE policy_id = ?`, policyID); err != nil {
		return nil, err
	}
	if err := replacePolicyBindings(tx, policyID, in, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetPolicy(key)
}

func replacePolicyBindings(tx *sql.Tx, policyID int64, in PolicyInput, now time.Time) error {
	seenSources := make(map[string]struct{}, len(in.Sources))
	for _, rule := range in.Sources {
		rule.SourceKey = strings.TrimSpace(rule.SourceKey)
		if rule.SourceKey == "" {
			return fmt.Errorf("存储源不能为空")
		}
		if rule.Permission != models.PermissionReadOnly && rule.Permission != models.PermissionReadWrite {
			return fmt.Errorf("非法权限级别: %s", rule.Permission)
		}
		if _, exists := seenSources[rule.SourceKey]; exists {
			return fmt.Errorf("存储源规则重复")
		}
		seenSources[rule.SourceKey] = struct{}{}
		var sourceID int64
		if err := tx.QueryRow(`SELECT id FROM storage_sources WHERE key = ?`, rule.SourceKey).Scan(&sourceID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO access_policy_sources
  (policy_id, storage_source_id, permission, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			policyID, sourceID, rule.Permission, now, now); err != nil {
			return err
		}
		seenPaths := make(map[string]struct{}, len(rule.PathRules))
		for _, pathRule := range rule.PathRules {
			pathPrefix, err := security.NormalizeRelPath(pathRule.PathPrefix)
			if err != nil {
				return fmt.Errorf("非法子路径 %q: %w", pathRule.PathPrefix, err)
			}
			if pathPrefix == "" {
				return fmt.Errorf("子路径规则不能指向存储源根目录")
			}
			if pathRule.Permission != models.PermissionReadOnly && pathRule.Permission != models.PermissionReadWrite {
				return fmt.Errorf("非法权限级别: %s", pathRule.Permission)
			}
			if _, exists := seenPaths[pathPrefix]; exists {
				return fmt.Errorf("子路径规则重复: %s", pathPrefix)
			}
			seenPaths[pathPrefix] = struct{}{}
			if _, err := tx.Exec(`INSERT INTO access_policy_path_rules
  (policy_id, storage_source_id, path_prefix, permission, created_at, updated_at)
  VALUES (?, ?, ?, ?, ?, ?)`, policyID, sourceID, pathPrefix, pathRule.Permission, now, now); err != nil {
				return err
			}
		}
	}

	seenUsers := make(map[int64]struct{}, len(in.UserIDs))
	for _, userID := range in.UserIDs {
		if userID <= 0 {
			return fmt.Errorf("非法用户 ID")
		}
		if _, exists := seenUsers[userID]; exists {
			continue
		}
		seenUsers[userID] = struct{}{}
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM users WHERE id = ?`, userID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("用户不存在")
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO user_access_policies (user_id, policy_id, created_at)
  VALUES (?, ?, ?)`, userID, policyID, now); err != nil {
			return err
		}
	}
	return nil
}

// DeletePolicy 删除策略及其全部规则和用户绑定。
func (s *Service) DeletePolicy(key string) error {
	result, err := s.db.Exec(`DELETE FROM access_policies WHERE key = ?`, key)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrPolicyNotFound
	}
	return nil
}

// GetPolicy 返回一条完整策略。
func (s *Service) GetPolicy(key string) (*models.AccessPolicy, error) {
	var policy models.AccessPolicy
	var description sql.NullString
	err := s.db.QueryRow(`SELECT id, key, name, description, created_at, updated_at
  FROM access_policies WHERE key = ?`, key).Scan(
		&policy.ID, &policy.Key, &policy.Name, &description, &policy.CreatedAt, &policy.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPolicyNotFound
	}
	if err != nil {
		return nil, err
	}
	policy.Description = description.String
	if err := s.loadPolicyBindings(&policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

// ListPolicies 返回完整策略列表。
func (s *Service) ListPolicies() ([]*models.AccessPolicy, error) {
	rows, err := s.db.Query(`SELECT key FROM access_policies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	policies := make([]*models.AccessPolicy, 0, len(keys))
	for _, key := range keys {
		policy, err := s.GetPolicy(key)
		if err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, nil
}

func (s *Service) loadPolicyBindings(policy *models.AccessPolicy) error {
	policy.Sources = []models.AccessPolicySourceRule{}
	policy.Users = []models.AccessPolicyUserBinding{}
	rows, err := s.db.Query(`SELECT s.key, s.name, ps.permission
  FROM access_policy_sources ps JOIN storage_sources s ON s.id = ps.storage_source_id
  WHERE ps.policy_id = ? ORDER BY s.id`, policy.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var rule models.AccessPolicySourceRule
		if err := rows.Scan(&rule.SourceKey, &rule.SourceName, &rule.Permission); err != nil {
			rows.Close()
			return err
		}
		rule.PathRules = []models.AccessPolicyPathRule{}
		policy.Sources = append(policy.Sources, rule)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sourceIndexes := make(map[string]int, len(policy.Sources))
	for i := range policy.Sources {
		sourceIndexes[policy.Sources[i].SourceKey] = i
	}
	rows, err = s.db.Query(`SELECT ss.key, pr.path_prefix, pr.permission
  FROM access_policy_path_rules pr
  JOIN storage_sources ss ON ss.id = pr.storage_source_id
  WHERE pr.policy_id = ? ORDER BY ss.id, pr.path_prefix`, policy.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var sourceKey string
		var pathRule models.AccessPolicyPathRule
		if err := rows.Scan(&sourceKey, &pathRule.PathPrefix, &pathRule.Permission); err != nil {
			rows.Close()
			return err
		}
		if index, ok := sourceIndexes[sourceKey]; ok {
			policy.Sources[index].PathRules = append(policy.Sources[index].PathRules, pathRule)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.db.Query(`SELECT u.id, u.username, u.display_name
  FROM user_access_policies up JOIN users u ON u.id = up.user_id
  WHERE up.policy_id = ? ORDER BY u.id`, policy.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var user models.AccessPolicyUserBinding
		if err := rows.Scan(&user.UserID, &user.Username, &user.DisplayName); err != nil {
			return err
		}
		policy.Users = append(policy.Users, user)
	}
	return rows.Err()
}

func permissionLevel(permission string) int {
	if permission == models.PermissionReadWrite {
		return 2
	}
	if permission == models.PermissionReadOnly {
		return 1
	}
	return 0
}

func permissionFromLevel(level int) string {
	if level >= 2 {
		return models.PermissionReadWrite
	}
	if level == 1 {
		return models.PermissionReadOnly
	}
	return ""
}

func pathPrefixMatches(relPath, prefix string) bool {
	return relPath == prefix || strings.HasPrefix(relPath, prefix+"/")
}

// permissionOf 返回用户在指定路径上从全部访问策略合并得到的权限。
// 单个策略内由最长路径前缀覆盖源级默认权限，多个策略之间取最高权限；超级管理员隐式拥有读写权限。
func (s *Service) permissionOf(user *models.User, storageSourceID int64, relPath string) (string, error) {
	if user.IsAdmin() {
		return models.PermissionReadWrite, nil
	}
	normalized, err := security.NormalizeRelPath(relPath)
	if err != nil {
		return "", err
	}
	rows, err := s.db.Query(`SELECT ps.policy_id, ps.permission, pr.path_prefix, pr.permission
  FROM user_access_policies up
  JOIN access_policy_sources ps ON ps.policy_id = up.policy_id
	LEFT JOIN access_policy_path_rules pr
	  ON pr.policy_id = ps.policy_id AND pr.storage_source_id = ps.storage_source_id
  WHERE up.user_id = ? AND ps.storage_source_id = ?
  ORDER BY ps.policy_id`, user.ID, storageSourceID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	bestLevel := 0
	var currentPolicyID int64 = -1
	currentPermission := ""
	longestPrefix := -1
	flush := func() {
		if level := permissionLevel(currentPermission); level > bestLevel {
			bestLevel = level
		}
	}
	for rows.Next() {
		var policyID int64
		var basePermission string
		var pathPrefix, pathPermission sql.NullString
		if err := rows.Scan(&policyID, &basePermission, &pathPrefix, &pathPermission); err != nil {
			return "", err
		}
		if policyID != currentPolicyID {
			if currentPolicyID != -1 {
				flush()
			}
			currentPolicyID = policyID
			currentPermission = basePermission
			longestPrefix = -1
		}
		if pathPrefix.Valid && pathPermission.Valid && pathPrefixMatches(normalized, pathPrefix.String) && len(pathPrefix.String) > longestPrefix {
			currentPermission = pathPermission.String
			longestPrefix = len(pathPrefix.String)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if currentPolicyID != -1 {
		flush()
	}
	return permissionFromLevel(bestLevel), nil
}

// CanReadSource 要求存储源存在、未禁用且至少有一个策略授予读取权限。
func (s *Service) CanReadSource(user *models.User, sourceKey string) (bool, error) {
	src, err := s.Get(sourceKey)
	if err != nil {
		return false, err
	}
	if src.IsDisabled {
		return false, nil
	}
	permission, err := s.permissionOf(user, src.ID, "")
	if err != nil {
		return false, err
	}
	return permission == models.PermissionReadOnly || permission == models.PermissionReadWrite, nil
}

// CanWriteSource 要求存储源存在、未禁用且至少有一个策略授予写入权限。
func (s *Service) CanWriteSource(user *models.User, sourceKey string) (bool, error) {
	src, err := s.Get(sourceKey)
	if err != nil {
		return false, err
	}
	if src.IsDisabled {
		return false, nil
	}
	permission, err := s.permissionOf(user, src.ID, "")
	if err != nil {
		return false, err
	}
	return permission == models.PermissionReadWrite, nil
}

// PermissionAtPath 返回用户在指定存储源路径的最终权限；禁用源不返回权限。
func (s *Service) PermissionAtPath(user *models.User, sourceKey, relPath string) (string, error) {
	src, err := s.Get(sourceKey)
	if err != nil {
		return "", err
	}
	if src.IsDisabled {
		return "", nil
	}
	return s.permissionOf(user, src.ID, relPath)
}

// CanReadPath 检查指定路径的最终读取权限。
func (s *Service) CanReadPath(user *models.User, sourceKey, relPath string) (bool, error) {
	permission, err := s.PermissionAtPath(user, sourceKey, relPath)
	if err != nil {
		return false, err
	}
	return permission == models.PermissionReadOnly || permission == models.PermissionReadWrite, nil
}

// CanWritePath 检查指定路径的最终写入权限。
func (s *Service) CanWritePath(user *models.User, sourceKey, relPath string) (bool, error) {
	permission, err := s.PermissionAtPath(user, sourceKey, relPath)
	if err != nil {
		return false, err
	}
	return permission == models.PermissionReadWrite, nil
}

// CanWriteSubtree 检查会影响整棵子树的操作，避免通过删除或移动父目录绕过更深层的只读规则。
func (s *Service) CanWriteSubtree(user *models.User, sourceKey, relPath string) (bool, error) {
	return s.canAccessSubtree(user, sourceKey, relPath, true)
}

// CanReadSubtree 检查读取整棵子树时不会穿过更深层的拒绝规则。
func (s *Service) CanReadSubtree(user *models.User, sourceKey, relPath string) (bool, error) {
	return s.canAccessSubtree(user, sourceKey, relPath, false)
}

func (s *Service) canAccessSubtree(user *models.User, sourceKey, relPath string, needWrite bool) (bool, error) {
	src, err := s.Get(sourceKey)
	if err != nil {
		return false, err
	}
	if src.IsDisabled {
		return false, nil
	}
	normalized, err := security.NormalizeRelPath(relPath)
	if err != nil {
		return false, err
	}
	permission, err := s.permissionOf(user, src.ID, normalized)
	allowed := permission == models.PermissionReadWrite || (!needWrite && permission == models.PermissionReadOnly)
	if err != nil || !allowed {
		return false, err
	}
	if user.IsAdmin() {
		return true, nil
	}
	rows, err := s.db.Query(`SELECT DISTINCT pr.path_prefix
  FROM user_access_policies up
  JOIN access_policy_path_rules pr ON pr.policy_id = up.policy_id
  WHERE up.user_id = ? AND pr.storage_source_id = ?`, user.ID, src.ID)
	if err != nil {
		return false, err
	}
	var descendants []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			rows.Close()
			return false, err
		}
		if normalized == "" || strings.HasPrefix(prefix, normalized+"/") {
			descendants = append(descendants, prefix)
		}
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, prefix := range descendants {
		permission, err := s.permissionOf(user, src.ID, prefix)
		allowed := permission == models.PermissionReadWrite || (!needWrite && permission == models.PermissionReadOnly)
		if err != nil || !allowed {
			return false, err
		}
	}
	return true, nil
}

// ListForUser 返回用户从全部策略合并后可访问的存储源视图。
func (s *Service) ListForUser(user *models.User) ([]*models.UserSourceView, error) {
	if user.IsAdmin() {
		all, err := s.List()
		if err != nil {
			return nil, err
		}
		out := []*models.UserSourceView{}
		for _, source := range all {
			if source.IsDisabled {
				continue
			}
			out = append(out, &models.UserSourceView{
				Key: source.Key, Name: source.Name, Description: source.Description,
				Permission:    models.PermissionReadWrite,
				WebdavEnabled: source.WebdavEnabled, S3Enabled: source.S3Enabled,
				QuotaBytes: source.QuotaBytes,
			})
		}
		return out, nil
	}

	rows, err := s.db.Query(`SELECT s.key, s.name, s.description,
  CASE MAX(CASE ps.permission WHEN 'read_write' THEN 2 ELSE 1 END)
    WHEN 2 THEN 'read_write' ELSE 'read_only' END,
  s.webdav_enabled, s.s3_enabled, s.quota_bytes
  FROM user_access_policies up
  JOIN access_policy_sources ps ON ps.policy_id = up.policy_id
  JOIN storage_sources s ON s.id = ps.storage_source_id
  WHERE up.user_id = ? AND s.is_disabled = 0
  GROUP BY s.id
  ORDER BY s.id`, user.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*models.UserSourceView{}
	for rows.Next() {
		var view models.UserSourceView
		var description sql.NullString
		if err := rows.Scan(&view.Key, &view.Name, &description, &view.Permission,
			&view.WebdavEnabled, &view.S3Enabled, &view.QuotaBytes); err != nil {
			return nil, err
		}
		view.Description = description.String
		out = append(out, &view)
	}
	return out, rows.Err()
}
