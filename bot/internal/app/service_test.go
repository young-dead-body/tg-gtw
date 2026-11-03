package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/company/telegram-gateway/bot/internal/api"
	"github.com/company/telegram-gateway/bot/internal/events"
	"github.com/stretchr/testify/require"
	tele "gopkg.in/telebot.v3"
)

type fakeAPI struct {
	creds      *api.Credentials
	user       *api.User
	err        error
	issuedFor  string
	linkedID   int64
	linkedCode string
}

func (f *fakeAPI) IssueCredentials(_ context.Context, id string) (*api.Credentials, error) {
	f.issuedFor = id
	return f.creds, f.err
}
func (f *fakeAPI) GetUser(context.Context, string) (*api.User, error) { return f.user, f.err }
func (f *fakeAPI) BindTelegram(_ context.Context, id int64, code string) error {
	f.linkedID = id
	f.linkedCode = code
	return f.err
}

type fakeSender struct {
	ids  []int64
	sent []string
}

func (f *fakeSender) Send(id int64, text string) error {
	f.ids = append(f.ids, id)
	f.sent = append(f.sent, text)
	return nil
}

type fakeContext struct {
	tele.Context
	kind  tele.ChatType
	id    int64
	args  []string
	reply string
}

func (c *fakeContext) Chat() *tele.Chat   { return &tele.Chat{ID: c.id, Type: c.kind} }
func (c *fakeContext) Sender() *tele.User { return &tele.User{ID: c.id, Username: "renamed_user"} }
func (c *fakeContext) Args() []string     { return c.args }
func (c *fakeContext) Reply(what interface{}, _ ...interface{}) error {
	c.reply = what.(string)
	return nil
}

func TestCredentialsOnlyInPrivateChat(t *testing.T) {
	f := &fakeAPI{creds: &api.Credentials{ResetURL: "https://gw/#reset=secret", ExpiresIn: 900}}
	s := NewService(f)
	c := &fakeContext{kind: tele.ChatGroup, id: -123}
	require.NoError(t, s.handleCredentials(c))
	require.Empty(t, f.issuedFor)
	require.NotContains(t, c.reply, "secret")
	c = &fakeContext{kind: tele.ChatPrivate, id: 123}
	require.NoError(t, s.handleCredentials(c))
	require.Equal(t, "123", f.issuedFor)
	require.Contains(t, c.reply, "https://gw/#reset=secret")
	require.NotContains(t, c.reply, "Пароль:")
}
func TestLinkUsesStableSenderID(t *testing.T) {
	f := &fakeAPI{}
	s := NewService(f)
	code := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	c := &fakeContext{kind: tele.ChatPrivate, id: 456, args: []string{code}}
	require.NoError(t, s.handleLink(c))
	require.EqualValues(t, 456, f.linkedID)
	require.Equal(t, code, f.linkedCode)
}
func TestLinkRejectedInGroup(t *testing.T) {
	f := &fakeAPI{}
	s := NewService(f)
	require.NoError(t, s.handleLink(&fakeContext{kind: tele.ChatGroup, id: -1}))
	require.Zero(t, f.linkedID)
}
func TestNotificationsUseVerifiedID(t *testing.T) {
	expires := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f := &fakeAPI{user: &api.User{ID: "1", Telegram: "@reassigned", TelegramUserID: 789, ExpiresAt: &expires}}
	s := NewService(f)
	sender := &fakeSender{}
	s.SetSender(sender)
	require.NoError(t, s.HandleEvent(context.Background(), events.Event{Name: eventAccessExpiring, UserID: "1"}))
	require.Equal(t, []int64{789}, sender.ids)
	require.Contains(t, sender.sent[0], "01.09.2026")
	f.user.TelegramUserID = 0
	require.NoError(t, s.HandleEvent(context.Background(), events.Event{Name: eventPasswordChanged, UserID: "1"}))
	require.Len(t, sender.sent, 1)
}
func TestNotificationResolveFailure(t *testing.T) {
	s := NewService(&fakeAPI{err: errors.New("offline")})
	require.Error(t, s.HandleEvent(context.Background(), events.Event{Name: eventAccessExpired, UserID: "1"}))
	require.NoError(t, s.HandleEvent(context.Background(), events.Event{Name: "unknown"}))
}
func TestCredentialsErrorMessages(t *testing.T) {
	for _, status := range []int{403, 404, 409, 500} {
		s := NewService(&fakeAPI{err: &api.APIError{Status: status}})
		require.NotEmpty(t, s.issueCredentials(context.Background(), "123", 123))
	}
}
