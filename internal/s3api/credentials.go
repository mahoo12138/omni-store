package s3api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/omni-store/omnistore/internal/auth"
	"github.com/omni-store/omnistore/internal/db"
	"github.com/omni-store/omnistore/internal/models"
)

const MaxCredentialsPerUser = 10

var (
	ErrCredentialNotFound = errors.New("S3 凭据不存在")
	ErrCredentialDisabled = errors.New("S3 凭据已禁用")
	ErrCredentialLimit    = errors.New("S3 凭据最多创建 10 个")
	ErrCredentialName     = errors.New("凭据名称必须为 1-32 个可见字符")
)

// Credential 是可返回给管理 API 的 S3 凭据元数据，不包含 Secret。
type Credential struct {
	AccessKeyID string     `json:"access_key_id"`
	Name        string     `json:"name"`
	IsDisabled  bool       `json:"is_disabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
}

const credentialTouchThrottle = time.Minute

// Credentials 管理独立于网页登录、WebDAV 和图床 Token 的 S3 Key。
type Credentials struct {
	db          *sql.DB
	rdb         *sql.DB
	dataDir     string
	configured  string
	touchMu     sync.Mutex
	lastTouched map[string]time.Time
}

func NewCredentials(db *sql.DB, dataDir, configuredMasterKey string) *Credentials {
	return &Credentials{
		db:          db,
		dataDir:     dataDir,
		configured:  strings.TrimSpace(configuredMasterKey),
		lastTouched: make(map[string]time.Time),
	}
}

// SetReadDB 注入只读连接池用于热路径签名凭据解析。
func (c *Credentials) SetReadDB(rdb *sql.DB) {
	c.rdb = rdb
}

func (c *Credentials) readDB() *sql.DB {
	if c.rdb != nil {
		return c.rdb
	}
	return c.db
}

func normalizeCredentialName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := len([]rune(name)); n < 1 || n > 32 {
		return "", ErrCredentialName
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return "", ErrCredentialName
		}
	}
	return name, nil
}

func (c *Credentials) Create(userID int64, name string) (*Credential, string, error) {
	name, err := normalizeCredentialName(name)
	if err != nil {
		return nil, "", err
	}
	key, err := c.masterKey()
	if err != nil {
		return nil, "", err
	}
	tx, err := c.db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM s3_credentials WHERE owner_user_id = ?`, userID).Scan(&count); err != nil {
		return nil, "", err
	}
	if count >= MaxCredentialsPerUser {
		return nil, "", ErrCredentialLimit
	}

	for range 5 {
		accessKeyID := "OSAK" + strings.ToUpper(auth.NewRandomToken("", 12))
		secret := auth.NewRandomToken("", 30)
		ciphertext, nonce, err := encryptSecret(key, accessKeyID, secret)
		if err != nil {
			return nil, "", err
		}
		now := time.Now().UTC()
		_, err = tx.Exec(`INSERT INTO s3_credentials
  (access_key_id, secret_access_key_encrypted, secret_key_nonce, owner_user_id, name, is_disabled, created_at)
  VALUES (?, ?, ?, ?, ?, 0, ?)`, accessKeyID, ciphertext, nonce, userID, name, now)
		if err == nil {
			if err := tx.Commit(); err != nil {
				return nil, "", err
			}
			return &Credential{AccessKeyID: accessKeyID, Name: name, CreatedAt: now}, secret, nil
		}
		if !db.IsUniqueConstraint(err, "s3_credentials.access_key_id") {
			return nil, "", fmt.Errorf("创建 S3 凭据失败: %w", err)
		}
	}
	return nil, "", fmt.Errorf("生成 Access Key ID 失败")
}

