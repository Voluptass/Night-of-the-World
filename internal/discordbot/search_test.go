package discordbot

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Voluptass/Night-of-the-World/internal/postsearch"
	"github.com/disgoorg/disgo/discord"
)

func TestParseTagIDs(t *testing.T) {
	ids, err := parseTagIDs("11, 22 33")
	if err != nil || len(ids) != 3 || ids[0] != 11 || ids[2] != 33 {
		t.Fatalf("解析失败: %v %v", ids, err)
	}
	if ids, err := parseTagIDs("   "); err != nil || ids != nil {
		t.Fatalf("空白应视为不筛选: %v %v", ids, err)
	}
	for _, raw := range []string{"abc", "0", "-1", "11,,", "11 22 33 44 55 66"} {
		if _, err := parseTagIDs(raw); err == nil {
			t.Fatalf("接受了非法输入 %q", raw)
		}
	}
}

func TestSanitizeTitleNeutralisesMarkdownAndLength(t *testing.T) {
	got := sanitizeTitle("[链接](http://x) `代码`\n换行")
	for _, bad := range []string{"[", "]", "`", "\n"} {
		if strings.Contains(got, bad) {
			t.Fatalf("未清理 %q: %s", bad, got)
		}
	}
	long := sanitizeTitle(strings.Repeat("字", 200))
	if utf8.RuneCountInString(long) > 81 {
		t.Fatalf("未截断: %d 字符", utf8.RuneCountInString(long))
	}
	if sanitizeTitle("   ") != "（无标题）" {
		t.Fatal("空标题未替换")
	}
}

func TestRenderSearchStaysWithinDiscordLimits(t *testing.T) {
	// 极端输入：占满关键词个数与单个词长的上限，且标签名很长，逼出内容预算保护。
	terms := make([]string, postsearch.MaxKeywordTerms)
	for i := range terms {
		terms[i] = strings.Repeat("词", postsearch.MaxTermRunes)
	}
	tags := make([]postsearch.Tag, 0, maxTagRows)
	for i := 0; i < maxTagRows; i++ {
		tags = append(tags, postsearch.Tag{ID: uint64(10 + i), Name: strings.Repeat("标签", 20)})
	}
	result := postsearch.Result{
		Forum:        postsearch.Forum{ID: 100, Name: "反馈", AvailableTags: tags},
		KeywordTerms: terms,
		TagIDs:       []uint64{10, 11, 12, 13, 14},
		Scanned:      99999,
	}
	hits := make([]postsearch.Hit, 25)
	for i := range hits {
		hits[i] = postsearch.Hit{
			ThreadID:     uint64(1000 + i),
			Name:         strings.Repeat("超长标题", 30) + "[注入]`代码`",
			MatchedTags:  []uint64{10, 11, 12, 13, 14},
			CreatedAt:    time.Unix(int64(i), 0),
			MessageCount: 3,
		}
	}
	content, components := renderSearch(result, hits, 0, strings.Repeat("a", postsearch.SearchIDLength))
	if utf8.RuneCountInString(content) > 2000 {
		t.Fatalf("内容超长: %d", utf8.RuneCountInString(content))
	}
	if !strings.Contains(content, "部分条目因长度限制未显示") {
		t.Fatal("超长时未提示条目被省略")
	}
	if len(components) != 1 {
		t.Fatalf("应有一行翻页按钮，得到 %d", len(components))
	}
	row, ok := components[0].(discord.ActionRowComponent)
	if !ok || len(row.Components) != 2 {
		t.Fatal("翻页按钮结构错误")
	}
	previous, ok := row.Components[0].(discord.ButtonComponent)
	if !ok || !previous.Disabled {
		t.Fatal("第一页的“上一页”应禁用")
	}
	next, ok := row.Components[1].(discord.ButtonComponent)
	if !ok || next.Disabled {
		t.Fatal("第一页的“下一页”应可用")
	}
	if !strings.HasPrefix(next.CustomID, searchPrefix) {
		t.Fatalf("按钮前缀错误: %s", next.CustomID)
	}
}

func TestRenderSearchSinglePageHasNoButtons(t *testing.T) {
	result := postsearch.Result{Forum: postsearch.Forum{ID: 100}, KeywordTerms: []string{"a"}}
	content, components := renderSearch(result, []postsearch.Hit{{ThreadID: 1, Name: "唯一"}}, 0, "0123456789abcdef")
	if len(components) != 0 {
		t.Fatal("单页结果不应有翻页按钮")
	}
	if !strings.Contains(content, "第 1/1 页") {
		t.Fatalf("页码信息缺失: %s", content)
	}
}

func TestRenderEmptySearchListsTags(t *testing.T) {
	result := postsearch.Result{
		Forum:        postsearch.Forum{ID: 100, AvailableTags: []postsearch.Tag{{ID: 11, Name: "已解决"}}},
		KeywordTerms: []string{"不存在"},
		Scanned:      12,
	}
	content := renderEmptySearch(result)
	if !strings.Contains(content, "没有匹配的帖子") || !strings.Contains(content, "已解决") {
		t.Fatalf("空结果说明不完整: %s", content)
	}
	if !strings.Contains(content, "只按帖子标题匹配") {
		t.Fatal("缺少“不搜索正文”的说明")
	}
}
