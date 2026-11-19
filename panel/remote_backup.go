package main

// ---------- 异地备份：七牛云 Kodo + Cloudflare R2（v1.3.11） ----------
// 定时备份（每天 03:00，点“立即备份”也一样）写完本地明文 JSON 后，
// 把当次备份加密成信封、各传一份到 R2 与七牛，两个都传、互为备份。
// 本地备份文件保持原样（明文 JSON，现有恢复路径不动）；加密只发生在上传
// 那一步，远程存的是 .enc 加密包。手动导出（浏览器下载）不上传。
//
// 密钥存取（方案 a）：面板存派生出的 32 字节主密钥（config.json，600 权限），
// 口令本身任何地方都不存。主密钥 = PBKDF2-HMAC-SHA256（口令，固定盐 S，
// 10 万次，32 字节），S 在设口令时生成、同样存 config.json，且每个备份的
// 信封里也带一份 S——新机器上没有 config.json 时，用户输口令、按信封里
// 的 S 重新派生就能解开。口令错了 GCM 认证失败，报错就是“口令不对”。
// 换口令只换主密钥，S 不变：旧备份仍用旧口令 + 同一个 S 解，UI 必须警告。
// 威胁模型：防的是桶侧泄露（桶配错、凭据泄露、厂商侧问题），不是本机被
// 攻破——本机丢了的话本地明文备份本来就已经在攻击者手里了。
//
// 信封格式（魔数＋版本＋盐＋nonce＋密文，以后能升级）：
//   "RPBK"（4 字节）‖ 0x01（版本）‖ S（16 字节）‖ nonce（12 字节）‖
//   AES-256-GCM 密文。密钥是主密钥（AES-256）。
// 口令、主密钥、云端 SecretKey 绝不进日志、审计、导出、备份文件。
// 上传是异步的（goroutine），失败重试 3 次，再失败发 Telegram 提醒，
// 本地备份成功与上传结果互不影响；远程只留最近 7 份，传完自动裁剪。

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ---------- 配置（存在 config.json 的 remote_backup 一节） ----------

const (
	remoteBackupKeep     = 7                     // 远程保留份数（与本地定时备份默认一致）
	remoteBackupSaltLen  = 16                    // KDF 固定盐 S 的长度
	remoteBackupMinPass  = 8                     // 口令最短长度
	remoteBackupMaxTries = 3                     // 每目的地上传重试次数（含第一次）
	remoteBackupTestKey  = "remote-backup-test-" // 测试对象键前缀（不在 backup- 前缀里，裁剪碰不到）
)

// RemoteBackupDest 一个异地目的地（R2 或七牛）：开关、桶、凭据、上次状态。
// SecretKey 只写不读：任何接口都不返回它，前端留空 = 保持原值。
type RemoteBackupDest struct {
	Enabled    bool   `json:"enabled"`
	Bucket     string `json:"bucket"`
	AccountID  string `json:"account_id,omitempty"` // R2：账号 ID（端点由它拼出来）
	Region     string `json:"region,omitempty"`     // 七牛：区域 ID，如 cn-east-1；R2 固定 auto
	Endpoint   string `json:"endpoint,omitempty"`   // 端点覆盖：留空用默认；R2 填了就直接用它（测试/自建网关用）
	AccessKey  string `json:"access_key,omitempty"`
	SecretKey  string `json:"secret_key,omitempty"`
	LastStatus string `json:"last_status,omitempty"` // 成功/失败的一句话，如“成功 2026-11-19 03:00”
	LastAt     string `json:"last_at,omitempty"`     // RFC3339
}

// RemoteBackupCfg 异地备份总配置：口令只存“设没设”，主密钥与盐存派生值。
type RemoteBackupCfg struct {
	PassphraseSet bool             `json:"passphrase_set"`
	Key           string           `json:"key,omitempty"`  // base64（主密钥 32 字节）
	Salt          string           `json:"salt,omitempty"` // base64（固定盐 S 16 字节）
	R2            RemoteBackupDest `json:"r2"`
	Qiniu         RemoteBackupDest `json:"qiniu"`
}

// qiniuRegions 七牛 Kodo S3 兼容端点的区域表（七牛开发者中心 2026-11-13 更新版）：
// 端点统一是 https://s3.<区域>.qiniucs.com。默认华东-浙江。
func qiniuRegions() []struct{ ID, Name string } {
	return []struct{ ID, Name string }{
		{"cn-east-1", "华东-浙江"}, {"cn-east-2", "华东-浙江2"},
		{"cn-north-1", "华北-河北"}, {"cn-south-1", "华南-广东"},
		{"us-north-1", "北美-洛杉矶"}, {"ap-southeast-1", "亚太-新加坡"},
		{"ap-southeast-2", "亚太-河内"}, {"ap-southeast-3", "亚太-胡志明"},
	}
}

