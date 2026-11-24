// Package s3api implements OmniStore's deliberately small S3-compatible API.
package s3api

import (
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/omni-store/omnistore/internal/audit"
	"github.com/omni-store/omnistore/internal/files"
	"github.com/omni-store/omnistore/internal/models"
	"github.com/omni-store/omnistore/internal/security"
	"github.com/omni-store/omnistore/internal/sources"
)

const xmlNamespace = "http://s3.amazonaws.com/doc/2006-03-01/"

type Handler struct {
	credentials *Credentials
	verifier    *signatureVerifier
	sources     *sources.Service
	files       *files.Service
	audit       *audit.Logger
	proxy       *security.ProxyResolver
	logger      *slog.Logger
	maxUpload   int64
	multipart   *MultipartStore
}

func NewHandler(credentials *Credentials, sources *sources.Service, files *files.Service,
	auditLogger *audit.Logger, proxy *security.ProxyResolver, logger *slog.Logger, maxFileSizeMB int64,
	multipart *MultipartStore) *Handler {
	return &Handler{
		credentials: credentials,
		verifier:    newSignatureVerifier(credentials),
		sources:     sources, files: files, audit: auditLogger, proxy: proxy, logger: logger,
		maxUpload: maxFileSizeMB * 1024 * 1024, multipart: multipart,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", "OmniStore-S3")
	id := requestID(r)
	r.Header.Set("X-Request-ID", id)
	w.Header().Set("x-amz-request-id", id)
	authenticated, err := h.verifier.verify(r)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}

	bucket, key := splitPath(r.URL.Path)
	if bucket == "" {
		if r.Method == http.MethodGet {
			h.listBuckets(w, r, authenticated.User)
			return
		}
		h.writeError(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed", "此资源不支持该方法", "")
		return
	}

	src, err := h.sources.Get(bucket)
	if err != nil || src.IsDisabled || !src.S3Enabled {
		// 只有 s3_enabled 的源进入对象操作；未开启源按 NoSuchBucket 处理。
		h.writeError(w, r, http.StatusNotFound, "NoSuchBucket", "指定的存储源不存在", bucket)
		return
	}

	if key == "" {
		h.serveBucket(w, r, authenticated.User, src)
		return
	}
	h.serveObject(w, r, authenticated, src, key)
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, user *models.User, src *models.StorageSource, key string, write, subtree bool) bool {
	normalized, err := security.NormalizeRelPath(strings.TrimSuffix(key, "/"))
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, "InvalidObjectName", "对象 Key 非法", key)
		return false
	}
	var allowed bool
	if subtree {
		allowed, err = h.sources.CanWriteSubtree(user, src.Key, normalized)
	} else if write {
		allowed, err = h.sources.CanWritePath(user, src.Key, normalized)
	} else {
		allowed, err = h.sources.CanReadPath(user, src.Key, normalized)
	}
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "InternalError", "权限检查失败", key)
		return false
	}
	if !allowed {
		h.writeError(w, r, http.StatusForbidden, "AccessDenied", "没有访问该路径的权限", key)
		return false
	}
	return true
}

func splitPath(urlPath string) (string, string) {
	value := strings.TrimPrefix(urlPath, "/")
	if value == "" {
		return "", ""
	}
	bucket, key, found := strings.Cut(value, "/")
	if !found {
		return bucket, ""
	}
	return bucket, key
}

func (h *Handler) serveBucket(w http.ResponseWriter, r *http.Request, user *models.User, src *models.StorageSource) {
	switch {
	case r.Method == http.MethodHead && hasUnsupportedOperationQuery(r.URL.Query()):
		h.writeError(w, r, http.StatusNotImplemented, "NotImplemented", "该 S3 操作尚未实现", src.Key)
	case r.Method == http.MethodHead:
		if !h.authorize(w, r, user, src, "", false, false) {
			return
		}
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		if !h.authorize(w, r, user, src, r.URL.Query().Get("prefix"), false, false) {
			return
		}
		h.listObjectsV2(w, r, src)
	case r.Method == http.MethodPost && r.URL.Query().Has("delete"):
		h.deleteObjects(w, r, user, src)
	default:
		h.writeError(w, r, http.StatusNotImplemented, "NotImplemented", "该 S3 操作尚未实现", src.Key)
	}
}

