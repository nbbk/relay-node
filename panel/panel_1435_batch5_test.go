package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- 1.4.35：面板 UI/UX 重设计第 5 批（导航与密度统一） ----------
//
// 总览瘦身（只留 6 个落地）＋落地管理补搜索；证书两页改名互指；
// 链接页拆历史记录／生成 twin 两个选项卡；端口占用与端口管理互指；
// 全部列表统一搜索＋每页条数（10/20/50/100，localStorage 记住）＋分页；
// 审计统一成 20 条；用户表窄屏固定首／末列；分组改成与档位一致的表格；
// 流量图悬停数值＋X 轴 5 个点；落地证书去进度条只留天数徽章；
// 恢复 toast 改文案、重新下发加二次确认。前端行为由 jsdom
// test-1435-batch5.js 覆盖。

func TestVersion1435(t *testing.T) {
	if Version != "1.4.42" {
		t.Fatalf("Version 应为 1.4.35，实际 %q", Version)
	}
	vb, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("读 VERSION 失败：%v", err)
	}
	if strings.TrimSpace(string(vb)) != "1.4.42" {
		t.Fatalf("VERSION 文件应为 1.4.35，实际 %q", strings.TrimSpace(string(vb)))
	}
}

// TestAuditDefault20Batch5：审计默认每页 20 条（v1.4.35，P1-8）。
// 不传 page_size 与传非法值都按 20 条算；25 条记录共 2 页。
func TestAuditDefault20Batch5(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	cookie := loginTestCookie(t, h)
	writeAuditEntries(t, filepath.Join(s.env.PanelHome, "audit.log"), time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC), 25)

	get := func(q string) (int, map[string]any) {
		req := httptest.NewRequest("GET", "/api/v1/audit?"+q, nil)
		req.Header.Set("Cookie", cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}
	entries := func(body map[string]any) []any {
		es, _ := body["entries"].([]any)
		return es
	}
	// 不传 page_size：默认 20 条，共 2 页。
	if code, b := get("page=1"); code != 200 || b["page_size"] != float64(20) || len(entries(b)) != 20 || b["total_pages"] != float64(2) {
		t.Fatalf("默认：code=%d body=%v", code, b)
	}
	// 第 2 页剩 5 条。
	if code, b := get("page=2"); code != 200 || len(entries(b)) != 5 {
		t.Fatalf("第 2 页：code=%d entries=%d", code, len(entries(b)))
	}
	// 非法 page_size 按默认 20 条。
	if code, b := get("page=1&page_size=abc"); code != 200 || b["page_size"] != float64(20) || len(entries(b)) != 20 {
		t.Fatalf("page_size=abc：code=%d body=%v", code, b)
	}
	// 显式 10 条仍可用（前端别的列表可选 10，后端不锁死 20）。
	if code, b := get("page=1&page_size=10"); code != 200 || b["page_size"] != float64(10) || len(entries(b)) != 10 {
		t.Fatalf("page_size=10：code=%d body=%v", code, b)
	}
}

func TestBatch5Frontend1435(t *testing.T) {
	jsB, err := os.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("读 web/app.js 失败：%v", err)
	}
	htmlB, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("读 web/index.html 失败：%v", err)
	}
	cssB, err := os.ReadFile("web/style.css")
	if err != nil {
		t.Fatalf("读 web/style.css 失败：%v", err)
	}
	js, html, css := string(jsB), string(htmlB), string(cssB)

	// 证书改名互指（P1-7）。
	if !strings.Contains(js, "title: '落地证书'") {
		t.Fatalf("PAGES 里证书页标题应为“落地证书”")
	}
	if !strings.Contains(html, "<h1>落地证书</h1>") || !strings.Contains(html, "轻量节点后端的证书在「节点」页下方") {
		t.Fatalf("落地证书页应改名并互指节点页")
	}
	if !strings.Contains(html, "节点证书（轻量节点后端）") || !strings.Contains(html, "旧落地证书与续签引导在「落地证书」页") {
		t.Fatalf("节点证书标题应加“轻量节点后端”限定并互指落地证书页")
	}
	// 总览瘦身（P1-10）：只留 6 个＋查看全部。
	if !strings.Contains(js, "总览只留最近 6 个落地") || !strings.Contains(js, "filtered.slice(0, 6)") || !strings.Contains(js, "查看全部（") {
		t.Fatalf("总览应只留 6 个落地并带“查看全部”链接")
	}
	if !strings.Contains(html, `id="manage-search"`) {
		t.Fatalf("落地管理页应有与总览相同的搜索框 manage-search")
	}
	// 链接页双选项卡（P1-13）。
	if !strings.Contains(js, "LINKS_TABS = ['history', 'twin']") || !strings.Contains(js, "showLinksTab") {
		t.Fatalf("链接页应拆成历史记录／生成 twin 两个选项卡")
	}
	if !strings.Contains(html, `id="links-tab-history"`) || !strings.Contains(html, `id="links-panel-twin"`) {
		t.Fatalf("index.html 里没有链接页双选项卡容器")
	}
	// 每页条数统一（P1-8）。
	if !strings.Contains(js, "PAGE_SIZE_OPTIONS = [10, 20, 50, 100]") || !strings.Contains(js, "storedPageSize") {
		t.Fatalf("分页应支持 10/20/50/100 并记住选择")
	}
	if !strings.Contains(js, "auditPageSize = storedPageSize()") || !strings.Contains(js, "审计统一成 20 条") {
		t.Fatalf("审计前端每页应为 20 条")
	}
	if !strings.Contains(js, "remoteListPage") {
		t.Fatalf("远程备份列表应分页（remoteListPage）")
	}
	// 证书去进度条（P2-11）：不再按 90 天算比例。
	if strings.Contains(js, "days / 90 * 100") || strings.Contains(js, "剩余天数进度（按 90 天有效期）") {
		t.Fatalf("落地证书不应再有按 90 天算的进度条")
	}
	// 用户表固定列＋分组表格（P2-8）。
	if !strings.Contains(css, ".user-table-wrap") || !strings.Contains(css, "position:sticky") {
		t.Fatalf("style.css 里没有用户表固定列样式")
	}
	if !strings.Contains(js, "<th>后端节点数</th>") {
		t.Fatalf("分组列表应改成表格并有“后端节点数”列")
	}
	// 流量图悬停＋X 轴 5 个点（P2-9），catch 不再调 drawChart。
	if !strings.Contains(js, "traffic-chart-tip") || !strings.Contains(js, "onTrafficChartHover") || !strings.Contains(js, "trafficChartState") {
		t.Fatalf("流量图应有悬停数值条")
	}
	if !strings.Contains(js, "至少标 5 个时间点") {
		t.Fatalf("流量图 X 轴应标 5 个时间点")
	}
	// 恢复 toast 与重新下发二次确认（P2-12）。
	if !strings.Contains(js, "已恢复，去「落地管理」查看") {
		t.Fatalf("恢复 toast 应改成“已恢复，去「落地管理」查看”")
	}
	if !strings.Contains(js, "重新下发中转配置") || !strings.Contains(js, "已建立的中转连接会断开，须重连") {
		t.Fatalf("重新下发应有二次确认并讲清重连后果")
	}
	// 端口占用与端口管理互指。
	if !strings.Contains(html, `id="fw-goto-ports"`) || !strings.Contains(html, "去端口管理 →") {
		t.Fatalf("服务页与端口管理页应互指")
	}
}