func validQiniuRegion(id string) bool {
	for _, r := range qiniuRegions() {
		if r.ID == id {
			return true
		}
	}
	return false
}

// destEndpoint 目的地实际用的 S3 端点：R2 默认按账号 ID 拼，七牛默认按区域拼；
// 两边都允许填 endpoint 覆盖（R2 覆盖主要给测试/自建 S3 网关用）。
func destEndpoint(kind string, d RemoteBackupDest) string {
	if ep := strings.TrimSpace(d.Endpoint); ep != "" {
		return strings.TrimRight(ep, "/")
	}
	if kind == "r2" {
		return "https://" + strings.TrimSpace(d.AccountID) + ".r2.cloudflarestorage.com"
	}
	region := strings.TrimSpace(d.Region)
	if region == "" {
		region = "cn-east-1"
	}
	return "https://s3." + region + ".qiniucs.com"
}

// destRegion 目的地实际用的 SigV4 区域：R2 固定 auto，七牛用区域 ID。
func destRegion(kind string, d RemoteBackupDest) string {
	if kind == "r2" {
		return "auto"
	}
	if r := strings.TrimSpace(d.Region); r != "" {
		return r
	}
	return "cn-east-1"
}

func (s *Server) remoteDest(kind string) *RemoteBackupDest {
	if kind == "r2" {
		return &s.cfg.RemoteBackup.R2
	}
	return &s.cfg.RemoteBackup.Qiniu
}

// ---------- 口令与信封加密 ----------

var (
	remoteBackupMagic           = []byte("RPBK")
	remoteBackupVersion    byte = 0x01
	errRemoteBadEnvelope        = errors.New("备份文件格式不对")
	errRemoteBadPassphrase      = errors.New("口令不对，解不开这个备份")
)

// deriveRemoteBackupKey 主密钥 = PBKDF2-HMAC-SHA256（口令，盐 S，10 万次，32 字节），
// 复用 auth.go 的 pbkdf2SHA256 与 pbkdf2Iterations（与登录密码同一路子）。
func deriveRemoteBackupKey(passphrase string, salt []byte) []byte {
	return pbkdf2SHA256([]byte(passphrase), salt, pbkdf2Iterations, 32)
}

// remoteBackupMasterKey 读面板存的主密钥（base64 解回 32 字节）。
func (s *Server) remoteBackupMasterKey() ([]byte, error) {
	if s.cfg.RemoteBackup.Key == "" || s.cfg.RemoteBackup.Salt == "" {
		return nil, errors.New("还没设置异地备份口令")
	}
	key, err := base64.StdEncoding.DecodeString(s.cfg.RemoteBackup.Key)
	if err != nil || len(key) != 32 {
		return nil, errors.New("异地备份口令损坏，请重新设置口令")
	}
	return key, nil
}

// remoteBackupSalt 读面板存的固定盐 S（base64 解回 16 字节）。
func (s *Server) remoteBackupSalt() ([]byte, error) {
	salt, err := base64.StdEncoding.DecodeString(s.cfg.RemoteBackup.Salt)
	if err != nil || len(salt) != remoteBackupSaltLen {
		return nil, errors.New("异地备份口令损坏，请重新设置口令")
	}
	return salt, nil
}

// encryptRemoteBackup 加密一份备份：信封 = 魔数‖版本‖盐 S‖nonce‖密文。
// key 是主密钥（AES-256），salt 是固定盐 S（信封里带一份，供新机器恢复时重派生）。
func encryptRemoteBackup(key, salt, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 4+1+len(salt)+len(nonce)+len(plaintext)+g.Overhead())
	out = append(out, remoteBackupMagic...)
	out = append(out, remoteBackupVersion)
	out = append(out, salt...)
	out = append(out, nonce...)
	out = g.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// decryptRemoteBackup 解一份远程备份：用户输口令、按信封里的盐重派生主密钥再解。
// 口令错了 GCM 认证失败 → errRemoteBadPassphrase；信封本身不对 → errRemoteBadEnvelope。
func decryptRemoteBackup(passphrase string, envelope []byte) ([]byte, error) {
	if len(envelope) < 4+1+remoteBackupSaltLen+12 {
		return nil, errRemoteBadEnvelope
	}
	if !bytes.Equal(envelope[:4], remoteBackupMagic) {
		return nil, errRemoteBadEnvelope
	}
	if envelope[4] != remoteBackupVersion {
		return nil, fmt.Errorf("备份版本不支持（版本 %d）", envelope[4])
	}
	salt := envelope[5 : 5+remoteBackupSaltLen]
	nonce := envelope[5+remoteBackupSaltLen : 5+remoteBackupSaltLen+12]
	ct := envelope[5+remoteBackupSaltLen+12:]
	key := deriveRemoteBackupKey(passphrase, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	pt, err := g.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errRemoteBadPassphrase
	}
	return pt, nil
}