func (h *Handler) serveObject(w http.ResponseWriter, r *http.Request, authenticated *authenticatedRequest, src *models.StorageSource, key string) {
	if !validObjectKey(key) {
		h.writeError(w, r, http.StatusBadRequest, "InvalidObjectName", "对象 Key 非法", key)
		return
	}
	query := r.URL.Query()
	write := r.Method == http.MethodPut || r.Method == http.MethodDelete || r.Method == http.MethodPost
	subtree := r.Method == http.MethodDelete && !query.Has("uploadId")
	if !h.authorize(w, r, authenticated.User, src, strings.TrimSuffix(key, "/"), write, subtree) {
		return
	}
	switch {
	case r.Method == http.MethodPost && query.Has("uploads"):
		h.createMultipartUpload(w, r, authenticated.User, src, key)
	case r.Method == http.MethodPut && query.Has("uploadId") && query.Has("partNumber"):
		h.uploadPart(w, r, authenticated, src, key)
	case r.Method == http.MethodGet && query.Has("uploadId"):
		h.listParts(w, r, authenticated.User, src, key)
	case r.Method == http.MethodPost && query.Has("uploadId"):
		h.completeMultipartUpload(w, r, authenticated, src, key)
	case r.Method == http.MethodDelete && query.Has("uploadId"):
		h.abortMultipartUpload(w, r, authenticated.User, src, key)
	case query.Has("uploadId") || query.Has("partNumber"):
		h.writeError(w, r, http.StatusBadRequest, "InvalidRequest", "Multipart 请求参数不完整", key)
	case hasUnsupportedOperationQuery(query):
		h.writeError(w, r, http.StatusNotImplemented, "NotImplemented", "该 S3 操作尚未实现", key)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		h.getObject(w, r, src, key)
	case r.Method == http.MethodPut:
		h.putObject(w, r, authenticated, src, key)
	case r.Method == http.MethodDelete:
		h.deleteObject(w, r, authenticated.User, src, key, true)
	default:
		h.writeError(w, r, http.StatusNotImplemented, "NotImplemented", "该 S3 操作尚未实现", key)
	}
}

func hasUnsupportedOperationQuery(query url.Values) bool {
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "x-amz-") || lower == "x-id" {
			continue
		}
		return true
	}
	return false
}

func validObjectKey(key string) bool {
	if key == "" {
		return false
	}
	normalized, err := security.NormalizeRelPath(key)
	if err != nil {
		return false
	}
	// 文件系统后端不能无损表达连续斜杠、反斜杠等 S3 Key；明确拒绝而不是静默别名。
	return normalized == strings.TrimSuffix(key, "/")
}

type listAllMyBucketsResult struct {
	XMLName xml.Name    `xml:"ListAllMyBucketsResult"`
	XMLNS   string      `xml:"xmlns,attr"`
	Owner   owner       `xml:"Owner"`
	Buckets bucketItems `xml:"Buckets"`
}
type owner struct{ ID, DisplayName string }
type bucketItems struct {
	Items []bucket `xml:"Bucket"`
}
type bucket struct {
	Name         string    `xml:"Name"`
	CreationDate time.Time `xml:"CreationDate"`
}

func (h *Handler) listBuckets(w http.ResponseWriter, r *http.Request, user *models.User) {
	views, err := h.sources.ListForUser(user)
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "InternalError", "列举存储源失败", "")
		return
	}
	items := make([]bucket, 0, len(views))
	for _, view := range views {
		src, err := h.sources.Get(view.Key)
		// Bucket 列表只返回启用 S3 且用户有权限的源。
		if err == nil && src.S3Enabled {
			items = append(items, bucket{Name: src.Key, CreationDate: src.CreatedAt.UTC()})
		}
	}
	h.writeXML(w, http.StatusOK, listAllMyBucketsResult{
		XMLNS:   xmlNamespace,
		Owner:   owner{ID: user.UserPublicID, DisplayName: user.DisplayName},
		Buckets: bucketItems{Items: items},
	})
}

