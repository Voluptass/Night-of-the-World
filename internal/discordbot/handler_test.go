package discordbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Voluptass/Night-of-the-World/internal/postsearch"
	"github.com/Voluptass/Night-of-the-World/internal/purge"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
)

func TestRegisterUpsertsOnlyNamedGuildCommands(t *testing.T) {
	names := make(chan string, 3)
	api := testREST(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/applications/123/guilds/9/commands" {
			t.Errorf("unsafe registration: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var body struct {
			Name        string `json:"name"`
			Permissions string `json:"default_member_permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if body.Permissions != "8192" {
			t.Errorf("command not restricted to Manage Messages: %q", body.Permissions)
		}
		names <- body.Name
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"100","application_id":"123","guild_id":"9","type":1,"name":%q,"description":"test"}`, body.Name)
	})
	if err := Register(context.Background(), api, 123, 9); err != nil {
		t.Fatal(err)
	}
	// Register 只 upsert 这三个具名命令，不批量覆盖。
	got := map[string]bool{}
	for range 3 {
		got[<-names] = true
	}
	for _, want := range []string{"purge-user", "purge-status", "search-posts"} {
		if !got[want] {
			t.Fatalf("command %q not registered; got %v", want, got)
		}
	}
}

func TestRenderSafetyAndDiscordLength(t *testing.T) {
	for _, state := range []purge.State{purge.Scanning, purge.Ready, purge.Deleting, purge.Completed, purge.Cancelled, purge.Failed, purge.Expired} {
		job := purge.Job{ID: strings.Repeat("a", 32), State: state, Target: purge.Target{GuildID: 9, ThreadID: 1, ActorID: 7, AccountID: 42}, Stats: purge.Stats{Scanned: 100000, Matched: 50000}, ExpiresAt: time.Now().Add(time.Minute)}
		message := Render(job)
		if message.Content == nil || utf8.RuneCountInString(*message.Content) > 2000 {
			t.Fatalf("invalid content length for %s", state)
		}
		if message.AllowedMentions == nil || len(message.AllowedMentions.Parse) != 0 || len(message.AllowedMentions.Users) != 0 || len(message.AllowedMentions.Roles) != 0 || message.AllowedMentions.RepliedUser {
			t.Fatal("mentions enabled")
		}
		if message.Components == nil {
			t.Fatal("stale buttons would not be cleared")
		}
		if state == purge.Ready && !strings.Contains(*message.Content, "删除不可恢复") {
			t.Fatal("missing destructive confirmation warning")
		}
		if state == purge.Completed && len(*message.Components) != 0 {
			t.Fatal("completed task still has buttons")
		}
	}
}

type integrationAPI struct {
	checks  atomic.Int32
	deleted atomic.Int32
}

func (a *integrationAPI) Authorize(context.Context, purge.Target) error { a.checks.Add(1); return nil }
func (a *integrationAPI) Messages(_ context.Context, ch, _ uint64) ([]purge.Message, error) {
	return []purge.Message{{ID: 100, ChannelID: ch, AuthorID: 42, CreatedAt: time.Now()}, {ID: 99, ChannelID: ch, AuthorID: 42, CreatedAt: time.Now()}}, nil
}
func (a *integrationAPI) Delete(context.Context, uint64, uint64, string) error {
	a.deleted.Add(1)
	return nil
}
func (a *integrationAPI) BulkDelete(_ context.Context, _ uint64, ids []uint64, _ string) error {
	a.deleted.Add(int32(len(ids)))
	return nil
}

// stubPostAPI 满足 postsearch.API，仅用于验证 handler 接线，不参与搜索用例。
type stubPostAPI struct{}

func (stubPostAPI) Authorize(context.Context, postsearch.Target) (postsearch.Forum, error) {
	return postsearch.Forum{}, postsearch.ErrNotForum
}

func (stubPostAPI) ActiveThreads(context.Context, uint64) ([]postsearch.Thread, error) {
	return nil, nil
}

func (stubPostAPI) ArchivedThreads(context.Context, uint64, time.Time) ([]postsearch.Thread, bool, error) {
	return nil, false, nil
}