// verifyRemoteBackupPassphrase 校验旧口令：按存的盐派生、与存的主密钥常数时间比较。
func (s *Server) verifyRemoteBackupPassphrase(passphrase string) bool {
	key, err := s.remoteBackupMasterKey()
	if err != nil {
		return false
	}
	salt, err := s.remoteBackupSalt()
	if err != nil {
		return false
	}
	want := deriveRemoteBackupKey(passphrase, salt)
	return subtle.ConstantTimeCompare(want, key) == 1
}

// ---------- S3 兼容客户端（只用标准库，SigV4 签名） ----------
// R2 与七牛 Kodo 都走 S3 协议，同一套客户端：path-style 地址
// {endpoint}/{bucket}/{key}，PUT/GET/DELETE/ListObjectsV2。
// 签名是标准 AWS SigV4（HMAC-SHA256 链），不引新依赖。

type s3Client struct {
	endpoint   string // 不带末尾斜杠，如 https://xxx.r2.cloudflarestorage.com
	region     string
	accessKey  string
	secretKey  string
	bucket     string
	httpClient *http.Client
}

func newS3Client(endpoint, region, accessKey, secretKey, bucket string) *s3Client {
	return &s3Client{
		endpoint:   strings.TrimRight(strings.TrimSpace(endpoint), "/"),
		region:     region,
		accessKey:  accessKey,
		secretKey:  secretKey,
		bucket:     bucket,
		httpClient: &http.Client{},
	}
}

// s3Object 列表里的一条对象。
type s3Object struct {
	Key          string
	Size         int64
	LastModified string
}

// s3Error S3 返回的错误体（只取 Code/Message，不含任何凭据）。
type s3Error struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *s3Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("S3 错误（HTTP %d，%s）：%s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("S3 错误（HTTP %d，%s）", e.StatusCode, e.Code)
}

// s3Do 发一次带 SigV4 签名的请求。body 为 nil = 空体。
// 超时由调用方用 context 控制。
func (c *s3Client) s3Do(ctx context.Context, method, key string, query url.Values, body []byte, contentType string) (int, []byte, error) {
	u := c.endpoint + "/" + c.bucket + "/" + key
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, nil, err
	}
	payloadHash := sha256HexBytes(body)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	signV4(req, payloadHash, c.accessKey, c.secretKey, c.region, "s3", time.Now())
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, respBody, parseS3Error(resp.StatusCode, respBody)
	}
	return resp.StatusCode, respBody, nil
}

func parseS3Error(status int, body []byte) error {
	var e struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}
	if xml.Unmarshal(body, &e) == nil && e.Code != "" {
		return &s3Error{StatusCode: status, Code: e.Code, Message: e.Message}
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return &s3Error{StatusCode: status, Code: "HTTP", Message: msg}
}

// PutObject 上传一个对象（备份 .enc 文件）。
func (c *s3Client) PutObject(ctx context.Context, key string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	_, _, err := c.s3Do(ctx, http.MethodPut, key, nil, data, "application/octet-stream")
	return err
}

// GetObject 下载一个对象。
func (c *s3Client) GetObject(ctx context.Context, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	_, body, err := c.s3Do(ctx, http.MethodGet, key, nil, nil, "")
	return body, err
}

// DeleteObject 删除一个对象。
func (c *s3Client) DeleteObject(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, _, err := c.s3Do(ctx, http.MethodDelete, key, nil, nil, "")
	return err
}

// ListObjects 列出指定前缀的对象（ListObjectsV2，新的在前按 Key 排——
// 备份键里带时间戳，Key 字典序就是时间序）。
func (c *s3Client) ListObjects(ctx context.Context, prefix string) ([]s3Object, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	q := url.Values{}
	q.Set("list-type", "2")
	q.Set("prefix", prefix)
	_, body, err := c.s3Do(ctx, http.MethodGet, "", q, nil, "")
	if err != nil {
		return nil, err
	}
	var res struct {
		XMLName  xml.Name `xml:"ListBucketResult"`
		Contents []struct {
			Key          string `xml:"Key"`
			LastModified string `xml:"LastModified"`
			Size         int64  `xml:"Size"`
		} `xml:"Contents"`
	}
	if err := xml.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("S3 列表解析失败：%w", err)
	}
	out := make([]s3Object, 0, len(res.Contents))
	for _, it := range res.Contents {
		out = append(out, s3Object{Key: it.Key, Size: it.Size, LastModified: it.LastModified})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key > out[j].Key })
	return out, nil
}

// ---------- SigV4 签名 ----------