type listBucketResult struct {
	XMLName               xml.Name       `xml:"ListBucketResult"`
	XMLNS                 string         `xml:"xmlns,attr"`
	Name                  string         `xml:"Name"`
	Prefix                string         `xml:"Prefix"`
	KeyCount              int            `xml:"KeyCount"`
	MaxKeys               int            `xml:"MaxKeys"`
	Delimiter             string         `xml:"Delimiter,omitempty"`
	IsTruncated           bool           `xml:"IsTruncated"`
	ContinuationToken     string         `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string         `xml:"NextContinuationToken,omitempty"`
	StartAfter            string         `xml:"StartAfter,omitempty"`
	EncodingType          string         `xml:"EncodingType,omitempty"`
	Contents              []object       `xml:"Contents"`
	CommonPrefixes        []commonPrefix `xml:"CommonPrefixes"`
}
type object struct {
	Key          string    `xml:"Key"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
	StorageClass string    `xml:"StorageClass"`
}
type commonPrefix struct {
	Prefix string `xml:"Prefix"`
}

func (h *Handler) listObjectsV2(w http.ResponseWriter, r *http.Request, src *models.StorageSource) {
	query := r.URL.Query()
	prefix := query.Get("prefix")
	delimiter := query.Get("delimiter")
	if delimiter != "" && delimiter != "/" {
		h.writeError(w, r, http.StatusNotImplemented, "NotImplemented", "仅支持 '/' delimiter", src.Key)
		return
	}
	maxKeys := 1000
	if raw := query.Get("max-keys"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			h.writeError(w, r, http.StatusBadRequest, "InvalidArgument", "max-keys 非法", src.Key)
			return
		}
		if value < maxKeys {
			maxKeys = value
		}
	}
	continuationToken := query.Get("continuation-token")
	startAfter := query.Get("start-after")
	after := startAfter
	if continuationToken != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(continuationToken)
		if err != nil {
			h.writeError(w, r, http.StatusBadRequest, "InvalidArgument", "continuation-token 非法", src.Key)
			return
		}
		after = string(decoded)
	}
	encodingType := query.Get("encoding-type")
	if encodingType != "" && encodingType != "url" {
		h.writeError(w, r, http.StatusBadRequest, "InvalidArgument", "encoding-type 非法", src.Key)
		return
	}

	entries, err := h.files.ListObjects(src)
	if err != nil {
		h.writeFileError(w, r, err, src.Key)
		return
	}
	result := listBucketResult{
		XMLNS: xmlNamespace, Name: src.Key, Prefix: prefix, MaxKeys: maxKeys,
		Delimiter: delimiter, ContinuationToken: continuationToken, StartAfter: startAfter,
		EncodingType: encodingType,
	}
	commonSeen := map[string]bool{}
	lastConsumed := ""
	more := false
	for _, entry := range entries {
		if entry.Key <= after || !strings.HasPrefix(entry.Key, prefix) {
			continue
		}
		displayPrefix := ""
		if delimiter != "" {
			rest := strings.TrimPrefix(entry.Key, prefix)
			if index := strings.Index(rest, delimiter); index >= 0 {
				displayPrefix = prefix + rest[:index+len(delimiter)]
				if commonSeen[displayPrefix] {
					lastConsumed = entry.Key
					continue
				}
			}
		}
		if result.KeyCount >= maxKeys {
			more = true
			break
		}
		lastConsumed = entry.Key
		if displayPrefix != "" {
			commonSeen[displayPrefix] = true
			result.CommonPrefixes = append(result.CommonPrefixes, commonPrefix{Prefix: encodeS3Value(displayPrefix, encodingType)})
		} else {
			etag, err := h.objectETag(src, entry.Key, entry.Size, entry.MTime)
			if err != nil {
				continue
			}
			result.Contents = append(result.Contents, object{
				Key: encodeS3Value(entry.Key, encodingType), LastModified: entry.MTime.UTC(),
				ETag: etag, Size: entry.Size, StorageClass: "STANDARD",
			})
		}
		result.KeyCount++
	}
	result.IsTruncated = more
	if more && lastConsumed != "" {
		result.NextContinuationToken = base64.RawURLEncoding.EncodeToString([]byte(lastConsumed))
	}
	h.writeXML(w, http.StatusOK, result)
}

func encodeS3Value(value, encodingType string) string {
	if encodingType == "url" {
		return awsEncode(value, true)
	}
	return value
}

