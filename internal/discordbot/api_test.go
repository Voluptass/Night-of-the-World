package discordbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/Voluptass/Night-of-the-World/internal/purge"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

func testREST(t *testing.T, handler http.HandlerFunc) rest.Rest {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := rest.NewClient("offline-test-token", rest.WithURL(server.URL), rest.WithHTTPClient(server.Client()), rest.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	api := rest.New(client)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		api.Close(ctx)
	})
	return api
}

func TestPermissionOverwriteOrder(t *testing.T) {
	required := discord.PermissionViewChannel | discord.PermissionReadMessageHistory | discord.PermissionManageMessages
	member := discord.Member{User: discord.User{ID: 7}, RoleIDs: []snowflake.ID{20, 21}}
	roles := []discord.Role{{ID: 9, Permissions: required}, {ID: 20}, {ID: 21}}
	cases := []struct {
		name       string
		overwrites discord.PermissionOverwrites
		want       bool
	}{
		{"base", nil, true},
		{"everyone deny", discord.PermissionOverwrites{discord.RolePermissionOverwrite{RoleID: 9, Deny: discord.PermissionManageMessages}}, false},
		{"role allow wins aggregate deny", discord.PermissionOverwrites{discord.RolePermissionOverwrite{RoleID: 20, Deny: discord.PermissionManageMessages}, discord.RolePermissionOverwrite{RoleID: 21, Allow: discord.PermissionManageMessages}}, true},
		{"member deny wins role allow", discord.PermissionOverwrites{discord.RolePermissionOverwrite{RoleID: 20, Allow: discord.PermissionManageMessages}, discord.MemberPermissionOverwrite{UserID: 7, Deny: discord.PermissionManageMessages}}, false},
		{"member allow wins role deny", discord.PermissionOverwrites{discord.RolePermissionOverwrite{RoleID: 20, Deny: discord.PermissionManageMessages}, discord.MemberPermissionOverwrite{UserID: 7, Allow: discord.PermissionManageMessages}}, true},
		{"hidden parent", discord.PermissionOverwrites{discord.MemberPermissionOverwrite{UserID: 7, Deny: discord.PermissionViewChannel}}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := channelPermissions(9, 99, roles, member, tt.overwrites, time.Now())
			if p.Has(required) != tt.want {
				t.Fatalf("permissions=%v", p)
			}
		})
	}
	deny := discord.PermissionOverwrites{discord.MemberPermissionOverwrite{UserID: 7, Deny: required}}
	if !channelPermissions(9, 7, roles, member, deny, time.Now()).Has(required) {
		t.Fatal("owner bypass missing")
	}
	adminRoles := append(append([]discord.Role(nil), roles...), discord.Role{ID: 20, Permissions: discord.PermissionAdministrator})
	if !channelPermissions(9, 99, adminRoles, member, deny, time.Now()).Has(required) {
		t.Fatal("administrator bypass missing")
	}
	until := time.Now().Add(time.Hour)
	member.CommunicationDisabledUntil = &until
	if channelPermissions(9, 99, roles, member, nil, time.Now()).Has(discord.PermissionManageMessages) {
		t.Fatal("timed-out member allowed")
	}
}