func sha256HexBytes(b []byte) string {
	h := sha256Sum(b)
	return hex.EncodeToString(h)
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// signV4 给请求加上 AWS SigV4 签名（S3）。签名的头只有三个：
// host、x-amz-content-sha256、x-amz-date；body 哈希用实算值（不用
// UNSIGNED-PAYLOAD，R2 与七牛都认实算哈希）。
func signV4(req *http.Request, payloadHash, accessKey, secretKey, region, service string, t time.Time) {
	now := t.UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	host := req.URL.Host
	// 规范请求：方法、规范 URI、规范查询串、规范头、已签头、body 哈希。
	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := canonicalQueryString(req.URL.Query())
	canonicalHeaders := "host:" + host + "\n" + "x-amz-content-sha256:" + payloadHash + "\n" + "x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		req.Method, canonicalURI, canonicalQuery, canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")
	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sha256Sum([]byte(canonicalRequest)))
	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

// canonicalQueryString 规范查询串：按 key 排序，RFC3986 编码（空格是 %20 不是 +）。
func canonicalQueryString(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, rfc3986Escape(k)+"="+rfc3986Escape(v))
		}
	}
	return strings.Join(parts, "&")
}

func rfc3986Escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

// decryptRemoteBackupWithKey 用现成主密钥解信封（测试上传核对用；
// 恢复走 decryptRemoteBackup，用用户输的口令现场派生）。
func decryptRemoteBackupWithKey(key, envelope []byte) ([]byte, error) {
	if len(envelope) < 4+1+remoteBackupSaltLen+12 {
		return nil, errRemoteBadEnvelope
	}
	if !bytes.Equal(envelope[:4], remoteBackupMagic) {
		return nil, errRemoteBadEnvelope
	}
	if envelope[4] != remoteBackupVersion {
		return nil, fmt.Errorf("备份版本不支持（版本 %d）", envelope[4])
	}
	nonce := envelope[5+remoteBackupSaltLen : 5+remoteBackupSaltLen+12]
	ct := envelope[5+remoteBackupSaltLen+12:]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	pt, err := g.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errRemoteBadPassphrase
	}
	return pt, nil
}

// ---------- 上传编排 ----------

func destName(kind string) string {
	if kind == "r2" {
		return "R2"
	}
	return "七牛云"
}

func (s *Server) remoteBackupAnyEnabled() bool {
	return s.cfg.RemoteBackup.R2.Enabled || s.cfg.RemoteBackup.Qiniu.Enabled
}

// remoteObjectKey 远程对象键：本地备份 id + .json.enc（前缀 backup-，
// 与本地文件名对应，列表与裁剪都按这个前缀来）。
func remoteObjectKey(id string) string { return id + ".json.enc" }

// uploadBackupToRemoteAsync 定时备份写完后异步上传（goroutine，绝不阻塞
// 备份循环与面板）。同一份备份正在传时不重复起。
func (s *Server) uploadBackupToRemoteAsync(id string) {
	if !s.remoteBackupAnyEnabled() {
		return
	}
	s.remoteUploadMu.Lock()
	if s.remoteUploading == nil {
		s.remoteUploading = map[string]bool{}
	}
	if s.remoteUploading[id] {
		s.remoteUploadMu.Unlock()
		return
	}
	s.remoteUploading[id] = true
	s.remoteUploadMu.Unlock()
	go func() {
		defer func() {
			s.remoteUploadMu.Lock()
			delete(s.remoteUploading, id)
			s.remoteUploadMu.Unlock()
		}()
		s.uploadBackupToRemote(id)
	}()
}

// uploadBackupToRemote 同步上传一份备份到所有开启的目的地（测试直接调它；
// 线上由 uploadBackupToRemoteAsync 包一层 goroutine）。本地备份成功与
// 上传结果互不影响：上传失败只记状态、记审计、发 Telegram，不动本地备份。
func (s *Server) uploadBackupToRemote(id string) {
	data, err := s.readScheduledBackup(id)
	if err != nil {
		return // 本地文件没了就跳过
	}
	key, err := s.remoteBackupMasterKey()
	if err != nil {
		s.audit("system", "异地备份上传", id, "失败", "口令没设置，跳过上传")
		return
	}
	salt, err := s.remoteBackupSalt()
	if err != nil {
		s.audit("system", "异地备份上传", id, "失败", "口令损坏，跳过上传")
		return
	}
	env, err := encryptRemoteBackup(key, salt, data)
	if err != nil {
		s.audit("system", "异地备份上传", id, "失败", "加密失败："+err.Error())
		return
	}
	for _, kind := range []string{"r2", "qiniu"} {
		d := *s.remoteDest(kind)
		if !d.Enabled {
			continue
		}
		if err := s.uploadToDest(kind, d, remoteObjectKey(id), env); err != nil {
			s.setRemoteDestStatus(kind, "失败："+err.Error())
			s.sendTelegram("异地备份上传失败：" + destName(kind) + "，" + err.Error())
			s.audit("system", "异地备份上传", id, "失败", destName(kind)+"："+err.Error())
			continue
		}
		s.setRemoteDestStatus(kind, "成功 "+time.Now().Format("2006-01-02 15:04"))
		s.audit("system", "异地备份上传", id, "成功", destName(kind))
	}
}