func (h *Handler) getObject(w http.ResponseWriter, r *http.Request, src *models.StorageSource, key string) {
	if strings.HasSuffix(key, "/") {
		entry, err := h.files.Stat(src, key)
		if err == nil && entry.Type == files.TypeDir {
			w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
			w.Header().Set("Content-Length", "0")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	f, info, unlock, err := h.files.OpenForRead(src, key)
	if err != nil {
		h.writeFileError(w, r, err, key)
		return
	}
	defer unlock()
	defer f.Close()
	etag, known, err := h.multipart.ObjectETag(src.ID, key, info.Size(), info.ModTime())
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "InternalError", "读取对象 ETag 失败", key)
		return
	}
	if !known {
		etag, err = etagReader(f)
	}
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "InternalError", "计算对象 ETag 失败", key)
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "InternalError", "读取对象失败", key)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Accept-Ranges", "bytes")
	contentType := mime.TypeByExtension(path.Ext(key))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, path.Base(key), info.ModTime(), f)
}

func (h *Handler) putObject(w http.ResponseWriter, r *http.Request, authenticated *authenticatedRequest, src *models.StorageSource, key string) {
	if err := validatePayloadHash(authenticated.PayloadHash); err != nil {
		code := "InvalidRequest"
		status := http.StatusBadRequest
		if strings.HasPrefix(authenticated.PayloadHash, "STREAMING-") {
			code, status = "NotImplemented", http.StatusNotImplemented
		}
		h.writeError(w, r, status, code, err.Error(), key)
		return
	}
	streaming := authenticated.PayloadHash == streamingUnsignedTrailer
	if !streaming && r.ContentLength > h.maxUpload && h.maxUpload > 0 {
		h.writeError(w, r, http.StatusRequestEntityTooLarge, "EntityTooLarge", "对象超过上传大小限制", key)
		return
	}
	if strings.HasSuffix(key, "/") {
		trimmed := strings.TrimSuffix(key, "/")
		parent, name := path.Split(trimmed)
		parent = strings.TrimSuffix(parent, "/")
		if err := h.files.EnsureObjectParents(src, trimmed); err != nil {
			h.writeFileError(w, r, err, key)
			return
		}
		_, err := h.files.Mkdir(src, parent, name)
		if err != nil && !errors.Is(err, files.ErrAlreadyExists) {
			h.writeFileError(w, r, err, key)
			return
		}
		w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
		w.WriteHeader(http.StatusOK)
		return
	}
	createdParents, err := h.files.EnsureObjectParentsCreated(src, key)
	if err != nil {
		h.writeFileError(w, r, err, key)
		return
	}
	body := io.Reader(r.Body)
	var chunked *awsChunkedReader
	if streaming {
		var err error
		chunked, err = newAWSChunkedReader(r.Body, r.Header.Get("X-Amz-Decoded-Content-Length"), r.Header.Get("X-Amz-Trailer"))
		if err != nil {
			h.files.CleanupEmptyDirs(src, createdParents)
			h.writeError(w, r, http.StatusBadRequest, "InvalidRequest", err.Error(), key)
			return
		}
		body = chunked
	}
	if h.maxUpload > 0 {
		body = http.MaxBytesReader(w, io.NopCloser(body), h.maxUpload)
	}
	if authenticated.PayloadHash != "UNSIGNED-PAYLOAD" && !streaming {
		body = newVerifyingReader(body, authenticated.PayloadHash)
	}
	md5Hash := md5.New()
	body = io.TeeReader(body, md5Hash)
	dir, filename := path.Split(key)
	dir = strings.TrimSuffix(dir, "/")
	_, _, err = h.files.UploadWithLockTokens(src, dir, filename, body, true, nil, &authenticated.User.ID)
	if err != nil {
		h.files.CleanupEmptyDirs(src, createdParents)
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			h.writeError(w, r, http.StatusRequestEntityTooLarge, "EntityTooLarge", "对象超过上传大小限制", key)
			return
		}
		if isPayloadReadError(err) {
			h.writePayloadError(w, r, err, key)
			return
		}
		h.writeFileError(w, r, err, key)
		return
	}
	etag := `"` + hex.EncodeToString(md5Hash.Sum(nil)) + `"`
	w.Header().Set("ETag", etag)
	if chunked != nil && chunked.trailerValue != "" {
		w.Header().Set(chunked.checksumName, chunked.trailerValue)
	}
	w.WriteHeader(http.StatusOK)
	if err := h.multipart.ForgetObjectETag(src.ID, key); err != nil {
		h.logger.Warn("清理旧 S3 Multipart ETag 失败", "err", err, "storage_source_id", src.ID, "key", key)
	}
	h.logMutation(r, authenticated.User, "put_object", src, key, audit.StatusSuccess, "")
}

