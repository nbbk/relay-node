package main

// ---------- 1.4.42：集中管理三子选项卡大标题整个去掉＋twin“节点名称”标签孤字 ----------
//
// 两件事：① 集中管理三个子选项卡（已登记的远程面板／登记远程面板／独立合并
// 订阅）的大标题 h2 整个去掉——用户 2026-11-28 19:48–19:49 逐个确认：名字
// 在页面上出现三遍（选项卡标签、大标题、内容标题）太多余，只留选项卡标签
// 和内容自己的标题（独立合并订阅列表的 h3“共 N 条”保留）；② 生成 twin 页
// “节点名称”标签换行后“名”字单独落第二行（纯观感）——“节点名称”四字包进
// white-space:nowrap 的 span，任何视口／字体下都拆不开。
//
// 追加（2026-11-28 19:51–19:56 逐页审计，管理面板＋完整面板 32 个子选项卡）：
// 另外 13 处重复标题合在同一版里一次去掉——静态 12 处（index.html：h2×11、
// h3×1）＋动态 1 处（app.js：远程面板卡片的 h3）。规则同上：只去掉纯粹重复
// 名字的大标题/小标题；选项卡标签、说明文字、内容自己的功能性标题全留着；
// 编号“①②③”与括号里的补充说明随 H2 一起去掉。

import (
	"os"
	"strings"
	"testing"
)

func TestVersion1442(t *testing.T) {
	if Version != "1.4.43" {
		t.Fatalf("Version 应为 1.4.42，实际 %q", Version)
	}
	vb, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("读 VERSION 失败：%v", err)
	}
	if strings.TrimSpace(string(vb)) != "1.4.43" {
		t.Fatalf("VERSION 文件应为 1.4.42，实际 %q", strings.TrimSpace(string(vb)))
	}
}

// TestCentralTitles1442：集中管理三个子选项卡（已登记的远程面板／登记远程
// 面板／独立合并订阅）的大标题 h2 整个去掉——用户 2026-11-28 19:48–19:49
// 逐个确认：页面上名字出现三遍（选项卡标签、大标题、内容标题）太多余，
// 只留选项卡标签和内容自己的标题。断言：集中管理 section 里一个 h2 都
// 没有；三个选项卡标签还在；三个面板本身还在；独立合并订阅列表的 h3
// （badge 是“共 N 条”，1.4.41 的修法）保持不动。
func TestCentralTitles1442(t *testing.T) {
	b, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("读 index.html 失败：%v", err)
	}
	src := string(b)
	if strings.Contains(src, `<h2>独立合并订阅 <span class="badge">独立合并订阅</span></h2>`) {
		t.Fatalf("重复大标题还在：h2 里 badge 文本与标题文本完全相同")
	}
	// 集中管理整个 section 里不应再有 h2。
	secStart := strings.Index(src, `id="page-central"`)
	if secStart < 0 {
		t.Fatalf("找不到 #page-central")
	}
	secRest := src[secStart:]
	secEnd := strings.Index(secRest, "<section ")
	if secEnd < 0 {
		t.Fatalf("#page-central 没有下一个 section，截取失败")
	}
	section := secRest[:secEnd]
	if strings.Contains(section, "<h2") {
		t.Fatalf("集中管理页里还有大标题 h2，应整个去掉")
	}
	for _, want := range []string{
		`id="central-panel-list"`,
		`id="central-panel-add"`,
		`id="central-panel-indmerged"`,
		`>已登记的远程面板</button>`,
		`>登记远程面板</button>`,
		`>独立合并订阅</button>`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("集中管理页缺了应保留的东西：%s", want)
		}
	}
	aj, err := os.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("读 app.js 失败：%v", err)
	}
	if !strings.Contains(string(aj), "共 ' + filtered.length + ' 条") {
		t.Fatalf("1.4.41 的列表标题条数 badge 被动过：app.js 里找不到“共 N 条”")
	}
}

// TestTwinNameLabel1442：生成 twin 页“节点名称”标签里的“节点名称”四字必须
// 包在 white-space:nowrap 的 span 里（且就在 twin-name 那个 label 内），
// 任何视口／字体下都不会拆成两行、不会剩一个“名”字单独落第二行。
func TestTwinNameLabel1442(t *testing.T) {
	b, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("读 index.html 失败：%v", err)
	}
	src := string(b)
	li := strings.Index(src, `for="twin-name"`)
	if li < 0 {
		t.Fatalf("找不到 twin-name 的 label")
	}
	le := strings.Index(src[li:], "</label>")
	if le < 0 {
		t.Fatalf("twin-name 的 label 没有闭合")
	}
	label := src[li : li+le]
	if !strings.Contains(label, `<span style="white-space:nowrap">节点名称</span>`) {
		t.Fatalf("twin-name label 里“节点名称”应包在 nowrap span 里，实际 %q", label)
	}
}

