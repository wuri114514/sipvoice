package mail

import "testing"

func TestSubjectIsFax(t *testing.T) {
	r := DefaultReceiveConfig()
	cases := map[string]bool{
		"[FAX] 合同扫描件":     true,
		"[fax] lowercase": true,
		"转发：[FAX] 请发":     true,
		"合同扫描件":           false,
		"FAX 没有方括号":       false,
		"[FAX无右括号":        false,
	}
	for sub, want := range cases {
		if got := r.SubjectIsFax(sub); got != want {
			t.Errorf("SubjectIsFax(%q) = %v, want %v", sub, got, want)
		}
	}
}

func TestSenderAllowed(t *testing.T) {
	r := DefaultReceiveConfig()
	r.Username = "fax@163.com"
	r.FromAllow = []string{"Boss@Example.com", "*@corp.com"}
	r.Normalize()

	cases := map[string]bool{
		"boss@example.com":   true, // 大小写不敏感
		"anyone@corp.com":    true, // 域通配
		"anyone@corp.com.cn": false,
		"evil@example.com":   false,
		"fax@163.com":        true, // 自己发给自己，方便测试
		"":                   false,
	}
	for from, want := range cases {
		if got := r.SenderAllowed(from); got != want {
			t.Errorf("SenderAllowed(%q) = %v, want %v", from, got, want)
		}
	}

	empty := DefaultReceiveConfig()
	if empty.SenderAllowed("boss@example.com") {
		t.Error("白名单为空时必须一律拒绝")
	}
}

func TestExtractDest(t *testing.T) {
	cases := map[string]string{
		"请发到 [2002] 谢谢":             "2002",
		"号码是【1003】":                 "1003",
		"发 [ +86 138-0013-8000 ] 吧": "+8613800138000",
		"没有号码":                      "",
		"[abc] 不是号码":                "",
	}
	for body, want := range cases {
		if got := ExtractDest(body); got != want {
			t.Errorf("ExtractDest(%q) = %q, want %q", body, got, want)
		}
	}
}