func (h *Handler) deleteObject(w http.ResponseWriter, r *http.Request, user *models.User, src *models.StorageSource, key string, writeResponse bool) bool {
	entry, err := h.files.Stat(src, key)
	if errors.Is(err, files.ErrNotFound) {
		if writeResponse {
			w.WriteHeader(http.StatusNoContent)
		}
		return true
	}
	if err != nil {
		if writeResponse {
			h.writeFileError(w, r, err, key)
		}
		return false
	}
	if entry.Type == files.TypeDir {
		listing, listErr := h.files.List(src, key, files.ListOptions{Page: 1, PageSize: 1}, false)
		if listErr != nil || listing.Total > 0 || !strings.HasSuffix(key, "/") {
			if writeResponse {
				w.WriteHeader(http.StatusNoContent)
			}
			return true
		}
	}
	if err := h.files.Delete(src, key); err != nil && !errors.Is(err, files.ErrNotFound) {
		if writeResponse {
			h.writeFileError(w, r, err, key)
		}
		return false
	}
	if writeResponse {
		w.WriteHeader(http.StatusNoContent)
	}
	if err := h.multipart.ForgetObjectETag(src.ID, key); err != nil {
		h.logger.Warn("清理 S3 Multipart ETag 失败", "err", err, "storage_source_id", src.ID, "key", key)
	}
	h.logMutation(r, user, "delete_object", src, key, audit.StatusSuccess, "")
	return true
}

type deleteRequest struct {
	Objects []struct {
		Key string `xml:"Key"`
	} `xml:"Object"`
	Quiet bool `xml:"Quiet"`
}
type deleteResult struct {
	XMLName xml.Name      `xml:"DeleteResult"`
	XMLNS   string        `xml:"xmlns,attr"`
	Deleted []deletedItem `xml:"Deleted"`
	Errors  []deleteError `xml:"Error"`
}
type deletedItem struct {
	Key string `xml:"Key"`
}
type deleteError struct{ Key, Code, Message string }

func (h *Handler) deleteObjects(w http.ResponseWriter, r *http.Request, user *models.User, src *models.StorageSource) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, "MalformedXML", "无法读取删除请求", src.Key)
		return
	}
	if contentMD5 := r.Header.Get("Content-MD5"); contentMD5 != "" {
		sum := md5.Sum(body)
		expected, err := base64.StdEncoding.DecodeString(contentMD5)
		if err != nil || subtle.ConstantTimeCompare(sum[:], expected) != 1 {
			h.writeError(w, r, http.StatusBadRequest, "BadDigest", "Content-MD5 不匹配", src.Key)
			return
		}
	}
	var request deleteRequest
	if err := xml.Unmarshal(body, &request); err != nil || len(request.Objects) > 1000 {
		h.writeError(w, r, http.StatusBadRequest, "MalformedXML", "删除请求 XML 非法", src.Key)
		return
	}
	result := deleteResult{XMLNS: xmlNamespace}
	for _, item := range request.Objects {
		if !validObjectKey(item.Key) {
			result.Errors = append(result.Errors, deleteError{Key: item.Key, Code: "InvalidObjectName", Message: "对象 Key 非法"})
			continue
		}
		if !h.authorizeDeleteItem(user, src, item.Key) {
			result.Errors = append(result.Errors, deleteError{Key: item.Key, Code: "AccessDenied", Message: "没有访问该路径的权限"})
			continue
		}
		if h.deleteObject(w, r, user, src, item.Key, false) {
			if !request.Quiet {
				result.Deleted = append(result.Deleted, deletedItem{Key: item.Key})
			}
		} else {
			result.Errors = append(result.Errors, deleteError{Key: item.Key, Code: "InternalError", Message: "删除对象失败"})
		}
	}
	h.writeXML(w, http.StatusOK, result)
}

func (h *Handler) authorizeDeleteItem(user *models.User, src *models.StorageSource, key string) bool {
	allowed, err := h.sources.CanWriteSubtree(user, src.Key, strings.TrimSuffix(key, "/"))
	return err == nil && allowed
}

