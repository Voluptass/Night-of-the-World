package discordbot

import "testing"

func TestParseAccount(t *testing.T) {
	for _, input := range []string{"123", "<@123>", "<@!123>", " 123 "} {
		id, err := ParseAccount(input)
		if err != nil || id != 123 {
			t.Fatalf("%q: %d %v", input, id, err)
		}
	}
	for _, input := range []string{"0", "-1", "+123", "Alice", "<@&123>", "<@123>suffix", "18446744073709551616", ""} {
		if _, err := ParseAccount(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestParsePost(t *testing.T) {
	for _, input := range []string{"456", "https://discord.com/channels/123/456", "https://discord.com/channels/123/456/789", "https://ptb.discord.com/channels/123/456/"} {
		id, err := ParsePost(input, 123)
		if err != nil || id != 456 {
			t.Fatalf("%q: %d %v", input, id, err)
		}
	}
	for _, input := range []string{
		"https://discord.com/channels/999/456", "https://evil.example/channels/123/456", "http://discord.com/channels/123/456",
		"https://discord.com.evil.example/channels/123/456", "https://discord.com@evil.example/channels/123/456", "https://user@discord.com/channels/123/456",
		"https://discord.com:443/channels/123/456", "https://discord.com/channels/@me/456", "https://discord.com/channels/123/0",
		"https://discord.com/channels/123/456/xyz", "https://discord.com/channels/123/456?extra=1", "https://discord.com/channels/123/%34%35%36",
		"https://discord.com//channels/123/456", "https://discord.com/channels/123/456/789/more", "0", "random",
	} {
		if _, err := ParsePost(input, 123); err == nil {
			t.Fatalf("accepted unsafe/wrong-scope input %q", input)
		}
	}
}

func FuzzParseTargets(f *testing.F) {
	for _, seed := range []string{"123", "<@!123>", "https://discord.com/channels/123/456", "", "https://user@evil/channels/123/456"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if id, err := ParseAccount(input); err == nil && id == 0 {
			t.Fatal("accepted zero account")
		}
		if id, err := ParsePost(input, 123); err == nil && id == 0 {
			t.Fatal("accepted zero post")
		}
	})
}