// remoteBackupRetryDelay 上传重试的退避（第 try 次失败后睡多久）；
// 包级变量只为测试能改成 0，线上默认 5 秒、15 秒。
var remoteBackupRetryDelay = func(try int) time.Duration { return time.Duration(5*try) * time.Second }

// uploadToDest 传一个对象到一个目的地：失败重试 3 次（退避 5 秒、15 秒），
// 4xx（配错、没权限、桶不存在）不重试；传完裁剪远程旧份（裁剪失败不算
// 上传失败，只记审计）。
func (s *Server) uploadToDest(kind string, d RemoteBackupDest, key string, data []byte) error {
	client, err := s.remoteS3Client(kind, d)
	if err != nil {
		return err
	}
	var lastErr error
	for try := 1; try <= remoteBackupMaxTries; try++ {
		if err := client.PutObject(context.Background(), key, data); err == nil {
			if err := s.pruneRemoteBackups(client); err != nil {
				s.audit("system", "异地备份裁剪", "", "失败", destName(kind)+"："+err.Error())
			}
			return nil
		} else {
			lastErr = err
			if !s3Retryable(err) {
				break
			}
		}
		if try < remoteBackupMaxTries {
			time.Sleep(remoteBackupRetryDelay(try))
		}
	}
	return lastErr
}

// s3Retryable 该不该重试：网络错与 5xx 重试；4xx（配错、没权限、桶不
// 存在）重试也没用，直接报。
func s3Retryable(err error) bool {
	var se *s3Error
	if errors.As(err, &se) {
		return se.StatusCode >= 500
	}
	return true
}

// remoteBackupKeyOrder 解析备份键 backup-YYYYMMDD-HHMMSS[-N].json.enc，
// 返回（时间戳，序号），复用 backupIDOrder。
func remoteBackupKeyOrder(key string) (string, int) {
	return backupIDOrder(strings.TrimSuffix(key, ".json.enc"))
}

// pruneRemoteBackups 远程轮转：只留最近 remoteBackupKeep 份，删最旧的。
// 按（时间戳，序号）倒序排，不只按 Key 字典序（见 remoteBackupKeyOrder）。
func (s *Server) pruneRemoteBackups(client *s3Client) error {
	objs, err := client.ListObjects(context.Background(), "backup-")
	if err != nil {
		return err
	}
	if len(objs) <= remoteBackupKeep {
		return nil
	}
	sort.Slice(objs, func(i, j int) bool {
		ti, ni := remoteBackupKeyOrder(objs[i].Key)
		tj, nj := remoteBackupKeyOrder(objs[j].Key)
		if ti != tj {
			return ti > tj
		}
		return ni > nj
	})
	for _, o := range objs[remoteBackupKeep:] {
		_ = client.DeleteObject(context.Background(), o.Key)
	}
	return nil
}

// setRemoteDestStatus 记一个目的地的上次上传状态并落盘。只记状态；与设置页
// 保存同时写 config.json 时后写者胜，丢一次状态不影响备份本身。
func (s *Server) setRemoteDestStatus(kind, status string) {
	d := s.remoteDest(kind)
	d.LastStatus = status
	d.LastAt = time.Now().UTC().Format(time.RFC3339)
	_ = s.saveConfig()
}

// remoteS3Client 按目的地配置建 S3 客户端（缺桶名/密钥/端点不对时直接报，
// 不发请求）。
func (s *Server) remoteS3Client(kind string, d RemoteBackupDest) (*s3Client, error) {
	if strings.TrimSpace(d.Bucket) == "" {
		return nil, errors.New("桶名没填")
	}
	if strings.TrimSpace(d.AccessKey) == "" || strings.TrimSpace(d.SecretKey) == "" {
		return nil, errors.New("密钥没填")
	}
	ep := destEndpoint(kind, d)
	if _, err := url.Parse(ep); err != nil || !strings.HasPrefix(ep, "http://") && !strings.HasPrefix(ep, "https://") {
		return nil, errors.New("端点地址不对")
	}
	return newS3Client(ep, destRegion(kind, d), strings.TrimSpace(d.AccessKey), strings.TrimSpace(d.SecretKey), strings.TrimSpace(d.Bucket)), nil
}

// remoteDestStatus 一个目的地的脱敏状态（给设置接口与设置页用）：
// SecretKey 绝不返回，只给 has_secret；AccessKey 是标识符，全文返回。
func (s *Server) remoteDestStatus(kind string) map[string]any {
	d := s.remoteDest(kind)
	return map[string]any{
		"enabled": d.Enabled, "bucket": d.Bucket, "account_id": d.AccountID,
		"region": destRegion(kind, *d), "endpoint": destEndpoint(kind, *d),
		"endpoint_override": d.Endpoint, "access_key": d.AccessKey,
		"has_secret": d.SecretKey != "", "last_status": d.LastStatus, "last_at": d.LastAt,
	}
}

