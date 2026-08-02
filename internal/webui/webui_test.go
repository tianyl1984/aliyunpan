package webui

import (
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCleanPanPath(t *testing.T) {
	cases := map[string]string{
		"":           "/",
		"/":          "/",
		"a/b":        "/a/b",
		"/a/b/":      "/a/b",
		"/a//b":      "/a/b",
		"/a/./b":     "/a/b",
		"/a/b/../c":  "/a/c",
		`\a\b`:       "/a/b",
		"/a/b/../..": "/",
		"/../../etc": "/etc",

		// 网盘文件名允许首尾空格，必须原样保留，否则 get_by_path 会找不到文件
		"/圣斗士 4K ":    "/圣斗士 4K ",
		"/圣斗士 4K /":   "/圣斗士 4K ",
		"/ a / b ":    "/ a / b ",
		"/a/b /../c ": "/a/c ",
	}
	for in, want := range cases {
		if got := cleanPanPath(in); got != want {
			t.Errorf("cleanPanPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeTrustedOrigins(t *testing.T) {
	// 合法输入：统一小写、去掉协议默认端口、去重
	ok := map[string]string{
		"https://pan.example.com":      "https://pan.example.com",
		"https://PAN.Example.COM":      "https://pan.example.com",
		"https://pan.example.com:443":  "https://pan.example.com",
		"http://pan.example.com:80":    "http://pan.example.com",
		"https://pan.example.com:8443": "https://pan.example.com:8443",
		"http://192.168.1.10:9000":     "http://192.168.1.10:9000",
		"https://pan.example.com/":     "https://pan.example.com",
		"  https://pan.example.com  ":  "https://pan.example.com",
		"http://[::1]:9000":            "http://[::1]:9000",
	}
	for in, want := range ok {
		got, err := normalizeTrustedOrigins([]string{in})
		if err != nil {
			t.Errorf("normalizeTrustedOrigins(%q) 报错: %v", in, err)
			continue
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("normalizeTrustedOrigins(%q) = %v, want [%s]", in, got, want)
		}
	}

	// 去重：不同写法归一化后是同一个来源
	if got, err := normalizeTrustedOrigins([]string{
		"https://pan.example.com", "https://PAN.example.com:443", "",
	}); err != nil || len(got) != 1 {
		t.Errorf("去重失败: got=%v err=%v", got, err)
	}

	// 非法输入必须报错，不能静默丢弃
	bad := []string{
		"*",                     // 通配符会让整层防护失效
		"pan.example.com",       // 缺少协议
		"pan.example.com:9000",  // 缺少协议
		"ftp://pan.example.com", // 非 http(s)
		"https://",              // 没有主机
	}
	for _, in := range bad {
		if _, err := normalizeTrustedOrigins([]string{in}); err == nil {
			t.Errorf("normalizeTrustedOrigins(%q) 应当报错，却通过了", in)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	mustParse := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", raw, err)
		}
		return u
	}

	// 场景一：没配可信来源，维持原有的严格行为
	strict := &Server{opt: &Options{Host: "0.0.0.0", Port: 8080}}
	if !strict.originAllowed(mustParse("http://127.0.0.1:8080")) {
		t.Error("端口一致时应当放行")
	}
	if strict.originAllowed(mustParse("http://127.0.0.1:18097")) {
		t.Error("端口不一致时应当拒绝（这正是端口映射踩到的坑）")
	}
	if strict.originAllowed(mustParse("https://pan.example.com:8443")) {
		t.Error("未声明的外部来源应当拒绝")
	}

	// 场景二：配了可信来源
	trusted, err := normalizeTrustedOrigins([]string{
		"https://pan.example.com", "http://192.168.1.10:9000",
	})
	if err != nil {
		t.Fatalf("规范化失败: %v", err)
	}
	s := &Server{opt: &Options{Host: "0.0.0.0", Port: 8080}, trustedOrigins: trusted}

	for _, raw := range []string{
		"https://pan.example.com",
		"https://pan.example.com:443", // 浏览器不会这么发，但容错
		"https://PAN.EXAMPLE.COM",
		"http://192.168.1.10:9000",
		"http://127.0.0.1:8080", // 叠加而非替换：直连依然可用
	} {
		if !s.originAllowed(mustParse(raw)) {
			t.Errorf("%q 应当放行", raw)
		}
	}

	for _, raw := range []string{
		"http://pan.example.com",       // 协议不符
		"https://evil.example.com",     // 未声明的域名
		"https://pan.example.com:8443", // 端口不符
		"https://pan.example.com.evil.com",
	} {
		if s.originAllowed(mustParse(raw)) {
			t.Errorf("%q 应当拒绝", raw)
		}
	}
}

func TestDirsOfPath(t *testing.T) {
	got := dirsOfPath("/a/b/c")
	want := []string{"/", "/a", "/a/b", "/a/b/c"}
	if len(got) != len(want) {
		t.Fatalf("dirsOfPath = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dirsOfPath = %v, want %v", got, want)
		}
	}
}

// TestResolveLocalPathEscape 是安全相关的回归用例：
// 任何形式的越界（.. 穿越、符号链接逃逸、白名单外的绝对路径）都必须被拒绝。
func TestResolveLocalPathEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	// 白名单内指向白名单外的符号链接
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前环境不支持符号链接: %v", err)
	}

	s := &Server{localRoots: normalizeRoots([]string{root})}

	allowed := []string{
		root,
		filepath.Join(root, "sub"),
		filepath.Join(root, "sub", "not-exist-yet"),
		filepath.Join(root, "sub", "..", "sub"),
	}
	for _, p := range allowed {
		if _, err := s.resolveLocalPath(p); err != nil {
			t.Errorf("resolveLocalPath(%q) 应当允许，却报错: %v", p, err)
		}
	}

	denied := []string{
		outside,
		filepath.Join(outside, "secret.txt"),
		filepath.Join(root, ".."),
		filepath.Join(root, "..", filepath.Base(outside)),
		link,                              // 符号链接本身
		filepath.Join(link, "secret.txt"), // 经由符号链接逃逸
		"",
	}
	for _, p := range denied {
		if _, err := s.resolveLocalPath(p); err == nil {
			t.Errorf("resolveLocalPath(%q) 应当被拒绝，却通过了", p)
		}
	}
}

func TestGatePauseResume(t *testing.T) {
	g := newGate()
	g.pause()

	released := make(chan struct{})
	go func() {
		g.waitIfPaused()
		close(released)
	}()

	select {
	case <-released:
		t.Fatal("暂停状态下 waitIfPaused 不应立即返回")
	case <-time.After(80 * time.Millisecond):
	}

	g.resume()
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("resume 后 waitIfPaused 应当返回")
	}
}

