package main

// v1.3.11 异地备份（七牛云 Kodo + Cloudflare R2，口令加密）的测试：
// 信封往返、口令错、密文被改、换口令、S3 客户端对本地假 S3 服务端验 SigV4
// （测试侧独立重算签名）、上传＋远程留 7 份裁剪、上传失败不影响本地备份、
// 口令未设不许开上传、恢复流程（对码 200、错码 400“口令不对”）。

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- 假 S3 服务端 ----------

// fakeS3141 内存假 S3：PUT/GET/DELETE/ListObjectsV2，收到请求先独立验 SigV4
// （测试侧按规范重算签名，不复用生产代码），签名不对直接 403；
// failNext 置 true 时下一次请求返回 403 AccessDenied（验错误解析）。
type fakeS3141 struct {
	mu       sync.Mutex
	objects  map[string][]byte
	badSig   int // 签名校验失败次数
	failNext bool
	secret   string
}

func newFakeS3141(secret string) *fakeS3141 {
	return &fakeS3141{objects: map[string][]byte{}, secret: secret}
}

func (f *fakeS3141) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if !f.verifySigV4(r, body) {
		f.mu.Lock()
		f.badSig++
		f.mu.Unlock()
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>SignatureDoesNotMatch</Code><Message>signature mismatch</Message></Error>`))
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>denied</Message></Error>`))
		return
	}
	key := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	// path-style：/{bucket}/{key}；列表请求 key 为空。
	parts := strings.SplitN(key, "/", 2)
	bucket, objKey := "", ""
	if len(parts) == 2 {
		bucket, objKey = parts[0], parts[1]
	} else if len(parts) == 1 {
		bucket = parts[0]
	}
	_ = bucket
	switch r.Method {
	case http.MethodPut:
		f.objects[objKey] = append([]byte(nil), body...)
		w.WriteHeader(200)
	case http.MethodGet:
		if r.URL.Query().Get("list-type") == "2" {
			prefix := r.URL.Query().Get("prefix")
			var keys []string
			for k := range f.objects {
				if strings.HasPrefix(k, prefix) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			type contents struct {
				Key          string `xml:"Key"`
				LastModified string `xml:"LastModified"`
				Size         int64  `xml:"Size"`
			}
			var res struct {
				XMLName  xml.Name   `xml:"ListBucketResult"`
				Contents []contents `xml:"Contents"`
			}
			for _, k := range keys {
				res.Contents = append(res.Contents, contents{Key: k, LastModified: "2026-11-19T03:00:00Z", Size: int64(len(f.objects[k]))})
			}
			w.Header().Set("Content-Type", "application/xml")
			_ = xml.NewEncoder(w).Encode(res)
			return
		}
		b, ok := f.objects[objKey]
		if !ok {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>not found</Message></Error>`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write(b)
	case http.MethodDelete:
		delete(f.objects, objKey)
		w.WriteHeader(204)
	default:
		w.WriteHeader(405)
	}
}

// verifySigV4 测试侧独立重算 SigV4 签名（按 AWS 规范从收到的请求现算，
// 不调用生产代码的 signV4）：规范头、规范查询串、签名链都重算一遍。
func (f *fakeS3141) verifySigV4(r *http.Request, body []byte) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		return false
	}
	rest := strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 ")
	parts := map[string]string{}
	for _, kv := range strings.Split(rest, ", ") {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			parts[k] = v
		}
	}
	cred, sh, sig := parts["Credential"], parts["SignedHeaders"], parts["Signature"]
	if cred == "" || sh == "" || sig == "" {
		return false
	}
	credParts := strings.Split(cred, "/")
	if len(credParts) != 5 || credParts[3] != "s3" || credParts[4] != "aws4_request" {
		return false
	}
	dateStamp, region := credParts[1], credParts[2]
	amzDate := r.Header.Get("x-amz-date")
	payloadHash := r.Header.Get("x-amz-content-sha256")
	sum := sha256.Sum256(body)
	if payloadHash != hex.EncodeToString(sum[:]) {
		return false
	}
	// 规范查询串（排序、RFC3986 编码）
	q := r.URL.Query()
	var qKeys []string
	for k := range q {
		qKeys = append(qKeys, k)
	}
	sort.Strings(qKeys)
	var qp []string
	for _, k := range qKeys {
		for _, v := range q[k] {
			qp = append(qp, rfc3986Escape(k)+"="+rfc3986Escape(v))
		}
	}
	canonicalQuery := strings.Join(qp, "&")
	// 规范头（按 SignedHeaders 的顺序取值）
	var ch strings.Builder
	for _, h := range strings.Split(sh, ";") {
		var v string
		if h == "host" {
			v = r.Host
		} else {
			v = strings.TrimSpace(r.Header.Get(h))
		}
		ch.WriteString(h + ":" + v + "\n")
	}
	canonicalURI := r.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalRequest := strings.Join([]string{r.Method, canonicalURI, canonicalQuery, ch.String(), sh, payloadHash}, "\n")
	scope := dateStamp + "/" + region + "/s3/aws4_request"
	crSum := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(crSum[:])
	hm := func(key, data []byte) []byte {
		h := hmac.New(sha256.New, key)
		h.Write(data)
		return h.Sum(nil)
	}
	kDate := hm([]byte("AWS4"+f.secret), []byte(dateStamp))
	kRegion := hm(kDate, []byte(region))
	kService := hm(kRegion, []byte("s3"))
	kSigning := hm(kService, []byte("aws4_request"))
	want := hex.EncodeToString(hm(kSigning, []byte(stringToSign)))
	return hmac.Equal([]byte(want), []byte(sig))
}

// ---------- 小工具 ----------

// setRemotePassphrase141 给测试面板设口令（走真实 handler，验一遍接口）。
func setRemotePassphrase141(t *testing.T, s *Server, pass string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/settings/remote-backup/passphrase",
		strings.NewReader(`{"old_passphrase":"","new_passphrase":"`+pass+`"}`))
	rec := httptest.NewRecorder()
	s.handleRemoteBackupPassphrasePost(rec, req)
	if rec.Code != 200 {
		t.Fatalf("设口令应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// enableFakeR2141 给测试面板配一个指向假 S3 的 R2 目的地并开启。
func enableFakeR2141(t *testing.T, s *Server, endpoint string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"r2": map[string]any{
		"enabled": true, "account_id": "test-account", "bucket": "test-bucket",
		"access_key": "test-ak", "secret_key": "test-secret", "endpoint": endpoint,
	}, "qiniu": map[string]any{"enabled": false}})
	req := httptest.NewRequest("POST", "/api/v1/settings/remote-backup", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleRemoteBackupSettingsPost(rec, req)
	if rec.Code != 200 {
		t.Fatalf("开启 R2 应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// ---------- 信封与口令 ----------

func TestRemoteBackupEnvelope141(t *testing.T) {
	salt := []byte("0123456789abcdef")
	key := deriveRemoteBackupKey("correct-horse-battery", salt)
	plain := []byte(`{"version":"1.3.11","relay_conf":"LANDING=203.0.113.1"}`)
	env, err := encryptRemoteBackup(key, salt, plain)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	// 信封头：魔数 + 版本 + 盐
	if !bytes.Equal(env[:4], []byte("RPBK")) || env[4] != 0x01 {
		t.Fatalf("信封头不对：%x", env[:5])
	}
	if !bytes.Equal(env[5:21], salt) {
		t.Fatalf("信封里的盐应与传入的盐一致")
	}
	// 对口令解得开
	pt, err := decryptRemoteBackup("correct-horse-battery", env)
	if err != nil || !bytes.Equal(pt, plain) {
		t.Fatalf("对口令应解开，实际 err=%v", err)
	}
	// 口令错 → 明确的“口令不对”
	if _, err := decryptRemoteBackup("wrong-passphrase", env); !errors.Is(err, errRemoteBadPassphrase) {
		t.Fatalf("错口令应报口令不对，实际：%v", err)
	}
	// 密文被改 → 解不开
	tampered := append([]byte(nil), env...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := decryptRemoteBackup("correct-horse-battery", tampered); err == nil {
		t.Fatalf("密文被改后应解不开")
	}
	// 魔数错 / 版本错 / 截断 → 格式错（不是口令错）
	badMagic := append([]byte(nil), env...)
	badMagic[0] = 'X'
	if _, err := decryptRemoteBackup("correct-horse-battery", badMagic); !errors.Is(err, errRemoteBadEnvelope) {
		t.Fatalf("魔数错应报格式不对，实际：%v", err)
	}
	badVer := append([]byte(nil), env...)
	badVer[4] = 0x09
	if _, err := decryptRemoteBackup("correct-horse-battery", badVer); err == nil || strings.Contains(err.Error(), "口令") {
		t.Fatalf("版本错应报版本不支持，实际：%v", err)
	}
	if _, err := decryptRemoteBackup("correct-horse-battery", env[:10]); !errors.Is(err, errRemoteBadEnvelope) {
		t.Fatalf("截断应报格式不对，实际：%v", err)
	}
	// 两次加密 nonce 不同，密文不同（同一明文也不重）
	env2, _ := encryptRemoteBackup(key, salt, plain)
	if bytes.Equal(env, env2) {
		t.Fatalf("两次加密的信封应不同（随机 nonce）")
	}
}

func TestRemoteBackupPassphraseChange141(t *testing.T) {
	s := newTestSubServer(t)
	setRemotePassphrase141(t, s, "old-passphrase-1")
	if !s.cfg.RemoteBackup.PassphraseSet {
		t.Fatalf("设完口令 PassphraseSet 应为 true")
	}
	if s.cfg.RemoteBackup.Key == "" || s.cfg.RemoteBackup.Salt == "" {
		t.Fatalf("应存派生主密钥与盐")
	}
	// 口令本身没存：config.json 里不应出现口令明文
	raw, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "config.json"))
	if strings.Contains(string(raw), "old-passphrase-1") {
		t.Fatalf("config.json 里不应出现口令明文")
	}
	if !s.verifyRemoteBackupPassphrase("old-passphrase-1") || s.verifyRemoteBackupPassphrase("nope") {
		t.Fatalf("旧口令校验应对、错口令不对")
	}
	// 旧口令加密一份
	key, _ := s.remoteBackupMasterKey()
	salt, _ := s.remoteBackupSalt()
	oldEnv, _ := encryptRemoteBackup(key, salt, []byte("old-backup"))
	// 换口令：旧口令不对 → 400；旧口令对 → 200 且带警告
	req := httptest.NewRequest("POST", "/api/v1/settings/remote-backup/passphrase",
		strings.NewReader(`{"old_passphrase":"nope","new_passphrase":"new-passphrase-2"}`))
	rec := httptest.NewRecorder()
	s.handleRemoteBackupPassphrasePost(rec, req)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "旧口令不对") {
		t.Fatalf("旧口令错应 400 旧口令不对，实际 %d：%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest("POST", "/api/v1/settings/remote-backup/passphrase",
		strings.NewReader(`{"old_passphrase":"old-passphrase-1","new_passphrase":"new-passphrase-2"}`))
	rec = httptest.NewRecorder()
	s.handleRemoteBackupPassphrasePost(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "旧口令") {
		t.Fatalf("换口令应 200 且带旧口令警告，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 旧备份仍用旧口令解得开，新口令解不开它；新备份用新口令
	if pt, err := decryptRemoteBackup("old-passphrase-1", oldEnv); err != nil || string(pt) != "old-backup" {
		t.Fatalf("旧备份应用旧口令解开，实际 err=%v", err)
	}
	if _, err := decryptRemoteBackup("new-passphrase-2", oldEnv); !errors.Is(err, errRemoteBadPassphrase) {
		t.Fatalf("旧备份用新口令应解不开，实际：%v", err)
	}
	key2, _ := s.remoteBackupMasterKey()
	if bytes.Equal(key, key2) {
		t.Fatalf("换口令后主密钥应变")
	}
	// 口令太短 → 400
	req = httptest.NewRequest("POST", "/api/v1/settings/remote-backup/passphrase",
		strings.NewReader(`{"old_passphrase":"","new_passphrase":"short"}`))
	rec = httptest.NewRecorder()
	s.handleRemoteBackupPassphrasePost(rec, req)
	if rec.Code != 400 {
		t.Fatalf("口令太短应 400，实际 %d", rec.Code)
	}
	// 清除口令：主密钥与盐清掉，开关关闭
	req = httptest.NewRequest("POST", "/api/v1/settings/remote-backup/passphrase/clear", nil)
	rec = httptest.NewRecorder()
	s.handleRemoteBackupPassphraseClear(rec, req)
	if rec.Code != 200 || s.cfg.RemoteBackup.PassphraseSet || s.cfg.RemoteBackup.Key != "" {
		t.Fatalf("清除后口令应清掉，实际 %d", rec.Code)
	}
}

// ---------- S3 客户端 ----------

func TestS3ClientFake141(t *testing.T) {
	fake := newFakeS3141("test-secret")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := newS3Client(srv.URL, "test-region", "test-ak", "test-secret", "test-bucket")
	ctx := context.Background()
	// PUT/GET/DELETE 往返
	if err := c.PutObject(ctx, "backup-20261119-030000.json.enc", []byte("encrypted-bytes")); err != nil {
		t.Fatalf("PUT 失败：%v", err)
	}
	got, err := c.GetObject(ctx, "backup-20261119-030000.json.enc")
	if err != nil || string(got) != "encrypted-bytes" {
		t.Fatalf("GET 应拿回原文，实际 %q err=%v", got, err)
	}
	if _, err := c.GetObject(ctx, "backup-missing.json.enc"); err == nil || !strings.Contains(err.Error(), "NoSuchKey") {
		t.Fatalf("取不存在的键应报 NoSuchKey，实际：%v", err)
	}
	// ListObjectsV2：前缀过滤 + 新的在前
	_ = c.PutObject(ctx, "backup-20261118-030000.json.enc", []byte("old"))
	_ = c.PutObject(ctx, "other-file.txt", []byte("x"))
	objs, err := c.ListObjects(ctx, "backup-")
	if err != nil {
		t.Fatalf("列表失败：%v", err)
	}
	if len(objs) != 2 || objs[0].Key != "backup-20261119-030000.json.enc" || objs[1].Key != "backup-20261118-030000.json.enc" {
		t.Fatalf("列表应只含 backup- 前缀、新的在前，实际 %+v", objs)
	}
	if err := c.DeleteObject(ctx, "backup-20261118-030000.json.enc"); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	objs, _ = c.ListObjects(ctx, "backup-")
	if len(objs) != 1 {
		t.Fatalf("删完应剩 1 个，实际 %+v", objs)
	}
	if fake.badSig != 0 {
		t.Fatalf("假服务端验 SigV4 失败 %d 次（生产签名与测试侧独立重算不一致）", fake.badSig)
	}
	// 错误体解析：AccessDenied
	fake.mu.Lock()
	fake.failNext = true
	fake.mu.Unlock()
	if err := c.PutObject(ctx, "backup-x.json.enc", []byte("x")); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("403 应解析出 AccessDenied，实际：%v", err)
	}
	// 签名错的客户端应被假服务端 403（证明假服务端真在验签名）
	bad := newS3Client(srv.URL, "test-region", "test-ak", "wrong-secret", "test-bucket")
	if err := bad.PutObject(ctx, "backup-y.json.enc", []byte("x")); err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("密钥错应报 SignatureDoesNotMatch，实际：%v", err)
	}
}

// ---------- 设置校验与脱敏 ----------

func TestRemoteBackupSettingsValidation141(t *testing.T) {
	s := newTestSubServer(t)
	enable := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"r2": map[string]any{
			"enabled": true, "account_id": "acc", "bucket": "bkt",
			"access_key": "ak", "secret_key": "sk",
		}, "qiniu": map[string]any{"enabled": false}})
		req := httptest.NewRequest("POST", "/api/v1/settings/remote-backup", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleRemoteBackupSettingsPost(rec, req)
		return rec
	}
	// 口令没设 → 不许开
	if rec := enable(); rec.Code != 400 || !strings.Contains(rec.Body.String(), "口令") {
		t.Fatalf("口令没设时开上传应 400 且提口令，实际 %d：%s", rec.Code, rec.Body.String())
	}
	setRemotePassphrase141(t, s, "test-passphrase-1")
	// 桶名没填 → 400
	body, _ := json.Marshal(map[string]any{"r2": map[string]any{
		"enabled": true, "account_id": "acc", "bucket": "",
		"access_key": "ak", "secret_key": "sk",
	}, "qiniu": map[string]any{"enabled": false}})
	req := httptest.NewRequest("POST", "/api/v1/settings/remote-backup", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleRemoteBackupSettingsPost(rec, req)
	if rec.Code != 400 {
		t.Fatalf("桶名没填应 400，实际 %d", rec.Code)
	}
	// 七牛区域不对 → 400
	body, _ = json.Marshal(map[string]any{"r2": map[string]any{"enabled": false}, "qiniu": map[string]any{
		"enabled": true, "region": "no-such-region", "bucket": "bkt",
		"access_key": "ak", "secret_key": "sk",
	}})
	req = httptest.NewRequest("POST", "/api/v1/settings/remote-backup", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	s.handleRemoteBackupSettingsPost(rec, req)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "区域") {
		t.Fatalf("七牛区域不对应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 正常开 → 200；SecretKey 留空保持原值
	if rec := enable(); rec.Code != 200 {
		t.Fatalf("正常开应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	body, _ = json.Marshal(map[string]any{"r2": map[string]any{
		"enabled": true, "account_id": "acc", "bucket": "bkt",
		"access_key": "ak", "secret_key": "",
	}, "qiniu": map[string]any{"enabled": false}})
	req = httptest.NewRequest("POST", "/api/v1/settings/remote-backup", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	s.handleRemoteBackupSettingsPost(rec, req)
	if rec.Code != 200 || s.cfg.RemoteBackup.R2.SecretKey != "sk" {
		t.Fatalf("SecretKey 留空应保持原值，实际 %d secret=%q", rec.Code, s.cfg.RemoteBackup.R2.SecretKey)
	}
	// 设置接口脱敏：不含 secret_key、主密钥与口令
	st, _ := json.Marshal(s.remoteBackupStatus())
	if strings.Contains(string(st), "secret_key") || strings.Contains(string(st), "test-passphrase-1") {
		t.Fatalf("设置接口不应返回 SecretKey 与口令，实际：%s", st)
	}
	if strings.Contains(string(st), s.cfg.RemoteBackup.Key) {
		t.Fatalf("设置接口不应返回主密钥")
	}
}

// ---------- 上传与裁剪 ----------

func TestRemoteUploadPrune141(t *testing.T) {
	origDelay := remoteBackupRetryDelay
	remoteBackupRetryDelay = func(int) time.Duration { return 0 }
	defer func() { remoteBackupRetryDelay = origDelay }()
	fake := newFakeS3141("test-secret")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := newTestSubServer(t)
	setRemotePassphrase141(t, s, "prune-passphrase-1")
	enableFakeR2141(t, s, srv.URL)
	// 写一份合法 toml（备份里要带能通过校验的 config.toml）
	validToml := "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte(validToml), 0o644); err != nil {
		t.Fatal(err)
	}
	// 传 9 份（id 按秒递增，同一秒自动加 -2/-3 后缀，不会撞）
	var ids []string
	for i := 0; i < 9; i++ {
		id, err := s.runScheduledBackup("裁剪测试")
		if err != nil {
			t.Fatalf("备份失败：%v", err)
		}
		ids = append(ids, id)
		s.uploadBackupToRemote(id)
	}
	if fake.badSig != 0 {
		t.Fatalf("假服务端验 SigV4 失败 %d 次", fake.badSig)
	}
	c := newS3Client(srv.URL, "auto", "test-ak", "test-secret", "test-bucket")
	objs, err := c.ListObjects(context.Background(), "backup-")
	if err != nil {
		t.Fatalf("列表失败：%v", err)
	}
	if len(objs) != remoteBackupKeep {
		t.Fatalf("远程应只留 %d 份，实际 %d：%+v", remoteBackupKeep, len(objs), objs)
	}
	// 留的是最新的 7 份（第一份与第二份已被裁掉）
	for _, o := range objs {
		if o.Key == remoteObjectKey(ids[0]) || o.Key == remoteObjectKey(ids[1]) {
			t.Fatalf("最旧的两份应被裁掉，实际还在：%s", o.Key)
		}
	}
	// 远程对象真是加密包：用口令解开后与本地备份一致
	got, err := c.GetObject(context.Background(), remoteObjectKey(ids[8]))
	if err != nil {
		t.Fatalf("下载最新远程备份失败：%v", err)
	}
	pt, err := decryptRemoteBackup("prune-passphrase-1", got)
	if err != nil {
		t.Fatalf("远程备份应用口令解开，实际：%v", err)
	}
	local, _ := s.readScheduledBackup(ids[8])
	if !bytes.Equal(pt, local) {
		t.Fatalf("解密后的远程备份应与本地备份一致")
	}
	// 口令错解不开远程备份
	if _, err := decryptRemoteBackup("wrong-pass", got); !errors.Is(err, errRemoteBadPassphrase) {
		t.Fatalf("远程备份用错口令应报口令不对，实际：%v", err)
	}
}

func TestRemoteUploadFailureKeepsLocal141(t *testing.T) {
	origDelay := remoteBackupRetryDelay
	remoteBackupRetryDelay = func(int) time.Duration { return 0 }
	defer func() { remoteBackupRetryDelay = origDelay }()
	s := newTestSubServer(t)
	setRemotePassphrase141(t, s, "fail-passphrase-1")
	// 目的地指向一个打不开的端口（连接拒绝，不重试也快）
	body, _ := json.Marshal(map[string]any{"r2": map[string]any{
		"enabled": true, "account_id": "acc", "bucket": "bkt",
		"access_key": "ak", "secret_key": "sk", "endpoint": "http://127.0.0.1:1",
	}, "qiniu": map[string]any{"enabled": false}})
	req := httptest.NewRequest("POST", "/api/v1/settings/remote-backup", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleRemoteBackupSettingsPost(rec, req)
	if rec.Code != 200 {
		t.Fatalf("开上传应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	id, err := s.runScheduledBackup("失败测试")
	if err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	// 上传失败不应抛 panic、不应删本地备份
	s.uploadBackupToRemote(id)
	if _, err := os.Stat(filepath.Join(s.env.PanelHome, "scheduled-backups", id+".json")); err != nil {
		t.Fatalf("上传失败后本地备份应还在：%v", err)
	}
	if !strings.Contains(s.cfg.RemoteBackup.R2.LastStatus, "失败") {
		t.Fatalf("上次状态应记失败，实际：%s", s.cfg.RemoteBackup.R2.LastStatus)
	}
}

// ---------- 测试上传与恢复 ----------

func TestRemoteBackupTestHandler141(t *testing.T) {
	fake := newFakeS3141("test-secret")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := newTestSubServer(t)
	setRemotePassphrase141(t, s, "test-handler-pass-1")
	enableFakeR2141(t, s, srv.URL)
	req := httptest.NewRequest("POST", "/api/v1/remote-backup/test/r2", nil)
	req.SetPathValue("dest", "r2")
	rec := httptest.NewRecorder()
	s.handleRemoteBackupTest(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "测试通过") {
		t.Fatalf("测试上传应 200 测试通过，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 探针对象已删干净（假服务端里不留测试对象）
	fake.mu.Lock()
	left := len(fake.objects)
	fake.mu.Unlock()
	if left != 0 {
		t.Fatalf("测试上传后探针对象应删掉，假服务端还剩 %d 个对象", left)
	}
	if fake.badSig != 0 {
		t.Fatalf("假服务端验 SigV4 失败 %d 次", fake.badSig)
	}
	// 目的地没开 → 400
	s.cfg.RemoteBackup.R2.Enabled = false
	req = httptest.NewRequest("POST", "/api/v1/remote-backup/test/r2", nil)
	req.SetPathValue("dest", "r2")
	rec = httptest.NewRecorder()
	s.handleRemoteBackupTest(rec, req)
	if rec.Code != 400 {
		t.Fatalf("目的地没开时测试应 400，实际 %d", rec.Code)
	}
}

func TestRemoteBackupRestore141(t *testing.T) {
	fake := newFakeS3141("test-secret")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := newTestSubServer(t)
	validToml := "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte(validToml), 0o644); err != nil {
		t.Fatal(err)
	}
	s.cfg.SubUsers = []SubUser{{ID: "u1", Name: "张三", TokenHash: "hash-aaa", CreatedAt: "2026-01-01T00:00:00Z"}}
	setRemotePassphrase141(t, s, "restore-passphrase-1")
	enableFakeR2141(t, s, srv.URL)
	id, err := s.runScheduledBackup("恢复测试")
	if err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	s.uploadBackupToRemote(id)
	// 改掉现状：u1 删了
	s.cfg.SubUsers = nil
	restore := func(pass string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/remote-backup/restore/r2/"+remoteObjectKey(id),
			strings.NewReader(`{"passphrase":"`+pass+`"}`))
		req.SetPathValue("dest", "r2")
		req.SetPathValue("key", remoteObjectKey(id))
		rec := httptest.NewRecorder()
		s.handleRemoteBackupRestore(rec, req)
		return rec
	}
	// 口令错 → 400“口令不对”，且不恢复
	if rec := restore("wrong-pass"); rec.Code != 400 || !strings.Contains(rec.Body.String(), "口令不对") {
		t.Fatalf("错口令应 400 口令不对，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if len(s.cfg.SubUsers) != 0 {
		t.Fatalf("错口令恢复后订阅用户不应变")
	}
	// 口令对 → 200（测试环境 realm 起不来，走“配置已恢复，但重启失败”分支，仍是 200）
	if rec := restore("restore-passphrase-1"); rec.Code != 200 {
		t.Fatalf("对口令恢复应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// v1.3.29：设了迁移口令时备份里含令牌哈希，口令对就随备份恢复回来，
	// 旧订阅链接继续能用（未设口令时备份里不含令牌，恢复后哈希为空、须轮换，
	// 见 panel_1329_test.go 的无口令一档）。
	if len(s.cfg.SubUsers) != 1 || s.cfg.SubUsers[0].ID != "u1" || s.cfg.SubUsers[0].TokenHash != "hash-aaa" {
		t.Fatalf("u1 应恢复回来且令牌哈希随备份回来，实际 %+v", s.cfg.SubUsers)
	}
	// 键校验：乱下的键 → 400
	req := httptest.NewRequest("POST", "/api/v1/remote-backup/restore/r2/..%2Fconfig.json",
		strings.NewReader(`{"passphrase":"restore-passphrase-1"}`))
	req.SetPathValue("dest", "r2")
	req.SetPathValue("key", "../config.json")
	rec := httptest.NewRecorder()
	s.handleRemoteBackupRestore(rec, req)
	if rec.Code != 400 {
		t.Fatalf("非法键应 400，实际 %d", rec.Code)
	}
}

func TestRemoteBackupListDownload141(t *testing.T) {
	fake := newFakeS3141("test-secret")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := newTestSubServer(t)
	setRemotePassphrase141(t, s, "list-passphrase-1")
	enableFakeR2141(t, s, srv.URL)
	// 列表空着 → 200 空列表
	req := httptest.NewRequest("GET", "/api/v1/remote-backup/list/r2", nil)
	req.SetPathValue("dest", "r2")
	rec := httptest.NewRecorder()
	s.handleRemoteBackupList(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"objects":[]`) {
		t.Fatalf("空列表应 200 空数组，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 传一份后列表里有它，下载拿回的是同一个加密包
	id, _ := s.runScheduledBackup("列表测试")
	s.uploadBackupToRemote(id)
	req = httptest.NewRequest("GET", "/api/v1/remote-backup/list/r2", nil)
	req.SetPathValue("dest", "r2")
	rec = httptest.NewRecorder()
	s.handleRemoteBackupList(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), remoteObjectKey(id)) {
		t.Fatalf("列表应含刚传的备份，实际 %d：%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest("GET", "/api/v1/remote-backup/download/r2/"+remoteObjectKey(id), nil)
	req.SetPathValue("dest", "r2")
	req.SetPathValue("key", remoteObjectKey(id))
	rec = httptest.NewRecorder()
	s.handleRemoteBackupDownload(rec, req)
	if rec.Code != 200 {
		t.Fatalf("下载应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if _, err := decryptRemoteBackup("list-passphrase-1", rec.Body.Bytes()); err != nil {
		t.Fatalf("下载的应是能用口令解开的加密包，实际：%v", err)
	}
}

// TestScheduledBackupIDNoReuse141 同一秒多次备份的 id 不能复用：
// 旧逻辑是 plain 被轮转删掉后第 9 次又用 plain，新备份顶掉旧 id，
// 远程裁剪按 id 会把它当最旧的删掉（最新备份反而被裁）。修后后缀取现存最大 +1。
func TestScheduledBackupIDNoReuse141(t *testing.T) {
	s := newTestSubServer(t)
	seen := map[string]bool{}
	for i := 0; i < 12; i++ {
		id, err := s.runScheduledBackup("id 测试")
		if err != nil {
			t.Fatalf("备份失败：%v", err)
		}
		if seen[id] {
			t.Fatalf("第 %d 次备份复用了 id %s", i, id)
		}
		seen[id] = true
	}
	if len(s.listScheduledBackups()) != 7 {
		t.Fatalf("本地应只留 7 份，实际 %d", len(s.listScheduledBackups()))
	}
}
