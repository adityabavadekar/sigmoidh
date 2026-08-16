package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsTextFile(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"plain text", []byte("hello\nworld\n"), true},
		{"csv", []byte("a,b,c\n1,2,3\n"), true},
		{"json", []byte(`{"a":1}`), true},
		{"empty", []byte(""), true},
		{"png header", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, false},
		{"elf header", []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00}, false},
		{"random binary", []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0xfd}, false},
	}
	for _, c := range cases {
		if got := isTextFile(c.data); got != c.want {
			t.Errorf("%s: isTextFile=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsSensitiveFile(t *testing.T) {
	trueCases := []string{
		".env", ".env.local", ".env.production", ".env.example",
		"config/.env", "/mnt/data/.env.staging",
		"id_rsa", "/home/user/.ssh/id_rsa", "id_ed25519",
		".npmrc", ".netrc", "server.pem", "private.key",
	}
	for _, p := range trueCases {
		if !isSensitiveFile(p) {
			t.Errorf("isSensitiveFile(%q) = false, want true", p)
		}
	}

	falseCases := []string{
		"environment.yaml", "venv/bin/activate", "myenv.txt",
		".environment", "env.go", "docker-compose.env",
		"main.go", "notes.txt", "data.csv",
	}
	for _, p := range falseCases {
		if isSensitiveFile(p) {
			t.Errorf("isSensitiveFile(%q) = true, want false", p)
		}
	}
}

func TestIsProtectedPath(t *testing.T) {
	trueCases := []string{"/", "/etc", "/etc/passwd", "/usr/local/bin/tool", "/root", "/root/project"}
	for _, p := range trueCases {
		if !isProtectedPath(p) {
			t.Errorf("isProtectedPath(%q) = false, want true", p)
		}
	}

	falseCases := []string{"/tmp/build", "/mnt/data", "./node_modules", "/etcetera", "/etcetera/file"}
	for _, p := range falseCases {
		if isProtectedPath(p) {
			t.Errorf("isProtectedPath(%q) = true, want false", p)
		}
	}
}

func TestDestructiveCommandOnProtectedPath(t *testing.T) {
	blockedCases := []string{
		// Basic rm variants.
		"rm -rf /",
		"rm -fr /",
		"rm -r -f /",
		"rm -f -r /",
		"rm -Rf /",
		"rm -RF /",
		"rm -r -f /etc",
		"rm  -r  -f  /etc",
		"rm    -rf    /etc",
		"rm -rf /etc/",
		"rm -rf //",
		"rm -rf ///",
		"rm -rf /var",
		"rm -rf /var/",
		"rm -rf /var/lib",
		"rm -rf /var/lib/",
		"rm -rf /usr",
		"rm -rf /usr/",
		"rm -rf /usr/bin",
		"rm -rf /usr/local",
		"rm -rf /home",
		"rm -rf /home/",
		"rm -rf /root",
		"rm -rf /root/",
		"rm -rf /boot",
		"rm -rf /boot/",
		"rm -rf /dev",
		"rm -rf /dev/",
		"rm -rf /proc",
		"rm -rf /proc/",
		"rm -rf /sys",
		"rm -rf /sys/",
		"rm -rf /run",
		"rm -rf /run/",
		"rm -rf /opt",
		"rm -rf /srv",
		"rm -rf /mnt",
		"rm -rf /media",

		// Home expansion.
		"rm -rf ~",
		"rm -rf ~/",
		"rm -rf ~/.",
		"rm -rf ~/",
		"rm -rf ~/*",
		"sudo rm -rf ~",
		"sudo rm -rf ~/",
		"sudo rm -rf /",
		"sudo -n rm -rf /",
		"sudo -- rm -rf /",

		// Command chaining.
		"echo hi && rm -rf /",
		"echo hi && rm -rf /etc",
		"true && rm -rf /",
		"false || rm -rf /",
		"echo hi ; rm -rf /",
		"echo hi ; rm -rf /var/lib",
		"echo hi\nrm -rf /",
		"echo hi\nrm -rf /etc",
		"cd /tmp && cd / && rm -rf /",
		"cd / && rm -rf /",
		"cd /etc; rm -rf /etc",
		"printf ok; sudo rm -rf /",

		// Parentheses / subshells.
		"(rm -rf /)",
		"(echo hi; rm -rf /)",
		"(true && rm -rf /)",
		"$(rm -rf /)",
		"$(echo hi && rm -rf /)",
		"`rm -rf /`",
		"`echo hi; rm -rf /`",

		// Environment wrappers.
		"env rm -rf /",
		"env -- rm -rf /",
		"env PATH=/bin:/usr/bin rm -rf /",
		"sudo env rm -rf /",
		"sudo env -- rm -rf /",
		"command rm -rf /",

		// dd / direct device destruction.
		"dd if=/dev/zero of=/dev/sda",
		"dd if=/dev/zero of=/dev/sda bs=1M",
		"dd if=/dev/zero of=/dev/sda bs=4M count=100",
		"dd if=/dev/random of=/dev/sda",
		"dd if=/dev/zero of=/dev/nvme0n1",
		"dd if=/dev/zero of=/dev/nvme0n1 bs=1M",
		"sudo dd if=/dev/zero of=/dev/sda",
		"sudo dd if=/dev/zero of=/dev/nvme0n1",
		"dd of=/dev/sda if=/dev/zero",
		"dd of=/dev/nvme0n1 if=/dev/zero",

		// Filesystem destruction.
		"mkfs.ext4 /dev/sda",
		"mkfs.ext4 /dev/nvme0n1",
		"mkfs.xfs /dev/sda",
		"mkfs.btrfs /dev/sda",
		"sudo mkfs.ext4 /dev/sda",
		"wipefs -a /dev/sda",
		"wipefs --all /dev/sda",
		"sudo wipefs -a /dev/sda",

		// Fork bombs.
		":(){ :|:& };:",
		": () { : | : & }; :",
		"(){ :|:& };:",
		"function bomb(){ bomb|bomb& };bomb",

		// find-based deletion.
		"find / -delete",
		"find / -xdev -delete",
		"find /etc -delete",
		"find /var/lib -delete",
		"find / -type f -delete",
		"find / -type d -delete",
		"sudo find / -delete",

		// Recursive chmod/chown can destroy system accessibility.
		"chmod -R 000 /",
		"chmod -R 000 /etc",
		"chmod -R 777 /",
		"chown -R nobody:nobody /",
		"chown -R root:root /",

		// Long chained destructive commands.
		"echo start && echo one && echo two && echo three && rm -rf /",
		"true && true && true && true && true && rm -rf /etc",
		"printf x; printf y; printf z; rm -rf /var/lib",
		"cd /tmp && pwd && ls && cd / && rm -rf /",
		"echo preparing && sleep 1 && echo almost && rm -rf /",
		"true || true || true || sudo rm -rf /",

		// Destructive command buried deeply in a chain.
		"echo 1 && echo 2 && echo 3 && echo 4 && echo 5 && rm -rf /",
		"echo 1; echo 2; echo 3; echo 4; echo 5; rm -rf /etc",
		"true && true && true && true && true && true && rm -rf /var",
		"false || false || false || false || sudo rm -rf /",

		// Multiple destructive commands.
		"rm -rf /tmp && rm -rf /etc",
		"rm -rf /tmp; rm -rf /",
		"rm -rf /var && rm -rf /usr",
		"echo ok && rm -rf /home && echo done && rm -rf /root",
		"sudo rm -rf /etc && sudo rm -rf /var/lib",

		// Long command prefixes.
		"echo this is a very long harmless-looking command that eventually performs a destructive operation && rm -rf /",
		"printf '%s\\n' this command contains a very long amount of unrelated shell text before the destructive operation; rm -rf /",
		"echo one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty && rm -rf /",

		// Absolute paths with redundant components.
		"rm -rf /./",
		"rm -rf /./etc",
		"rm -rf /etc/..",
		"rm -rf /var/../",
		"rm -rf /usr/../",
		"rm -rf /tmp/../etc",
		"rm -rf /var/lib/../../etc",
		"rm -rf /foo/../../",
	}

	allowedCases := []string{
		// Normal project/build cleanup.
		"rm -rf /tmp/build",
		"rm -rf /tmp/my-project",
		"rm -rf ./node_modules",
		"rm -rf ./build",
		"rm -rf ./dist",
		"rm -rf ./target",
		"rm -rf ./tmp",
		"rm -rf ./cache",
		"rm -rf ../build",
		"rm -rf ../tmp",
		"rm -rf ./foo/bar",
		"rm -rf ./foo/bar/baz",
		"rm -r ./build",
		"rm -f ./build/output",
		"rm -rf ./node_modules ./dist ./build",

		// Commands merely talking about destructive commands.
		"echo 'rm -rf /' > file.txt",
		`echo "just talking about rm -rf here"`,
		`printf '%s\n' "rm -rf /etc"`,
		"echo rm -rf /",
		"printf 'rm -rf /'",
		"grep 'rm -rf /' file.txt",
		"grep -r 'rm -rf /' .",
		"grep -r rm -rf .",
		"cat file | grep foo",
		"cat README.md | grep 'rm -rf /'",
		"echo 'sudo rm -rf /' >> commands.txt",

		// Chaining safe operations.
		"rm -rf ./node_modules && echo done",
		"rm -rf ./build && go build ./...",
		"rm -rf ./dist; mkdir ./dist",
		"echo clean && rm -rf ./tmp && echo done",
		"true && rm -rf ./cache",
		"false || rm -rf ./build",

		// Absolute paths that aren't protected system paths.
		"rm -rf /tmp",
		"rm -rf /tmp/",
		"rm -rf /tmp/build",
		"rm -rf /tmp/build/output",
		"rm -rf /tmp/foo/bar",

		// Long harmless commands.
		"echo this is a very long command containing many words and arguments but it only prints text to stdout",
		"printf '%s\\n' this is an intentionally long command used to stress the parser without performing any destructive operation",
		"echo one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty",

		// Similar-looking but non-destructive commands.
		"ls -la /etc",
		"ls -la /",
		"find ./build -type f",
		"find ./tmp -type f -print",
		"grep -r rm -rf .",
		"grep -R 'sudo rm -rf /' .",
		"cat ./commands.txt",
		"du -sh /tmp",
		"stat /etc",
		"file /etc/passwd",
	}

	for _, c := range blockedCases {
		if _, blocked := destructiveCommandOnProtectedPath(c); !blocked {
			t.Errorf("%q: expected blocked, was allowed", c)
		}
	}

	for _, c := range allowedCases {
		if seg, blocked := destructiveCommandOnProtectedPath(c); blocked {
			t.Errorf("%q: expected allowed, was blocked on segment %q", c, seg)
		}
	}

	// Stress-test very long commands with the destructive operation buried
	// after a large amount of otherwise irrelevant shell text.
	longPrefixes := []string{
		"echo alpha",
		"echo beta",
		"printf 'gamma'",
		"true",
		"pwd",
		"echo delta",
		"printf 'epsilon'",
		"true",
		"echo zeta",
		"pwd",
		"echo eta",
		"printf 'theta'",
		"true",
		"echo iota",
		"pwd",
	}

	var longCommand string
	for i := 0; i < 100; i++ {
		if i > 0 {
			longCommand += " && "
		}
		longCommand += longPrefixes[i%len(longPrefixes)]
	}
	longCommand += " && rm -rf /"

	if _, blocked := destructiveCommandOnProtectedPath(longCommand); !blocked {
		t.Errorf("very long destructive command: expected blocked, was allowed")
	}

	// Very long chain with the destructive command at the beginning,
	// middle, and end.
	stressCommands := []string{
		"rm -rf / && " + longCommand,
		longCommand + " && rm -rf /",
		"echo start && rm -rf / && " + longCommand,
		"echo start && " + longCommand + " && rm -rf /etc",
	}

	for _, c := range stressCommands {
		if _, blocked := destructiveCommandOnProtectedPath(c); !blocked {
			t.Errorf("stress command: expected blocked, was allowed")
		}
	}
}

func TestRunReadGuards(t *testing.T) {
	dir := t.TempDir()

	envPath := filepath.Join(dir, ".env")
	os.WriteFile(envPath, []byte("SECRET=1"), 0o644)
	if out, isErr := runRead(envPath, 0, 0); !isErr {
		t.Errorf("runRead(.env) should refuse, got ok output: %s", out)
	}

	binPath := filepath.Join(dir, "image.bin")
	os.WriteFile(binPath, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0}, 0o644)
	if out, isErr := runRead(binPath, 0, 0); !isErr {
		t.Errorf("runRead(binary) should refuse, got ok output: %s", out)
	}

	csvPath := filepath.Join(dir, "data.csv")
	os.WriteFile(csvPath, []byte("a,b\n1,2\n"), 0o644)
	if out, isErr := runRead(csvPath, 0, 0); isErr {
		t.Errorf("runRead(csv) should succeed, got error: %s", out)
	}

	txtPath := filepath.Join(dir, "notes.txt")
	os.WriteFile(txtPath, []byte("line1\nline2\nline3\n"), 0o644)
	if out, isErr := runRead(txtPath, 0, 0); isErr || out != "line1\nline2\nline3\n" {
		t.Errorf("runRead(txt) mismatch: %q err=%v", out, isErr)
	}
}

func TestRunWriteGuards(t *testing.T) {
	dir := t.TempDir()

	if out, isErr := runWrite(filepath.Join(dir, ".env"), "SECRET=1"); !isErr {
		t.Errorf("runWrite(.env) should refuse, got ok output: %s", out)
	}

	if out, isErr := runWrite("/etc/should-not-write", "x"); !isErr {
		t.Errorf("runWrite(/etc/...) should refuse, got ok output: %s", out)
	}

	safe := filepath.Join(dir, "sub", "file.txt")
	if out, isErr := runWrite(safe, "hello"); isErr {
		t.Errorf("runWrite(safe path) should succeed, got error: %s", out)
	}
	data, _ := os.ReadFile(safe)
	if string(data) != "hello" {
		t.Errorf("runWrite content mismatch: %q", data)
	}
}

func requireDangerousTestSandbox(t *testing.T) {
	t.Helper()

	if os.Getenv("DANGEROUS_TEST_SANDBOX") != "1" {
		t.Skip("dangerous tests disabled; set DANGEROUS_TEST_SANDBOX=1")
	}

	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Skip("dangerous tests require Docker sandbox")
	}
}

func TestRunBashGuards(t *testing.T) {

	// t.Run("pwd", func(t *testing.T) {
	// 	out, isErr := runBash(context.Background(), "pwd")
	// 	if isErr {
	// 		t.Fatalf("runBash(pwd) failed: %s", out)
	// 	}
	// 	if out != "/\n" {
	// 		t.Fatalf("runBash(pwd) output mismatch: %q", out)
	// 	}
	// })
	//
	requireDangerousTestSandbox(t)

	// ctx := context.Background()

	// cases := []string{
	// 	"rm -rf /",
	// 	"rm -rf /etc",
	// 	"rm -rf /var/lib",
	// 	"sudo rm -rf /",
	// 	"dd if=/dev/zero of=/dev/sda",
	// 	"mkfs.ext4 /dev/sda",
	// 	":(){ :|:& };:",
	// }
	//
	// for _, command := range cases {
	// 	t.Run(command, func(t *testing.T) {
	// 		out, isErr := runBash(ctx, command)
	//
	// 		if !isErr {
	// 			t.Fatalf("expected refusal for %q, got: %s", command, out)
	// 		}
	// 	})
	// }
}