func TestOfflineInteractionPreviewAndConfirmation(t *testing.T) {
	updates := make(chan discord.MessageUpdate, 20)
	api := testREST(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || !strings.HasPrefix(r.URL.Path, "/webhooks/123/") || !strings.HasSuffix(r.URL.Path, "/messages/@original") {
			t.Errorf("unexpected response request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var message discord.MessageUpdate
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		updates <- message
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"77","channel_id":"10","author":{"id":"8"},"type":0}`)
	})
	backend := &integrationAPI{}
	manager := purge.NewManager(context.Background(), backend, time.Now)
	handler := NewHandler(context.Background(), api, manager, stubPostAPI{}, 123, 9)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := handler.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	ack := make(chan struct{}, 10)
	respond := func(kind discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
		if kind != discord.InteractionResponseTypeDeferredCreateMessage {
			t.Errorf("not deferred: %v", kind)
		}
		message, ok := data.(discord.MessageCreate)
		if !ok || message.Flags&discord.MessageFlagEphemeral == 0 {
			t.Error("initial response is not ephemeral")
		}
		ack <- struct{}{}
		return nil
	}
	var command discord.ApplicationCommandInteraction
	payload := `{"id":"999","application_id":"123","type":2,"token":"offline-command-token","guild_id":"9","member":{"user":{"id":"7"},"roles":[]},"data":{"id":"5","name":"purge-user","type":1,"options":[{"name":"account","type":3,"value":"42"},{"name":"post","type":3,"value":"1"}]}}`
	if err := json.Unmarshal([]byte(payload), &command); err != nil {
		t.Fatal(err)
	}
	handler.OnCommand(&events.ApplicationCommandInteractionCreate{ApplicationCommandInteraction: command, Respond: func(kind discord.InteractionResponseType, data discord.InteractionResponseData, opts ...rest.RequestOpt) error {
		if backend.checks.Load() != 0 {
			t.Error("work started before defer")
		}
		return respond(kind, data, opts...)
	}})
	select {
	case <-ack:
	case <-time.After(time.Second):
		t.Fatal("no initial acknowledgement")
	}
	var first discord.MessageUpdate
	select {
	case first = <-updates:
	case <-time.After(time.Second):
		t.Fatal("no task response")
	}
	match := regexp.MustCompile("任务：`([a-f0-9]{32})`").FindStringSubmatch(*first.Content)
	if len(match) != 2 {
		t.Fatalf("no task ID in response: %s", *first.Content)
	}
	id := match[1]
	waitJob := func(state purge.State) {
		t.Helper()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		timeout := time.NewTimer(time.Second)
		defer timeout.Stop()
		for {
			if job, ok := manager.Snapshot(id); ok && job.State == state {
				return
			}
			select {
			case <-ticker.C:
			case <-timeout.C:
				t.Fatalf("job did not reach %s", state)
			}
		}
	}
	waitJob(purge.Ready)
	if backend.deleted.Load() != 0 {
		t.Fatal("preview deleted messages")
	}
	click := func(actor int) {
		var interaction discord.ComponentInteraction
		raw := fmt.Sprintf(`{"id":"1001","application_id":"123","type":3,"token":"offline-button-token","guild_id":"9","member":{"user":{"id":"%d"},"roles":[]},"data":{"component_type":2,"custom_id":"purge:v1:confirm:%s"}}`, actor, id)
		if err := json.Unmarshal([]byte(raw), &interaction); err != nil {
			t.Fatal(err)
		}
		handler.OnComponent(&events.ComponentInteractionCreate{ComponentInteraction: interaction, Respond: respond})
		select {
		case <-ack:
		case <-time.After(time.Second):
			t.Fatal("button not acknowledged")
		}
		select {
		case <-updates:
		case <-time.After(time.Second):
			t.Fatal("button not answered")
		}
	}
	click(99)
	if backend.deleted.Load() != 0 {
		t.Fatal("another user confirmed task")
	}
	click(7)
	waitJob(purge.Completed)
	click(7)
	if backend.deleted.Load() != 2 {
		t.Fatalf("duplicate confirmation or wrong snapshot: %d", backend.deleted.Load())
	}
}