func (c *Credentials) List(userID int64) ([]*Credential, error) {
	rows, err := c.db.Query(`SELECT access_key_id, name, is_disabled, created_at, last_used_at
  FROM s3_credentials WHERE owner_user_id = ? ORDER BY created_at DESC, access_key_id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Credential{}
	for rows.Next() {
		var item Credential
		var lastUsed sql.NullTime
		if err := rows.Scan(&item.AccessKeyID, &item.Name, &item.IsDisabled, &item.CreatedAt, &lastUsed); err != nil {
			return nil, err
		}
		if lastUsed.Valid {
			item.LastUsedAt = &lastUsed.Time
		}
		out = append(out, &item)
	}
	return out, rows.Err()
}

func (c *Credentials) SetDisabled(userID int64, accessKeyID string, disabled bool) error {
	res, err := c.db.Exec(`UPDATE s3_credentials SET is_disabled = ?
  WHERE owner_user_id = ? AND access_key_id = ?`, disabled, userID, accessKeyID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

func (c *Credentials) Delete(userID int64, accessKeyID string) error {
	res, err := c.db.Exec(`DELETE FROM s3_credentials WHERE owner_user_id = ? AND access_key_id = ?`, userID, accessKeyID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

// Resolve 返回签名校验所需的用户和 Secret。用户或 Key 禁用时统一视为无效凭据。
func (c *Credentials) Resolve(accessKeyID string) (*models.User, string, error) {
	row := c.readDB().QueryRow(`SELECT
    u.id, u.user_public_id, u.username, u.display_name, u.role, u.is_disabled, u.created_at, u.updated_at,
    c.secret_access_key_encrypted, c.secret_key_nonce, c.is_disabled
  FROM s3_credentials c JOIN users u ON u.id = c.owner_user_id
  WHERE c.access_key_id = ?`, accessKeyID)
	var user models.User
	var ciphertext, nonce []byte
	var credentialDisabled bool
	err := row.Scan(&user.ID, &user.UserPublicID, &user.Username, &user.DisplayName,
		&user.Role, &user.IsDisabled, &user.CreatedAt, &user.UpdatedAt,
		&ciphertext, &nonce, &credentialDisabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrCredentialNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if user.IsDisabled || credentialDisabled {
		return nil, "", ErrCredentialDisabled
	}
	key, err := c.masterKey()
	if err != nil {
		return nil, "", err
	}
	secret, err := decryptSecret(key, accessKeyID, ciphertext, nonce)
	if err != nil {
		return nil, "", fmt.Errorf("解密 S3 Secret 失败（请检查 master key 或恢复密钥备份）: %w", err)
	}
	return &user, secret, nil
}

func (c *Credentials) Touch(accessKeyID string) {
	now := time.Now().UTC()
	c.touchMu.Lock()
	if c.lastTouched == nil {
		c.lastTouched = make(map[string]time.Time)
	}
	if prev, ok := c.lastTouched[accessKeyID]; ok && now.Sub(prev) < credentialTouchThrottle {
		c.touchMu.Unlock()
		return
	}
	if len(c.lastTouched) >= 4096 {
		clear(c.lastTouched)
	}
	c.lastTouched[accessKeyID] = now
	c.touchMu.Unlock()
	_, _ = c.db.Exec(`UPDATE s3_credentials SET last_used_at = ? WHERE access_key_id = ?`, now, accessKeyID)
}

func encryptSecret(key []byte, accessKeyID, secret string) ([]byte, []byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return gcm.Seal(nil, nonce, []byte(secret), []byte(accessKeyID)), nonce, nil
}

func decryptSecret(key []byte, accessKeyID string, ciphertext, nonce []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(accessKeyID))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (c *Credentials) masterKey() ([]byte, error) {
	if c.configured != "" {
		return decodeMasterKey(c.configured)
	}
	keyDir := filepath.Join(c.dataDir, "keys")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建密钥目录失败: %w", err)
	}
	keyPath := filepath.Join(keyDir, "s3-master.key")
	if data, err := os.ReadFile(keyPath); err == nil {
		if err := os.Chmod(keyPath, 0o600); err != nil {
			return nil, fmt.Errorf("收紧 S3 master key 权限失败: %w", err)
		}
		return decodeMasterKey(strings.TrimSpace(string(data)))
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取 S3 master key 失败: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(key) + "\n"
	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		data, readErr := os.ReadFile(keyPath)
		if readErr != nil {
			return nil, readErr
		}
		return decodeMasterKey(strings.TrimSpace(string(data)))
	}
	if err != nil {
		return nil, fmt.Errorf("创建 S3 master key 失败: %w", err)
	}
	if _, err := f.WriteString(encoded); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

func decodeMasterKey(value string) ([]byte, error) {
	decoders := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		hex.DecodeString,
	}
	for _, decode := range decoders {
		if key, err := decode(value); err == nil && len(key) == 32 {
			return key, nil
		}
	}
	if len(value) == 32 {
		return []byte(value), nil
	}
	return nil, fmt.Errorf("OMNISTORE_MASTER_KEY 必须解码为 32 字节（建议使用 base64）")
}