func TestAuthorizeUsingTargetParentAndFreshREST(t *testing.T) {
	required := discord.PermissionViewChannel | discord.PermissionReadMessageHistory | discord.PermissionManageMessages
	tests := []struct {
		name           string
		archived       bool
		locked         bool
		actorExtra     discord.Permissions
		botExtra       discord.Permissions
		actorDeny      discord.Permissions
		botDeny        discord.Permissions
		actorOwnsGuild bool
		wantErr        error
	}{
		{name: "allowed"},
		{name: "wrong guild", wantErr: purge.ErrInvalidTarget},
		{name: "normal channel", wantErr: purge.ErrInvalidTarget},
		{name: "private", wantErr: purge.ErrInvalidTarget},
		{name: "archived only", archived: true},
		{name: "locked only", locked: true},
		{name: "both need manage threads", archived: true, locked: true, wantErr: purge.ErrManageThreadsRequired},
		{name: "actor lacks manage threads", archived: true, locked: true, botExtra: discord.PermissionManageThreads, wantErr: purge.ErrManageThreadsRequired},
		{name: "bot lacks manage threads", archived: true, locked: true, actorExtra: discord.PermissionManageThreads, wantErr: purge.ErrManageThreadsRequired},
		{name: "both have manage threads", archived: true, locked: true, actorExtra: discord.PermissionManageThreads, botExtra: discord.PermissionManageThreads},
		{name: "actor admin does not grant bot permission", archived: true, locked: true, actorExtra: discord.PermissionAdministrator, wantErr: purge.ErrManageThreadsRequired},
		{name: "actor administrator", archived: true, locked: true, actorExtra: discord.PermissionAdministrator, botExtra: discord.PermissionManageThreads, actorDeny: discord.PermissionManageThreads},
		{name: "bot administrator", archived: true, locked: true, actorExtra: discord.PermissionManageThreads, botExtra: discord.PermissionAdministrator, botDeny: discord.PermissionManageThreads},
		{name: "guild owner", archived: true, locked: true, actorOwnsGuild: true, botExtra: discord.PermissionManageThreads},
		{name: "actor parent overwrite denies threads", archived: true, locked: true, actorExtra: discord.PermissionManageThreads, botExtra: discord.PermissionManageThreads, actorDeny: discord.PermissionManageThreads, wantErr: purge.ErrManageThreadsRequired},
		{name: "bot parent overwrite denies threads", archived: true, locked: true, actorExtra: discord.PermissionManageThreads, botExtra: discord.PermissionManageThreads, botDeny: discord.PermissionManageThreads, wantErr: purge.ErrManageThreadsRequired},
		{name: "actor denied", actorDeny: discord.PermissionManageMessages, wantErr: purge.ErrForbidden},
		{name: "bot denied", botDeny: discord.PermissionManageMessages, wantErr: purge.ErrForbidden},
		{name: "manage threads does not replace manage messages", archived: true, locked: true, actorExtra: discord.PermissionManageThreads, botExtra: discord.PermissionManageThreads, botDeny: discord.PermissionManageMessages, wantErr: purge.ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := testREST(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodGet {
					t.Errorf("authorization mutated resources: %s", r.Method)
					w.WriteHeader(400)
					return
				}
				switch r.URL.Path {
				case "/channels/1":
					kind := 11
					if tt.name == "normal channel" {
						kind = 0
					}
					if tt.name == "private" {
						kind = 12
					}
					guild := 9
					if tt.name == "wrong guild" {
						guild = 123
					}
					fmt.Fprintf(w, `{"id":"1","type":%d,"guild_id":"%d","parent_id":"10","thread_metadata":{"archived":%t,"locked":%t}}`, kind, guild, tt.archived, tt.locked)
				case "/channels/10":
					fmt.Fprintf(w, `{"id":"10","type":15,"guild_id":"9","permission_overwrites":[{"id":"7","type":1,"allow":"0","deny":"%d"},{"id":"8","type":1,"allow":"0","deny":"%d"}]}`, tt.actorDeny, tt.botDeny)
				case "/guilds/9":
					owner := 99
					if tt.actorOwnsGuild {
						owner = 7
					}
					fmt.Fprintf(w, `{"id":"9","owner_id":"%d"}`, owner)
				case "/guilds/9/roles":
					fmt.Fprintf(w, `[{"id":"9","permissions":"%d"},{"id":"20","permissions":"%d"},{"id":"21","permissions":"%d"}]`, required, tt.actorExtra, tt.botExtra)
				case "/guilds/9/members/7":
					fmt.Fprint(w, `{"user":{"id":"7"},"roles":["20"]}`)
				case "/guilds/9/members/8":
					fmt.Fprint(w, `{"user":{"id":"8"},"roles":["21"]}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			adapter := API{Rest: api, GuildID: 9, BotID: func() uint64 { return 8 }, Now: time.Now}
			err := adapter.Authorize(context.Background(), purge.Target{GuildID: 9, ThreadID: 1, ActorID: 7, AccountID: 42})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("authorization: got %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil && purge.SafeError(err) != tt.wantErr.Error() {
				t.Fatalf("permission error lost in user feedback: %s", purge.SafeError(err))
			}
		})
	}
}

func TestMessageRESTPathsPaginationAndAudit(t *testing.T) {
	bulkResult := make(chan []string, 1)
	api := testREST(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/channels/1/messages":
			if r.URL.Query().Get("before") != "2000" || r.URL.Query().Get("limit") != "100" {
				t.Errorf("wrong pagination: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `[{"id":"1000","channel_id":"1","author":{"id":"42"},"type":0},{"id":"999","channel_id":"1","author":{"id":"42"},"type":21},{"id":"1","channel_id":"1","author":{"id":"42"},"type":0}]`)
		case r.Method == http.MethodDelete && r.URL.Path == "/channels/1/messages/1000":
			reason, err := url.QueryUnescape(r.Header.Get("X-Audit-Log-Reason"))
			if err != nil || reason != "purge actor=7 测试" {
				t.Errorf("audit reason missing/incorrect")
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/channels/1/messages/bulk-delete":
			var body struct {
				Messages []string `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			bulkResult <- body.Messages
			if r.Header.Get("X-Audit-Log-Reason") == "" {
				t.Error("missing bulk audit reason")
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected mutation/request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
		}
	})
	adapter := API{Rest: api}
	messages, err := adapter.Messages(context.Background(), 1, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].AuthorID != 42 || messages[0].Protected || !messages[1].Protected || !messages[2].Protected {
		t.Fatalf("messages=%+v", messages)
	}
	if err := adapter.Delete(context.Background(), 1, 1000, "purge actor=7 测试"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.BulkDelete(context.Background(), 1, []uint64{1000, 998}, "purge actor=7"); err != nil {
		t.Fatal(err)
	}
	bulk := <-bulkResult
	if !reflect.DeepEqual(bulk, []string{"1000", "998"}) {
		t.Fatalf("bulk IDs=%v", bulk)
	}
}

func TestAPIErrorClassification(t *testing.T) {
	for _, tt := range []struct {
		name     string
		code     rest.JSONErrorCode
		status   int
		deleting bool
		want     error
	}{
		{"missing message", 10008, 404, true, purge.ErrMessageGone},
		{"missing thread", 10003, 404, true, purge.ErrInvalidTarget},
		{"old batch", 50034, 400, true, purge.ErrTooOld},
		{"unauthorized", 0, 401, true, purge.ErrForbidden},
		{"forbidden", 0, 403, true, purge.ErrForbidden},
		{"rate limit", 0, 429, true, purge.ErrUnavailable},
		{"server uncertain", 0, 502, true, purge.ErrUncertain},
		{"read server failure", 0, 502, false, purge.ErrUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := apiError(&rest.Error{Code: tt.code, Message: "secret error body", Response: &http.Response{StatusCode: tt.status}}, tt.deleting)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v want %v", err, tt.want)
			}
		})
	}
	if !errors.Is(apiError(context.DeadlineExceeded, true), purge.ErrUncertain) {
		t.Fatal("delete timeout not marked uncertain")
	}
	if !errors.Is(apiError(context.Canceled, false), context.Canceled) {
		t.Fatal("read cancellation lost")
	}
}