// remoteBackupStatus 异地备份总状态（脱敏，进设置接口）。
func (s *Server) remoteBackupStatus() map[string]any {
	return map[string]any{
		"passphrase_set": s.cfg.RemoteBackup.PassphraseSet,
		"r2":             s.remoteDestStatus("r2"),
		"qiniu":          s.remoteDestStatus("qiniu"),
	}
}

// ---------- 接口 ----------

func validRemoteDestKind(kind string) bool { return kind == "r2" || kind == "qiniu" }

// handleRemoteBackupSettingsPost 保存异地备份的目的地设置（口令另外走
// passphrase 接口）。SecretKey 留空 = 保持原值；开关打开时逐项校验，
// 口令没设不许开。
func (s *Server) handleRemoteBackupSettingsPost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		R2 struct {
			Enabled   bool   `json:"enabled"`
			AccountID string `json:"account_id"`
			Bucket    string `json:"bucket"`
			AccessKey string `json:"access_key"`
			SecretKey string `json:"secret_key"`
			Endpoint  string `json:"endpoint"`
		} `json:"r2"`
		Qiniu struct {
			Enabled   bool   `json:"enabled"`
			Region    string `json:"region"`
			Endpoint  string `json:"endpoint"`
			Bucket    string `json:"bucket"`
			AccessKey string `json:"access_key"`
			SecretKey string `json:"secret_key"`
		} `json:"qiniu"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	// 先按当前值合并，再校验：只改一个目的地时另一个不丢。
	nr2 := s.cfg.RemoteBackup.R2
	nr2.Enabled = req.R2.Enabled
	nr2.AccountID = strings.TrimSpace(req.R2.AccountID)
	nr2.Bucket = strings.TrimSpace(req.R2.Bucket)
	nr2.AccessKey = strings.TrimSpace(req.R2.AccessKey)
	if strings.TrimSpace(req.R2.SecretKey) != "" {
		nr2.SecretKey = req.R2.SecretKey
	}
	nr2.Endpoint = strings.TrimSpace(req.R2.Endpoint)
	nqn := s.cfg.RemoteBackup.Qiniu
	nqn.Enabled = req.Qiniu.Enabled
	nqn.Region = strings.TrimSpace(req.Qiniu.Region)
	if nqn.Region == "" {
		nqn.Region = "cn-east-1"
	}
	nqn.Endpoint = strings.TrimSpace(req.Qiniu.Endpoint)
	nqn.Bucket = strings.TrimSpace(req.Qiniu.Bucket)
	nqn.AccessKey = strings.TrimSpace(req.Qiniu.AccessKey)
	if strings.TrimSpace(req.Qiniu.SecretKey) != "" {
		nqn.SecretKey = req.Qiniu.SecretKey
	}
	for _, tc := range []struct {
		kind string
		d    RemoteBackupDest
	}{
		{"r2", nr2}, {"qiniu", nqn},
	} {
		if !tc.d.Enabled {
			continue
		}
		if !s.cfg.RemoteBackup.PassphraseSet {
			writeJSON(w, 400, errResp("先设置异地备份口令，再开启"+destName(tc.kind)+"上传"))
			return
		}
		if tc.d.Bucket == "" {
			writeJSON(w, 400, errResp(destName(tc.kind)+"：桶名没填"))
			return
		}
		if tc.kind == "r2" && tc.d.AccountID == "" && tc.d.Endpoint == "" {
			writeJSON(w, 400, errResp("R2：账号 ID 没填（或直接填端点覆盖）"))
			return
		}
		if tc.kind == "qiniu" && !validQiniuRegion(tc.d.Region) {
			writeJSON(w, 400, errResp("七牛云：区域不对"))
			return
		}
		if tc.d.AccessKey == "" {
			writeJSON(w, 400, errResp(destName(tc.kind)+"：AccessKey 没填"))
			return
		}
		if tc.d.SecretKey == "" {
			writeJSON(w, 400, errResp(destName(tc.kind)+"：SecretKey 没填"))
			return
		}
		if tc.d.Endpoint != "" && !strings.HasPrefix(tc.d.Endpoint, "http://") && !strings.HasPrefix(tc.d.Endpoint, "https://") {
			writeJSON(w, 400, errResp(destName(tc.kind)+"：端点地址不对，要以 http:// 或 https:// 开头"))
			return
		}
	}
	s.cfg.RemoteBackup.R2 = nr2
	s.cfg.RemoteBackup.Qiniu = nqn
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "修改异地备份设置", "", "成功", fmt.Sprintf("R2 启用=%v 七牛云启用=%v", nr2.Enabled, nqn.Enabled))
	writeJSON(w, 200, okResp(map[string]any{"remote_backup": s.remoteBackupStatus()}))
}

// handleRemoteBackupPassphrasePost 设置/换异地备份口令。已设过时须先验旧口令；
// 换口令只换主密钥、盐 S 不变：旧备份仍用旧口令解，返回里写明警告。
// 口令本身任何地方都不存，只存派生出的主密钥与盐。
func (s *Server) handleRemoteBackupPassphrasePost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassphrase string `json:"old_passphrase"`
		NewPassphrase string `json:"new_passphrase"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	newPass := strings.TrimSpace(req.NewPassphrase)
	if len([]rune(newPass)) < remoteBackupMinPass {
		writeJSON(w, 400, errResp("口令至少 8 位"))
		return
	}
	rb := &s.cfg.RemoteBackup
	changed := rb.PassphraseSet
	if changed {
		if !s.verifyRemoteBackupPassphrase(req.OldPassphrase) {
			writeJSON(w, 400, errResp("旧口令不对"))
			return
		}
		salt, err := s.remoteBackupSalt()
		if err != nil {
			writeJSON(w, 500, errResp(err.Error()))
			return
		}
		rb.Key = base64.StdEncoding.EncodeToString(deriveRemoteBackupKey(newPass, salt))
	} else {
		salt := make([]byte, remoteBackupSaltLen)
		if _, err := rand.Read(salt); err != nil {
			writeJSON(w, 500, errResp("生成失败"))
			return
		}
		rb.Salt = base64.StdEncoding.EncodeToString(salt)
		rb.Key = base64.StdEncoding.EncodeToString(deriveRemoteBackupKey(newPass, salt))
		rb.PassphraseSet = true
	}
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	if changed {
		s.audit(s.currentUser(r), "更换异地备份口令", "", "成功", "旧备份仍用旧口令加密")
		writeJSON(w, 200, okResp(map[string]any{
			"remote_backup": s.remoteBackupStatus(),
			"warn":          "已换口令：之前传到异地的备份仍用旧口令加密，恢复它们时请输旧口令；新备份用新口令。",
		}))
		return
	}
	s.audit(s.currentUser(r), "设置异地备份口令", "", "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"remote_backup": s.remoteBackupStatus()}))
}