// TestAuditTitles1442：逐页审计找出的另外 13 处重复标题（静态 12 处＋动态
// 1 处）全部去掉，保留的东西一个不少。静态 12 处全在 index.html：
// 回收站／中转路由／节点证书／历史记录／实时日志／备份／七牛云 Kodo／
// 已开放的端口／套餐档位／分组／订阅用户／订阅设置；动态 1 处在 app.js：
// 集中管理每台远程面板卡片顶部的 h3（名字与子选项卡标签重复）。
// 保留：全部选项卡标签、说明文字、功能性标题（“自动定时备份”“远程备份”
// 卡片标题、订阅设置三张卡标题、入网新节点／节点列表、twin 表单标题、
// 手动续签六步引导、realm 服务／端口占用表、开放指定端口／范围／快捷操作、
// 口令／Cloudflare R2、节点证书互指说明），以及内容自己的列表 h3
// （订阅用户／分组／独立合并订阅“共 N 条”）与本机卡片 h3“本机”。
func TestAuditTitles1442(t *testing.T) {
	b, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("读 index.html 失败：%v", err)
	}
	src := string(b)
	gone := []string{
		`<h2>回收站 <span class="small">（删除后保留 7 天，可恢复）</span></h2>`,
		`<h2>中转路由</h2>`,
		`<h2>节点证书（轻量节点后端）</h2>`,
		`<h2>历史记录（nodes.txt）</h2>`,
		`<h2>实时日志 <span class="small">（最近 200 行，每 10 秒自动刷新）</span></h2>`,
		`<h2>备份</h2>`,
		`<h3 style="margin-top:16px">七牛云 Kodo</h3>`,
		`<h2>已开放的端口</h2>`,
		`<h2>① 套餐档位</h2>`,
		`<h2>② 分组（权限组）</h2>`,
		`<h2>③ 订阅用户（一人一条）</h2>`,
		`<h2>订阅设置</h2>`,
	}
	for _, g := range gone {
		if strings.Contains(src, g) {
			t.Fatalf("审计清单里的重复标题还在：%s", g)
		}
	}
	kept := []string{
		`>回收站</button>`, `>中转路由</button>`, `>节点证书</button>`,
		`>历史记录</button>`, `>实时日志</button>`, `>备份</button>`,
		`>七牛云</button>`, `>已开放的端口</button>`, `>套餐档位</button>`,
		`>分组</button>`, `>订阅用户</button>`, `>订阅设置</button>`,
		`<h2 style="margin-top:20px">自动定时备份</h2>`, `<h2>远程备份（异地备份）</h2>`,
		`<h2>订阅对外域名</h2>`, `<h2>订阅路径</h2>`, `<h2>订阅网关</h2>`,
		`<h2>入网新节点</h2>`, `<h2 style="margin-top:20px">节点列表</h2>`,
		`<h2>粘贴原链接，生成节点链接</h2>`, `<h2>手动续签六步引导</h2>`,
		`<h2>realm 服务</h2>`, `<h2>端口占用表</h2>`,
		`<h2>开放指定端口</h2>`, `<h2>开放端口范围</h2>`, `<h2>快捷操作</h2>`,
		`<h3 style="margin-top:12px">口令</h3>`, `<h3 style="margin-top:16px">Cloudflare R2</h3>`,
		`旧落地证书与续签引导在「落地证书」页`,
	}
	for _, k := range kept {
		if !strings.Contains(src, k) {
			t.Fatalf("去标题时误删了应保留的东西：%s", k)
		}
	}
	aj, err := os.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("读 app.js 失败：%v", err)
	}
	ajs := string(aj)
	if strings.Contains(ajs, `'<h3>' + esc(rm.name || rm.base_url) + '</h3>'`) {
		t.Fatalf("远程面板卡片顶部的名字 h3 还在：应去掉，只留子选项卡标签")
	}
	for _, k := range []string{
		`centralOnlineBadge(rm)`,
		`远程面板</span>`,
		`<h3>本机</h3>`,
		`<h3>订阅用户</h3>`,
		`<h3>分组</h3>`,
		`<h3>独立合并订阅 <span class="badge">共 `,
	} {
		if !strings.Contains(ajs, k) {
			t.Fatalf("去标题时误删了 app.js 里应保留的东西：%s", k)
		}
	}
}
