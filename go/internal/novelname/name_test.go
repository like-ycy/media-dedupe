package novelname

import "testing"

func TestParseInventoryCases(t *testing.T) {
	cases := []struct{ raw, title string }{
		{"妻心如刀www.txt2016.com.txt", "妻心如刀"},
		{"soushu2025.com@我的妈妈有双重人格 1-50[搜书吧].txt", "我的妈妈有双重人格"},
		{"【我的妈妈有双重人格】（1-778）作者：瘦不了.txt", "我的妈妈有双重人格"},
		{"《超凡都市2035》（1-71章）作者：npwarship.txt", "超凡都市2035"},
		{"超凡都市2035(1-227章).txt", "超凡都市2035"},
		{"2012末日狂欢.txt", "2012末日狂欢"},
		{"[更283]帝王成长计划.txt", "帝王成长计划"},
		{"主子(上).txt", "主子"},
		{"帝台春 BY 色如空[下].txt", "帝台春"},
		{"龙战士传说——前传.txt", "龙战士传说"},
		{"《书名测试》（1-12章）作者：某人.txt.txt", "书名测试"},
		{"《我的妹妹是最棒的配菜》1-57-soushu2022.com-[搜书吧网址].txt", "我的妹妹是最棒的配菜"},
		{"8.4《赘婿的荣耀》1-23卷21 作者：某某.txt", "赘婿的荣耀"},
		{"第一序列.txt", "第一序列"},
		{"第二次重生(gl).txt", "第二次重生"},
		{"第九部 丹杏.txt", "第九部丹杏"},
		{"第五部 紫玫.txt", "第五部紫玫"},
		{"《普通作品》BY王甲.txt", "普通作品"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			n := Parse(tc.raw)
			if n.Title != tc.title {
				t.Fatalf("%+v want title %s", n, tc.title)
			}
		})
	}
	if n := Parse("帝台春 BY 色如空[下].txt"); len(n.Parts) != 1 || n.Parts[0] != "下" || len(n.Authors) != 1 || n.Authors[0] != "色如空" {
		t.Fatalf("lost post-author part: %+v", n)
	}
	if n := Parse("770577.txt"); len(n.Keys) != 0 {
		t.Fatalf("numeric title became matching key: %+v", n)
	}
	if n := Parse("《书名测试》作者：某人 [已完结 1-8章].txt"); len(n.Ranges) != 1 || n.Ranges[0].End != 8 {
		t.Fatalf("lost post-author range: %+v", n)
	}
	for _, raw := range []string{"第一序列.txt", "第二次重生(gl).txt", "第九部 丹杏.txt", "第五部 紫玫.txt"} {
		if n := Parse(raw); len(n.Ranges) != 0 {
			t.Fatalf("title ordinal became chapter progress: %+v", n)
		}
	}
	if n := Parse("《普通作品》BY王甲.txt"); len(n.Authors) != 1 || n.Authors[0] != "王甲" {
		t.Fatalf("missing compact BY author: %+v", n)
	}
	if len(Differences(Parse("主子(上).txt"), Parse("主子(下).txt"))) == 0 {
		t.Fatal("lost complementary part")
	}
}

func TestNameEvidenceNotIdentity(t *testing.T) {
	if Match(Parse("鹿鼎记.txt"), Parse("鹿鼎记别传.txt")) != 0 {
		t.Fatal("short prefix should not match")
	}
	if len(Differences(Parse("同名作品 作者：甲.txt"), Parse("同名作品 作者：乙.txt"))) == 0 {
		t.Fatal("lost author conflict")
	}
	if Match(Parse("我和我的母亲（寄印传奇）.txt"), Parse("寄印传奇【我和我的母亲】.txt")) != 1 {
		t.Fatal("lost reversed alias")
	}
	if len(Parse("鲁鲁修 (2).txt").Notes) == 0 {
		t.Fatal("ambiguous copy suffix must be visible")
	}
}
