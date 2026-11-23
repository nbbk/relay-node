package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------- 删落地不丢 CERT_MODE / MULTIPLIER（v1.3.47） ----------

// TestRebuildDeleteKeepsCertModeMultiplier1347 三落地各带不同的 CERT_MODE 与
// MULTIPLIER，删中间一个（再删第一个），剩下的落地值原样保留、编号重排后
// 值跟着落地走，且旧行不残留（残留的旧后缀行会错配到别的落地）。
func TestRebuildDeleteKeepsCertModeMultiplier1347(t *testing.T) {
	conf := "LANDING=203.0.113.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"\"\nCERT_DOMAIN=\"a.example.com\"\nCERT_MODE=\"dns\"\nMULTIPLIER=1.5\nPRESET=3\nMODE_NOTE=\"标准\"\nPORTS=\"tcp:443:443\"\n" +
		"LANDING_2=203.0.113.2\nLANDING_IS_DOMAIN_2=0\nDOMAIN_2=\"\"\nCERT_DOMAIN_2=\"b.example.com\"\nCERT_MODE_2=\"http\"\nMULTIPLIER_2=2\nPRESET_2=3\nMODE_NOTE_2=\"标准\"\nPORTS_2=\"tcp:4443:443\"\n" +
		"LANDING_3=203.0.113.3\nLANDING_IS_DOMAIN_3=0\nDOMAIN_3=\"\"\nCERT_DOMAIN_3=\"c.example.com\"\nCERT_MODE_3=\"manual\"\nPRESET_3=3\nMODE_NOTE_3=\"标准\"\nPORTS_3=\"tcp:5443:443\"\n" +
		"LANDING_COUNT=3\nALL_PORTS=\"tcp:443:443 tcp:4443:443 tcp:5443:443\"\n"
	rc := parseRelayConf([]byte(conf))

	// 删中间的 2 号：1 号不动，3 号前移成 2 号，值跟着走。
	lines := rebuildRelayConfAfterDelete(rc, 2)
	rc2 := parseRelayConf([]byte(strings.Join(lines, "\n")))
	if len(rc2.Landings) != 2 {
		t.Fatalf("删后应剩 2 个落地，实际 %d", len(rc2.Landings))
	}
	if got := rc2.Landings[0]; got.Landing != "203.0.113.1" || got.CertMode != "dns" || got.multiplier() != 1.5 {
		t.Fatalf("删后 1 号应是 203.0.113.1/dns/×1.5，实际 %+v（倍率 %v）", got, got.multiplier())
	}
	if got := rc2.Landings[1]; got.Landing != "203.0.113.3" || got.CertMode != "manual" || got.multiplier() != 1 {
		t.Fatalf("删后 2 号应是 203.0.113.3/manual/×1（3 号没填倍率），实际 %+v（倍率 %v）", got, got.multiplier())
	}
	for _, k := range []string{"CERT_MODE_3", "MULTIPLIER_3", "CERT_MODE_2", "MULTIPLIER_2"} {
		if k == "CERT_MODE_2" || k == "MULTIPLIER_2" {
			continue // 2 号是 3 号前移来的，有值是应该的，下面单独断言值
		}
		if rc2.Raw[k] != "" {
			t.Fatalf("删后不应残留 %s，实际 %q", k, rc2.Raw[k])
		}
	}
	if rc2.Raw["CERT_MODE_2"] != "manual" || rc2.Raw["MULTIPLIER_2"] != "" {
		t.Fatalf("删后 2 号的 CERT_MODE_2 应为 manual 且无 MULTIPLIER_2，实际 %q/%q", rc2.Raw["CERT_MODE_2"], rc2.Raw["MULTIPLIER_2"])
	}

	// 再删 1 号：剩下的 2 号前移成 1 号，值跟着走。
	lines = rebuildRelayConfAfterDelete(rc, 1)
	rc3 := parseRelayConf([]byte(strings.Join(lines, "\n")))
	if len(rc3.Landings) != 2 {
		t.Fatalf("删 1 号后应剩 2 个落地，实际 %d", len(rc3.Landings))
	}
	if got := rc3.Landings[0]; got.Landing != "203.0.113.2" || got.CertMode != "http" || got.multiplier() != 2 {
		t.Fatalf("删 1 号后新的 1 号应是 203.0.113.2/http/×2，实际 %+v（倍率 %v）", got, got.multiplier())
	}
	if got := rc3.Landings[1]; got.Landing != "203.0.113.3" || got.CertMode != "manual" || got.multiplier() != 1 {
		t.Fatalf("删 1 号后新的 2 号应是 203.0.113.3/manual/×1，实际 %+v（倍率 %v）", got, got.multiplier())
	}
	for _, k := range []string{"CERT_MODE_3", "MULTIPLIER_3"} {
		if rc3.Raw[k] != "" {
			t.Fatalf("删后不应残留 %s，实际 %q", k, rc3.Raw[k])
		}
	}
}