// handleRemoteBackupPassphraseClear 清除异地备份口令：主密钥与盐一起删，
// 两个目的地同时关闭。注意：口令本身不存本机，清除只影响新上传——
// 之前传上去的备份只要还记得口令，恢复时输口令照样解得开。
func (s *Server) handleRemoteBackupPassphraseClear(w http.ResponseWriter, r *http.Request) {
	rb := &s.cfg.RemoteBackup
	rb.Key = ""
	rb.Salt = ""
	rb.PassphraseSet = false
	rb.R2.Enabled = false
	rb.Qiniu.Enabled = false
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "清除异地备份口令", "", "成功", "两个目的地已关闭；旧备份仍可用旧口令恢复")
	writeJSON(w, 200, okResp(map[string]any{"remote_backup": s.remoteBackupStatus()}))
}

// handleRemoteBackupTest 测试上传：用存的主密钥加密一小段探针，上传、
// 下载回来、解密核对，全程走真实 S3；最后把探针对象删掉。哪一步失败
// 就报哪一步，不静默报成功。
func (s *Server) handleRemoteBackupTest(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("dest")
	if !validRemoteDestKind(kind) {
		writeJSON(w, 400, errResp("目的地不对"))
		return
	}
	d := *s.remoteDest(kind)
	if !d.Enabled {
		writeJSON(w, 400, errResp("先开启"+destName(kind)+"再测试"))
		return
	}
	key, err := s.remoteBackupMasterKey()
	if err != nil {
		writeJSON(w, 500, errResp(err.Error()))
		return
	}
	salt, err := s.remoteBackupSalt()
	if err != nil {
		writeJSON(w, 500, errResp(err.Error()))
		return
	}
	client, err := s.remoteS3Client(kind, d)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	probe := []byte(`{"remote_backup_test":true,"ts":"` + time.Now().UTC().Format(time.RFC3339) + `"}`)
	env, err := encryptRemoteBackup(key, salt, probe)
	if err != nil {
		writeJSON(w, 500, errResp("加密失败："+err.Error()))
		return
	}
	ctx := context.Background()
	testKey := remoteBackupTestKey + randomTokenHex(4) + ".enc"
	if err := client.PutObject(ctx, testKey, env); err != nil {
		writeJSON(w, 500, errResp("上传失败："+err.Error()))
		return
	}
	got, err := client.GetObject(ctx, testKey)
	if err != nil {
		_ = client.DeleteObject(ctx, testKey)
		writeJSON(w, 500, errResp("下载回来失败："+err.Error()))
		return
	}
	dec, err := decryptRemoteBackupWithKey(key, got)
	if err != nil {
		_ = client.DeleteObject(ctx, testKey)
		writeJSON(w, 500, errResp("解密核对失败："+err.Error()))
		return
	}
	_ = client.DeleteObject(ctx, testKey)
	if !bytes.Equal(dec, probe) {
		writeJSON(w, 500, errResp("解密核对失败：内容对不上"))
		return
	}
	s.audit(s.currentUser(r), "测试异地备份上传", destName(kind), "成功", "上传、下载、解密都对上了")
	writeJSON(w, 200, okResp(map[string]any{"detail": "测试通过：上传、下载、解密都对上了"}))
}