func (h *Handler) objectETag(src *models.StorageSource, key string, size int64, mtime time.Time) (string, error) {
	if etag, known, err := h.multipart.ObjectETag(src.ID, key, size, mtime); err != nil {
		return "", err
	} else if known {
		return etag, nil
	}
	f, _, unlock, err := h.files.OpenForRead(src, key)
	if err != nil {
		return "", err
	}
	defer unlock()
	defer f.Close()
	return etagReader(f)
}

func etagReader(r io.Reader) (string, error) {
	h := md5.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return `"` + hex.EncodeToString(h.Sum(nil)) + `"`, nil
}

type verifyingReader struct {
	r        io.Reader
	hash     hash.Hash
	expected []byte
	done     bool
}

func newVerifyingReader(r io.Reader, expectedHex string) io.Reader {
	expected, _ := hex.DecodeString(expectedHex)
	return &verifyingReader{r: r, hash: sha256.New(), expected: expected}
}

func (r *verifyingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		_, _ = r.hash.Write(p[:n])
	}
	if err == io.EOF && !r.done {
		r.done = true
		if subtle.ConstantTimeCompare(r.hash.Sum(nil), r.expected) != 1 {
			return n, fmt.Errorf("x-amz-content-sha256 与请求体不匹配")
		}
	}
	return n, err
}

func (h *Handler) writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrRequestExpired):
		h.writeError(w, r, http.StatusForbidden, "RequestTimeTooSkewed", err.Error(), "")
	case errors.Is(err, ErrSignatureMismatch):
		h.writeError(w, r, http.StatusForbidden, "SignatureDoesNotMatch", err.Error(), "")
	case errors.Is(err, ErrMissingAuthentication), errors.Is(err, ErrInvalidAuthentication):
		h.writeError(w, r, http.StatusForbidden, "InvalidAccessKeyId", err.Error(), "")
	default:
		h.logger.Error("S3 鉴权失败", "err", err)
		h.writeError(w, r, http.StatusInternalServerError, "InternalError", "S3 鉴权失败", "")
	}
}

func (h *Handler) writeFileError(w http.ResponseWriter, r *http.Request, err error, resource string) {
	switch {
	case errors.Is(err, files.ErrNotFound):
		h.writeError(w, r, http.StatusNotFound, "NoSuchKey", "指定对象不存在", resource)
	case errors.Is(err, files.ErrPathExcluded), errors.Is(err, files.ErrUnsupported):
		h.writeError(w, r, http.StatusForbidden, "AccessDenied", "对象不可访问", resource)
	case errors.Is(err, files.ErrLocked):
		h.writeError(w, r, http.StatusLocked, "OperationAborted", "对象被锁定", resource)
	case errors.Is(err, files.ErrQuotaExceeded):
		h.writeError(w, r, http.StatusInsufficientStorage, "InsufficientStorage", "存储源可用空间不足", resource)
	case errors.Is(err, files.ErrInvalid):
		h.writeError(w, r, http.StatusBadRequest, "InvalidObjectName", err.Error(), resource)
	default:
		h.logger.Error("S3 文件操作失败", "err", err, "resource", resource)
		h.writeError(w, r, http.StatusInternalServerError, "InternalError", "文件操作失败", resource)
	}
}

type s3Error struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource,omitempty"`
	RequestID string   `xml:"RequestId"`
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, status int, code, message, resource string) {
	if r.Method == http.MethodHead {
		// HEAD 响应不携带错误 XML；正文与 GET 的表示元数据保持一致语义。
		w.WriteHeader(status)
		return
	}
	h.writeXML(w, status, s3Error{Code: code, Message: message, Resource: resource, RequestID: requestID(r)})
}

func (h *Handler) writeXML(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(value)
}

func (h *Handler) logMutation(r *http.Request, user *models.User, action string, src *models.StorageSource, key, status, errorCode string) {
	h.audit.Log(audit.Entry{
		ActorType: audit.ActorUser, ActorUserID: &user.ID, EntryType: audit.EntryS3,
		Action: action, StorageSourceID: &src.ID, RelativePath: key,
		IPAddress: h.proxy.ClientIP(r), UserAgent: r.UserAgent(), Status: status, ErrorCode: errorCode,
	})
}

func requestID(r *http.Request) string {
	if value := r.Header.Get("X-Request-ID"); value != "" {
		return value
	}
	return fmt.Sprintf("s3-%d", time.Now().UnixNano())
}