// TestFieldKeysHasCertModeMultiplier1347 fieldKeys 含 CERT_MODE 与 MULTIPLIER：
// 删落地重建时旧行能被清掉（不残留），删落地取回收站片段时也能取全。
func TestFieldKeysHasCertModeMultiplier1347(t *testing.T) {
	for _, idx := range []int{1, 2} {
		keys := fieldKeysForTest(idx)
		for _, want := range []string{"CERT_MODE", "MULTIPLIER"} {
			suffix := ""
			if idx != 1 {
				suffix = "_2"
			}
			found := false
			for _, k := range keys {
				if k == want+suffix {
					found = true
				}
			}
			if !found {
				t.Fatalf("fieldKeys(%d) 缺 %s：%v", idx, want+suffix, keys)
			}
		}
	}
}

func fieldKeysForTest(index int) []string {
	return (&RelayConf{}).fieldKeys(index)
}

// TestEditRebuildKeepsCertModeMultiplier1347 编辑落地重建本来就带 CERT_MODE
// 与 MULTIPLIER（v1.3.40 的 setMultiplierLine 路径），回归守住：改端口后
// 两值还在。
func TestEditRebuildKeepsCertModeMultiplier1347(t *testing.T) {
	conf := "LANDING=203.0.113.1\nCERT_DOMAIN=\"a.example.com\"\nCERT_MODE=\"dns\"\nMULTIPLIER=2.5\nPRESET=3\nPORTS=\"tcp:443:443\"\nLANDING_COUNT=1\nALL_PORTS=\"tcp:443:443\"\n"
	rc := parseRelayConf([]byte(conf))
	nl := rc.Landings[0]
	nl.Ports = parsePorts("tcp:8443:443")
	nl.PortsRaw = "tcp:8443:443"
	lines := rebuildRelayConfForEdit(rc, 1, nl, false, false, "")
	rc2 := parseRelayConf([]byte(strings.Join(lines, "\n")))
	if len(rc2.Landings) != 1 {
		t.Fatalf("编辑后应剩 1 个落地，实际 %d", len(rc2.Landings))
	}
	if got := rc2.Landings[0]; got.CertMode != "dns" || got.multiplier() != 2.5 {
		t.Fatalf("编辑后 CERT_MODE/MULTIPLIER 应是 dns/×2.5，实际 %+v（倍率 %v）", got, got.multiplier())
	}
}

// ---------- 回收站恢复不丢 MULTIPLIER（v1.3.47） ----------

// TestRecycleItemKeepsMultiplier1347 回收站记录存得下流量倍率，恢复时
// setMultiplierLine 能把它写回新编号块（=1 不写键，默认；旧记录没存按 1 算）。
func TestRecycleItemKeepsMultiplier1347(t *testing.T) {
	it := RecycleItem{Landing: "203.0.113.9", CertMode: "dns", TrafficMultiplier: 2.5}
	b, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("RecycleItem 序列化失败：%v", err)
	}
	var back RecycleItem
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("RecycleItem 反序列化失败：%v", err)
	}
	if back.TrafficMultiplier != 2.5 || back.CertMode != "dns" {
		t.Fatalf("回收站记录应存住 dns/×2.5，实际 %+v", back)
	}
	// 恢复写回：新编号块后追加 MULTIPLIER，解析回来一致。
	lines := []string{"LANDING_2=203.0.113.9", "CERT_MODE_2=\"dns\""}
	lines = setMultiplierLine(lines, "_2", back.TrafficMultiplier)
	rc := parseRelayConf([]byte(strings.Join(append(lines, "LANDING_COUNT=2"), "\n")))
	if len(rc.Landings) != 1 || rc.Landings[0].multiplier() != 2.5 {
		t.Fatalf("恢复后倍率应为 ×2.5，实际 %+v", rc.Landings)
	}
	// 旧记录没存倍率（0）时恢复不写键，按默认 1 算。
	old := RecycleItem{Landing: "203.0.113.8"}
	lines = []string{"LANDING_2=203.0.113.8"}
	lines = setMultiplierLine(lines, "_2", old.TrafficMultiplier)
	rc = parseRelayConf([]byte(strings.Join(append(lines, "LANDING_COUNT=2"), "\n")))
	if len(rc.Landings) != 1 || rc.Landings[0].multiplier() != 1 {
		t.Fatalf("旧记录恢复后倍率应默认 ×1，实际 %+v", rc.Landings)
	}
	if rc.Raw["MULTIPLIER_2"] != "" {
		t.Fatalf("倍率=1 时不应写 MULTIPLIER_2 键，实际 %q", rc.Raw["MULTIPLIER_2"])
	}
}