// handleRemoteBackupList 列出一个目的地的远程备份（只列 backup- 前缀，新的在前）。
func (s *Server) handleRemoteBackupList(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("dest")
	if !validRemoteDestKind(kind) {
		writeJSON(w, 400, errResp("目的地不对"))
		return
	}
	d := *s.remoteDest(kind)
	if !d.Enabled {
		writeJSON(w, 400, errResp(destName(kind)+"没开启"))
		return
	}
	client, err := s.remoteS3Client(kind, d)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	objs, err := client.ListObjects(context.Background(), "backup-")
	if err != nil {
		writeJSON(w, 500, errResp("列出失败："+err.Error()))
		return
	}
	out := make([]map[string]any, 0, len(objs))
	for _, o := range objs {
		out = append(out, map[string]any{"key": o.Key, "size": o.Size, "last_modified": o.LastModified})
	}
	writeJSON(w, 200, okResp(map[string]any{"dest": kind, "objects": out}))
}

// validRemoteBackupKey 远程备份键校验：只允许 backup- 开头、.json.enc 结尾、
// 不含斜杠与 ..（防路径穿越与乱下别的对象）。
func validRemoteBackupKey(key string) bool {
	return strings.HasPrefix(key, "backup-") && strings.HasSuffix(key, ".json.enc") &&
		!strings.ContainsAny(key, `/\`) && !strings.Contains(key, "..")
}

// handleRemoteBackupDownload 下载一份远程备份（原样下 .enc 文件，
// 解密在恢复时做，不在这里解）。
func (s *Server) handleRemoteBackupDownload(w http.ResponseWriter, r *http.Request) {
	kind, key := r.PathValue("dest"), r.PathValue("key")
	if !validRemoteDestKind(kind) || !validRemoteBackupKey(key) {
		writeJSON(w, 400, errResp("备份不对"))
		return
	}
	d := *s.remoteDest(kind)
	if !d.Enabled {
		writeJSON(w, 400, errResp(destName(kind)+"没开启"))
		return
	}
	client, err := s.remoteS3Client(kind, d)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	data, err := client.GetObject(context.Background(), key)
	if err != nil {
		writeJSON(w, 500, errResp("下载失败："+err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+key+`"`)
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

// handleRemoteBackupRestore 从远程恢复一份备份：下载→用户输口令现场解密→
// 走现有导入恢复流程。口令错了报“口令不对，解不开这个备份”。
func (s *Server) handleRemoteBackupRestore(w http.ResponseWriter, r *http.Request) {
	kind, key := r.PathValue("dest"), r.PathValue("key")
	if !validRemoteDestKind(kind) || !validRemoteBackupKey(key) {
		writeJSON(w, 400, errResp("备份不对"))
		return
	}
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if strings.TrimSpace(req.Passphrase) == "" {
		writeJSON(w, 400, errResp("请输口令"))
		return
	}
	d := *s.remoteDest(kind)
	if !d.Enabled {
		writeJSON(w, 400, errResp(destName(kind)+"没开启"))
		return
	}
	client, err := s.remoteS3Client(kind, d)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	data, err := client.GetObject(context.Background(), key)
	if err != nil {
		writeJSON(w, 500, errResp("下载失败："+err.Error()))
		return
	}
	pt, err := decryptRemoteBackup(req.Passphrase, data)
	if errors.Is(err, errRemoteBadPassphrase) {
		writeJSON(w, 400, errResp("口令不对，解不开这个备份"))
		return
	}
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	var imp importPayload
	if err := json.Unmarshal(pt, &imp); err != nil {
		writeJSON(w, 400, errResp("备份文件格式不对"))
		return
	}
	if ferr := s.validateImportPayload(imp); ferr != nil {
		writeJSON(w, ferr.status, errResp(ferr.msg))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	warn, ferr := s.applyImportPayload(imp, importApplyOpts{
		preBackupLabel: "恢复异地备份前自动备份", auditAction: "恢复异地备份",
		auditDetail:       destName(kind) + " " + key + "；密码哈希与 Telegram token 保持本机原值；订阅用户按 ID 保留现有令牌，不在备份里的用户会被移除，新用户需重新生成令牌",
		restartWarnPrefix: "配置已恢复，但重启 realm 失败：",
		verifyWarnPrefix:  "恢复后端口校验失败：", user: s.currentUser(r),
	})
	if ferr != nil {
		writeJSON(w, ferr.status, errResp(ferr.msg))
		return
	}
	if warn != "" {
		writeJSON(w, 200, errResp(warn))
		return
	}
	writeJSON(w, 200, okResp(nil))
}