// TestGateCancelWakesWaiters 取消必须能唤醒所有阻塞在暂停上的协程，
// 否则取消一个已暂停的任务会永久卡住执行器。
func TestGateCancelWakesWaiters(t *testing.T) {
	g := newGate()
	g.pause()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.waitIfPaused()
		}()
	}

	time.Sleep(50 * time.Millisecond)
	g.cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel 没有唤醒阻塞中的协程")
	}
	if !g.isCanceled() {
		t.Fatal("isCanceled 应为 true")
	}
}

func TestEventBrokerNeverBlocks(t *testing.T) {
	b := NewEventBroker()
	_, cancel := b.Subscribe() // 订阅但从不消费
	defer cancel()

	// 发布远超缓冲区的事件量。发布方可能是下载协程，绝不能被慢消费者阻塞。
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer*4; i++ {
			b.Publish(Event{Type: EventLog, Data: i})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Publish 在慢消费者面前阻塞了")
	}
}

func TestEventBrokerDelivers(t *testing.T) {
	b := NewEventBroker()
	ch, cancel := b.Subscribe()
	defer cancel()

	b.Publish(Event{Type: EventJobState, Data: "hello"})
	select {
	case ev := <-ch:
		if ev.Type != EventJobState || ev.Data != "hello" {
			t.Fatalf("收到意外事件: %+v", ev)
		}
		if ev.Id == 0 || ev.Ts == 0 {
			t.Fatalf("事件缺少 Id/Ts: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("没有收到事件")
	}
}

func TestConsoleDenylist(t *testing.T) {
	c := NewConsole(NewEventBroker(), false)

	// 这些命令在服务进程里执行会有实际危害，必须始终拒绝
	for _, name := range []string{"who", "sync", "login", "su", "drive", "run"} {
		if _, denied := c.denyReason(name); !denied {
			t.Errorf("命令 %q 应当被禁用", name)
		}
	}
	for _, name := range []string{"ls", "tree", "share", "album", "recycle", "config"} {
		if _, denied := c.denyReason(name); denied {
			t.Errorf("命令 %q 不应被禁用", name)
		}
	}

	// --allow-shell 只放开 run，其余仍然禁用
	c2 := NewConsole(NewEventBroker(), true)
	if _, denied := c2.denyReason("run"); denied {
		t.Error("--allow-shell 下 run 应当被允许")
	}
	if _, denied := c2.denyReason("who"); !denied {
		t.Error("--allow-shell 不应放开 who")
	}
}

func TestAuthDeriveKeyStable(t *testing.T) {
	salt := []byte("0123456789abcdef")
	a := deriveKey("hunter2", salt)
	b := deriveKey("hunter2", salt)
	c := deriveKey("hunter3", salt)

	if string(a) != string(b) {
		t.Fatal("相同口令应派生出相同的密钥")
	}
	if string(a) == string(c) {
		t.Fatal("不同口令不应派生出相同的密钥")
	}
	if len(a) != 32 {
		t.Fatalf("密钥长度应为 32，实际 %d", len(a))
	}
}

func TestHostAllowed(t *testing.T) {
	s := &Server{opt: &Options{Host: "127.0.0.1", Port: 8080}}
	for _, h := range []string{"127.0.0.1:8080", "localhost:8080"} {
		if !s.hostAllowed("http", h) {
			t.Errorf("hostAllowed(http, %q) 应为 true", h)
		}
	}
	for _, h := range []string{"evil.com", "evil.com:8080", "127.0.0.1:9999", ""} {
		if s.hostAllowed("http", h) {
			t.Errorf("hostAllowed(http, %q) 应为 false", h)
		}
	}
}

// TestHostAllowedDefaultPort 是安全相关的回归用例。
//
// 监听 0.0.0.0 时，早期实现对"不带端口的 Origin"直接跳过端口校验，
// 然后走到 0.0.0.0 的放行分支返回 true —— 任意 https:// 站点都能通过
// Origin 校验。现在改为按协议默认端口补齐后再比对。
func TestHostAllowedDefaultPort(t *testing.T) {
	wide := &Server{opt: &Options{Host: "0.0.0.0", Port: 8080}}
	for _, c := range []struct {
		scheme, host string
	}{
		{"https", "evil.com"},        // 隐含 443，对不上 8080
		{"http", "evil.com"},         // 隐含 80
		{"https", "pan.example.com"}, // 合法反代域名同样要显式声明
		{"", "evil.com"},             // 协议未知，端口无法推断
	} {
		if wide.hostAllowed(c.scheme, c.host) {
			t.Errorf("hostAllowed(%q, %q) 应为 false", c.scheme, c.host)
		}
	}
	// 端口显式对上时仍然放行，局域网 IP 直连不受影响
	if !wide.hostAllowed("http", "192.168.1.10:8080") {
		t.Error(`hostAllowed(http, "192.168.1.10:8080") 应为 true`)
	}

	// 服务本身就跑在 80 端口时，不带端口的同机来源应当放行
	p80 := &Server{opt: &Options{Host: "0.0.0.0", Port: 80}}
	if !p80.hostAllowed("http", "192.168.1.10") {
		t.Error(`监听 80 端口时 hostAllowed(http, "192.168.1.10") 应为 true`)
	}
	if p80.hostAllowed("https", "192.168.1.10") {
		t.Error(`监听 80 端口时 https 来源（隐含 443）应为 false`)
	}
}
